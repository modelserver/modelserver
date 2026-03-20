package admin

import (
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/billing"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/ratelimit"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListOrders(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		p := parsePagination(r)
		orders, total, err := st.ListOrdersByProject(projectID, p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list orders")
			return
		}
		writeList(w, orders, total, p.Page, p.Limit())
	}
}

func handleGetOrder(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		order, err := st.GetOrderByID(chi.URLParam(r, "orderID"))
		if err != nil || order == nil {
			writeError(w, http.StatusNotFound, "not_found", "order not found")
			return
		}
		writeData(w, http.StatusOK, order)
	}
}

func handleCancelOrder(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orderID := chi.URLParam(r, "orderID")
		order, err := st.GetOrderByID(orderID)
		if err != nil || order == nil {
			writeError(w, http.StatusNotFound, "not_found", "order not found")
			return
		}
		if order.Status != types.OrderStatusPending && order.Status != types.OrderStatusPaying {
			writeError(w, http.StatusConflict, "not_cancellable", "only pending or paying orders can be cancelled")
			return
		}
		ok, err := st.CancelOrder(orderID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to cancel order")
			return
		}
		if !ok {
			writeError(w, http.StatusConflict, "not_cancellable", "order status has changed")
			return
		}
		order.Status = types.OrderStatusCancelled
		writeData(w, http.StatusOK, order)
	}
}

