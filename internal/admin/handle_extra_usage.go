package admin

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/billing"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/metrics"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// extraUsageGetResponse packs settings + derived counters for the dashboard.
type extraUsageGetResponse struct {
	Enabled            bool      `json:"enabled"`
	BalanceFen         int64     `json:"balance_fen"`
	MonthlyLimitFen    int64     `json:"monthly_limit_fen"`
	MonthlySpentFen    int64     `json:"monthly_spent_fen"`
	MonthlyWindowStart string    `json:"monthly_window_start"`
	CreditPriceFen     int64     `json:"credit_price_fen"`
	MinTopupFen        int64     `json:"min_topup_fen"`
	MaxTopupFen        int64     `json:"max_topup_fen"`
	DailyTopupLimitFen int64     `json:"daily_topup_limit_fen"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

// handleGetExtraUsage returns the project's extra-usage state + policy
// knobs the dashboard needs to render the page.
func handleGetExtraUsage(st *store.Store, cfg config.ExtraUsageConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		settings, err := st.GetExtraUsageSettings(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to load extra usage settings")
			return
		}
		spent, err := st.GetMonthlyExtraSpendFen(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to sum monthly spend")
			return
		}
		resp := extraUsageGetResponse{
			MonthlyWindowStart: monthWindowStart(cfg.MonthlyWindowTZ).Format(time.RFC3339),
			CreditPriceFen:     cfg.CreditPriceFen,
			MinTopupFen:        cfg.MinTopupFen,
			MaxTopupFen:        cfg.MaxTopupFen,
			DailyTopupLimitFen: cfg.DailyTopupLimitFen,
		}
		if settings != nil {
			resp.Enabled = settings.Enabled
			resp.BalanceFen = settings.BalanceFen
			resp.MonthlyLimitFen = settings.MonthlyLimitFen
			resp.UpdatedAt = settings.UpdatedAt
		}
		resp.MonthlySpentFen = spent
		writeData(w, http.StatusOK, resp)
	}
}

// handleUpdateExtraUsage lets the project owner toggle `enabled` or change
// the monthly limit. Balance is intentionally NOT writable here.
func handleUpdateExtraUsage(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		var body struct {
			Enabled         *bool  `json:"enabled"`
			MonthlyLimitFen *int64 `json:"monthly_limit_fen"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		// Fetch existing to preserve unspecified fields.
		existing, err := st.GetExtraUsageSettings(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to load settings")
			return
		}
		enabled := false
		var monthlyLimit int64
		if existing != nil {
			enabled = existing.Enabled
			monthlyLimit = existing.MonthlyLimitFen
		}
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		if body.MonthlyLimitFen != nil {
			if *body.MonthlyLimitFen < 0 {
				writeError(w, http.StatusBadRequest, "bad_request", "monthly_limit_fen must be >= 0")
				return
			}
			monthlyLimit = *body.MonthlyLimitFen
		}

		out, err := st.UpsertExtraUsageSettings(projectID, enabled, monthlyLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to save settings")
			return
		}
		writeData(w, http.StatusOK, out)
	}
}

// handleListExtraUsageTransactions paginates the ledger.
func handleListExtraUsageTransactions(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		p := parsePagination(r)
		typeFilter := r.URL.Query().Get("type")
		txs, total, err := st.ListExtraUsageTransactions(projectID, p, typeFilter)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list transactions")
			return
		}
		writeList(w, txs, total, p.Page, p.Limit())
	}
}

