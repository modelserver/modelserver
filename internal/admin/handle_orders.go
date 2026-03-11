package admin

import (
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/billing"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListOrders(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		orders, err := st.ListOrdersByProject(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list orders")
			return
		}
		writeData(w, http.StatusOK, orders)
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
		if plan.GroupTag != "" && plan.GroupTag != project.BillingTag {
			writeError(w, http.StatusForbidden, "forbidden", "plan not available for this project")
			return
		}

		// Check for active subscription.
		activeSub, err := st.GetActiveSubscription(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to check active subscription")
			return
		}

		var orderType string
		var unitPrice int64
		var amount int64
		var existingSubID string
		periods := body.Periods

		if activeSub == nil {
			// No active subscription — new order.
			orderType = types.OrderTypeNew
			unitPrice = plan.PricePerPeriod
			amount = unitPrice * int64(periods)
		} else if activeSub.PlanID == plan.ID || activeSub.PlanName == plan.Slug {
			// Same plan — renew.
			orderType = types.OrderTypeRenew
			unitPrice = plan.PricePerPeriod
			amount = unitPrice * int64(periods)
			existingSubID = activeSub.ID
		} else {
			// Different plan — check tier.
			activePlan, err := st.GetPlanBySlug(activeSub.PlanName)
			if err != nil || activePlan == nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to look up active plan")
				return
			}
			if plan.TierLevel <= activePlan.TierLevel {
				writeError(w, http.StatusConflict, "downgrade_not_allowed", "cannot downgrade to a lower or same tier plan")
				return
			}

			// Upgrade — calculate prorated price.
			orderType = types.OrderTypeUpgrade
			existingSubID = activeSub.ID

			now := time.Now()
			remaining := activeSub.ExpiresAt.Sub(now)
			// Use real calendar month duration for proration instead of 30-day approximation.
			periodStart := now
			periodEnd := periodStart.AddDate(0, activePlan.PeriodMonths, 0)
			periodDuration := periodEnd.Sub(periodStart)
			remainingPeriods := int(math.Ceil(float64(remaining) / float64(periodDuration)))
			if remainingPeriods < 1 {
				remainingPeriods = 1
			}

			unitPrice = plan.PricePerPeriod - activePlan.PricePerPeriod
			if unitPrice < 0 {
				unitPrice = 0
			}
			amount = unitPrice * int64(remainingPeriods)
			periods = remainingPeriods
		}

		// Create order.
		order := &types.Order{
			ProjectID:              projectID,
			PlanID:                 plan.ID,
			OrderType:              orderType,
			Periods:                periods,
			UnitPrice:              unitPrice,
			Amount:                 amount,
			Currency:               "CNY",
			Status:                 types.OrderStatusPending,
			ExistingSubscriptionID: existingSubID,
			Metadata:               "{}",
		}
		if err := st.CreateOrder(order); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create order: "+err.Error())
			return
		}

		// Call payment provider if configured.
		if payClient != nil {
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
		}

		writeData(w, http.StatusCreated, order)
	}
}