func handleCreateOrder(st *store.Store, payClient billing.PaymentClient, billingCfg config.BillingConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")

		var body struct {
			PlanSlug string `json:"plan_slug"`
			Periods  int    `json:"periods"`
			Channel  string `json:"channel"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.PlanSlug == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "plan_slug is required")
			return
		}
		if body.Periods <= 0 {
			body.Periods = 1
		}

		// Look up plan.
		plan, err := st.GetPlanBySlug(body.PlanSlug)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to look up plan")
			return
		}
		if plan == nil || !plan.IsActive {
			writeError(w, http.StatusNotFound, "not_found", "plan not found or inactive")
			return
		}

		// Look up project and check group_tag match.
		project, err := st.GetProjectByID(projectID)
		if err != nil || project == nil {
			writeError(w, http.StatusNotFound, "not_found", "project not found")
			return
		}
		if plan.GroupTag != "" && !containsString(project.BillingTags, plan.GroupTag) {
			writeError(w, http.StatusForbidden, "forbidden", "plan not available for this project")
			return
		}

		// Every project must have an active subscription (at minimum, free).
		activeSub, err := st.GetActiveSubscription(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to check active subscription")
			return
		}
		if activeSub == nil {
			writeError(w, http.StatusBadRequest, "no_subscription", "no active subscription — cannot create order")
			return
		}
		isRenewal := activeSub.PlanID == plan.ID || activeSub.PlanName == plan.Slug

		// Look up active plan for tier checks and pricing.
		activePlan, err := st.GetPlanBySlug(activeSub.PlanName)
		if err != nil || activePlan == nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to look up active plan")
			return
		}

		// Must be an upgrade or renewal (same plan).
		if !isRenewal && plan.TierLevel <= activePlan.TierLevel {
			writeError(w, http.StatusConflict, "downgrade_not_allowed", "cannot downgrade to a lower or same tier plan")
			return
		}

		// Prevent duplicate orders — only one paying order allowed at a time.
		hasPaying, err := st.HasPayingOrder(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to check existing orders")
			return
		}
		if hasPaying {
			writeError(w, http.StatusConflict, "paying_order_exists", "a paying order already exists — please cancel it first or wait for it to complete")
			return
		}

		var unitPrice int64
		var amount int64
		periods := body.Periods
		existingSubID := activeSub.ID

		if isRenewal {
			// Renewal: same plan, user picks periods, full price.
			unitPrice = plan.PricePerPeriod
			amount = unitPrice * int64(periods)
		} else if activePlan.PricePerPeriod == 0 {
			// Free → paid: user picks periods, full price.
			unitPrice = plan.PricePerPeriod
			amount = unitPrice * int64(periods)
		} else {
			// Paid → paid upgrade: credit remaining value of current subscription.
			now := time.Now()
			totalDuration := activeSub.ExpiresAt.Sub(activeSub.StartsAt)
			usedDuration := now.Sub(activeSub.StartsAt)
			var remainingValue int64
			if totalDuration > 0 && usedDuration < totalDuration {
				fraction := float64(totalDuration-usedDuration) / float64(totalDuration)
				remainingValue = int64(math.Round(fraction * float64(activePlan.PricePerPeriod)))
			}
			unitPrice = plan.PricePerPeriod - remainingValue
			if unitPrice < 0 {
				unitPrice = 0
			}
			amount = unitPrice
			periods = 1
		}

		// Create order.
		order := &types.Order{
			ProjectID:              projectID,
			PlanID:                 plan.ID,
			Periods:                periods,
			UnitPrice:              unitPrice,
			Amount:                 amount,
			Currency:               "CNY",
			Status:                 types.OrderStatusPending,
			Channel:                body.Channel,
			ExistingSubscriptionID: existingSubID,
			Metadata:               "{}",
		}
		if err := st.CreateOrder(order); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create order: "+err.Error())
			return
		}

		// Call payment provider — required for paid orders.
		if payClient == nil {
			_ = st.UpdateOrderStatus(order.ID, types.OrderStatusFailed)
			writeError(w, http.StatusServiceUnavailable, "payment_not_configured", "payment provider is not configured")
			return
		}
		payResp, err := payClient.CreatePayment(r.Context(), billing.PaymentRequest{
			OrderID:     order.ID,
			ProductName: plan.DisplayName,
			Channel:     body.Channel,
			Currency:    order.Currency,
			Amount:      order.Amount,
			NotifyURL:   billingCfg.NotifyURL,
			ReturnURL:   billingCfg.ReturnURL,
		})
		if err != nil {
			_ = st.UpdateOrderStatus(order.ID, types.OrderStatusFailed)
			writeError(w, http.StatusBadGateway, "payment_error", "failed to create payment")
			return
		}
		if err := st.UpdateOrderPayment(order.ID, payResp.PaymentRef, payResp.PaymentURL, types.OrderStatusPaying); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update order payment")
			return
		}
		order.PaymentRef = payResp.PaymentRef
		order.PaymentURL = payResp.PaymentURL
		order.Status = types.OrderStatusPaying

		writeData(w, http.StatusCreated, order)
	}
}

func handleSubscriptionUsage(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")

		activeSub, err := st.GetActiveSubscription(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to get active subscription")
			return
		}
		if activeSub == nil {
			writeData(w, http.StatusOK, []ratelimit.CreditWindowStatus{})
			return
		}

		plan, err := st.GetPlanBySlug(activeSub.PlanName)
		if err != nil || plan == nil {
			writeData(w, http.StatusOK, []ratelimit.CreditWindowStatus{})
			return
		}

		policy := plan.ToPolicy(projectID, &activeSub.StartsAt)

		statuses := make([]ratelimit.CreditWindowStatus, 0, len(policy.CreditRules))
		for _, rule := range policy.CreditRules {
			windowStart := ratelimit.WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)
			used, err := st.SumCreditsInWindowByProject(projectID, windowStart)
			if err != nil {
				used = 0
			}
			percentage := 0.0
			if rule.MaxCredits > 0 {
				percentage = (used / float64(rule.MaxCredits)) * 100
				if percentage > 100 {
					percentage = 100
				}
			}
			s := ratelimit.CreditWindowStatus{
				Window:     rule.Window,
				Percentage: percentage,
			}
			if rule.WindowType == types.WindowTypeCalendar || rule.WindowType == types.WindowTypeFixed {
				resetDur := ratelimit.WindowResetDuration(rule.Window, rule.WindowType, rule.AnchorTime)
				s.ResetsAt = time.Now().UTC().Add(resetDur).Format(time.RFC3339)
			}
			statuses = append(statuses, s)
		}

		writeData(w, http.StatusOK, statuses)
	}
}