// handleCreateExtraUsageTopup creates a topup order, calls the payment
// provider, and returns the payment URL.
func handleCreateExtraUsageTopup(st *store.Store, payClient billing.PaymentClient, billingCfg config.BillingConfig, euCfg config.ExtraUsageConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")

		var body struct {
			AmountFen int64  `json:"amount_fen"`
			Channel   string `json:"channel"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.AmountFen < euCfg.MinTopupFen {
			writeError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("amount_fen must be >= %d", euCfg.MinTopupFen))
			return
		}
		if body.AmountFen > euCfg.MaxTopupFen {
			writeError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("amount_fen must be <= %d", euCfg.MaxTopupFen))
			return
		}

		// Daily accumulated limit.
		daily, err := st.SumDailyExtraUsageTopupFen(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to check daily topup cap")
			return
		}
		if euCfg.DailyTopupLimitFen > 0 && daily+body.AmountFen > euCfg.DailyTopupLimitFen {
			writeError(w, http.StatusConflict, "daily_topup_limit",
				fmt.Sprintf("daily topup limit %d fen reached", euCfg.DailyTopupLimitFen))
			return
		}

		order := &types.Order{
			ProjectID:           projectID,
			Periods:             1,
			UnitPrice:           body.AmountFen,
			Amount:              body.AmountFen,
			Currency:            "CNY",
			Status:              types.OrderStatusPending,
			Channel:             body.Channel,
			Metadata:            "{}",
			OrderType:           types.OrderTypeExtraUsageTopup,
			ExtraUsageAmountFen: body.AmountFen,
		}
		if err := st.CreateOrder(order); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create order: "+err.Error())
			return
		}

		if payClient == nil {
			_ = st.UpdateOrderStatus(order.ID, types.OrderStatusFailed)
			writeError(w, http.StatusServiceUnavailable, "payment_not_configured", "payment provider is not configured")
			return
		}
		payResp, err := payClient.CreatePayment(r.Context(), billing.PaymentRequest{
			OrderID:     order.ID,
			ProductName: fmt.Sprintf("extra-usage topup ¥%.2f", float64(body.AmountFen)/100),
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

// handleGetExtraUsageTopup fetches a single topup order's status (polled by
// the frontend while the user pays).
func handleGetExtraUsageTopup(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orderID := chi.URLParam(r, "orderID")
		order, err := st.GetOrderByID(orderID)
		if err != nil || order == nil {
			writeError(w, http.StatusNotFound, "not_found", "order not found")
			return
		}
		if order.OrderType != types.OrderTypeExtraUsageTopup {
			writeError(w, http.StatusNotFound, "not_found", "order not found")
			return
		}
		writeData(w, http.StatusOK, order)
	}
}

// handleAdminExtraUsageOverview returns every enabled project's settings and
// recent spend. Superadmin only.
type adminExtraUsageOverviewRow struct {
	types.ExtraUsageSettings
	Spend7DaysFen int64 `json:"spend_7d_fen"`
}

func handleAdminExtraUsageOverview(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := st.ListExtraUsageSettings()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list settings")
			return
		}
		out := make([]adminExtraUsageOverviewRow, 0, len(rows))
		for _, s := range rows {
			spend, err := st.SumRecentExtraUsageSpendFen(s.ProjectID, 7)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to sum recent spend")
				return
			}
			out = append(out, adminExtraUsageOverviewRow{ExtraUsageSettings: s, Spend7DaysFen: spend})
		}
		writeData(w, http.StatusOK, out)
	}
}

// handleAdminExtraUsageDirectTopup lets superadmins credit a project without
// going through the payment provider. Used for manual ops and E2E tests.
func handleAdminExtraUsageDirectTopup(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		var body struct {
			AmountFen   int64  `json:"amount_fen"`
			Description string `json:"description"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.AmountFen <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "amount_fen must be > 0")
			return
		}
		bal, err := st.TopUpExtraUsage(store.TopUpExtraUsageReq{
			ProjectID:   projectID,
			AmountFen:   body.AmountFen,
			Reason:      types.ExtraUsageReasonAdminAdjust,
			Description: body.Description,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to top up: "+err.Error())
			return
		}
		metrics.SetExtraUsageBalance(projectID, bal)
		writeData(w, http.StatusOK, map[string]interface{}{
			"project_id":  projectID,
			"balance_fen": bal,
		})
	}
}

// deliverExtraUsageTopupOrder is the webhook-driven branch that applies a
// paid top-up order to the project's balance. It is shared between the
// delivery webhook handler and any future admin-driven retry path.
func deliverExtraUsageTopupOrder(st *store.Store, order *types.Order) (int64, error) {
	if order.OrderType != types.OrderTypeExtraUsageTopup {
		return 0, errors.New("not an extra-usage topup order")
	}
	if order.ExtraUsageAmountFen <= 0 {
		return 0, errors.New("topup order has zero amount")
	}
	bal, err := st.TopUpExtraUsage(store.TopUpExtraUsageReq{
		ProjectID:   order.ProjectID,
		AmountFen:   order.ExtraUsageAmountFen,
		OrderID:     order.ID,
		Reason:      types.ExtraUsageReasonUserTopup,
		Description: fmt.Sprintf("order=%s channel=%s", order.ID, order.Channel),
	})
	if err != nil {
		return 0, fmt.Errorf("apply topup: %w", err)
	}
	if err := st.UpdateOrderStatus(order.ID, types.OrderStatusDelivered); err != nil {
		// Log only; the idempotent ledger row is already in place, the next
		// webhook/delivery will mark the status.
		return bal, fmt.Errorf("topup applied but mark delivered failed: %w", err)
	}
	metrics.IncExtraUsageTopup(order.Channel)
	metrics.SetExtraUsageBalance(order.ProjectID, bal)
	return bal, nil
}

// monthWindowStart returns the start of the current month in the configured
// timezone (default Asia/Shanghai). Only used to tell the dashboard when the
// window resets — the store uses its own SQL-side computation.
func monthWindowStart(tzName string) time.Time {
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc, _ = time.LoadLocation("Asia/Shanghai")
	}
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
}
