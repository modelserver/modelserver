package admin

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/billing"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/metrics"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// creditUnitPrices holds the per-million-credit price in each supported
// currency and the implicit exchange rate (for informational display only).
type creditUnitPrices struct {
	CNYFenPerMillion   int64   `json:"cny_fen_per_million"`
	USDCentsPerMillion int64   `json:"usd_cents_per_million"`
	ImplicitUSDToCNY   float64 `json:"implicit_usd_to_cny_rate"`
}

// topupAmounts holds the topup bound (min or max) in each supported currency.
type topupAmounts struct {
	CNYFen   int64 `json:"cny_fen"`
	USDCents int64 `json:"usd_cents"`
}

// extraUsageGetResponse packs settings + derived counters for the dashboard.
type extraUsageGetResponse struct {
	Enabled             bool             `json:"enabled"`
	BalanceCredits      int64            `json:"balance_credits"`
	MonthlyLimitCredits int64            `json:"monthly_limit_credits"`
	MonthlySpentCredits int64            `json:"monthly_spent_credits"`
	MonthlyWindowStart  string           `json:"monthly_window_start"`
	BypassBalanceCheck  bool             `json:"bypass_balance_check"`
	UpdatedAt           time.Time        `json:"updated_at,omitempty"`

	CreditUnitPrices creditUnitPrices `json:"credit_unit_prices"`
	MinTopup         topupAmounts     `json:"min_topup"`
	MaxTopup         topupAmounts     `json:"max_topup"`
	DailyTopupLimit  int64            `json:"daily_topup_limit_credits"`
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
		monthStart := store.MonthWindowStart()
		spent, err := st.GetMonthlyExtraSpendCredits(projectID, monthStart)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to sum monthly spend")
			return
		}
		resp := extraUsageGetResponse{
			MonthlyWindowStart: monthStart.Format(time.RFC3339),
			CreditUnitPrices: creditUnitPrices{
				CNYFenPerMillion:   cfg.CreditPriceCNYFen,
				USDCentsPerMillion: cfg.CreditPriceUSDCents,
				ImplicitUSDToCNY:   float64(cfg.CreditPriceCNYFen) / float64(cfg.CreditPriceUSDCents),
			},
			MinTopup:        topupAmounts{CNYFen: cfg.MinTopupCNYFen, USDCents: cfg.MinTopupUSDCents},
			MaxTopup:        topupAmounts{CNYFen: cfg.MaxTopupCNYFen, USDCents: cfg.MaxTopupUSDCents},
			DailyTopupLimit: cfg.DailyTopupLimitCredits,
		}
		if settings != nil {
			resp.Enabled = settings.Enabled
			resp.BalanceCredits = settings.BalanceCredits
			resp.MonthlyLimitCredits = settings.MonthlyLimitCredits
			resp.BypassBalanceCheck = settings.BypassBalanceCheck
			resp.UpdatedAt = settings.UpdatedAt
		}
		resp.MonthlySpentCredits = spent
		writeData(w, http.StatusOK, resp)
	}
}

// handleUpdateExtraUsage lets project owners/maintainers toggle `enabled`
// or change the monthly limit. Balance is intentionally NOT writable
// here. Developers and Viewers must NOT be able to enable extra usage
// or raise the monthly cap — that would let any project member spend
// the project's money post-quota.
func handleUpdateExtraUsage(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		var body struct {
			Enabled             *bool  `json:"enabled"`
			MonthlyLimitCredits *int64 `json:"monthly_limit_credits"`
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
			monthlyLimit = existing.MonthlyLimitCredits
		}
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		if body.MonthlyLimitCredits != nil {
			if *body.MonthlyLimitCredits < 0 {
				writeError(w, http.StatusBadRequest, "bad_request", "monthly_limit_credits must be >= 0")
				return
			}
			monthlyLimit = *body.MonthlyLimitCredits
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
// provider, and returns the payment URL. Owners/Maintainers only —
// allowing Developers/Viewers to mint payment intents would let any
// member trigger billing the Owner did not authorize.
func handleCreateExtraUsageTopup(st *store.Store, payClient billing.PaymentClient, billingCfg config.BillingConfig, euCfg config.ExtraUsageConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")

		var body struct {
			AmountFen int64  `json:"amount_fen"`
			Channel   string `json:"channel"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.AmountFen < euCfg.MinTopupCNYFen {
			writeError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("amount_fen must be >= %d", euCfg.MinTopupCNYFen))
			return
		}
		if body.AmountFen > euCfg.MaxTopupCNYFen {
			writeError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("amount_fen must be <= %d", euCfg.MaxTopupCNYFen))
			return
		}

		// Daily accumulated limit.
		daily, err := st.SumDailyExtraUsageTopupCredits(projectID, store.DayWindowStart())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to check daily topup cap")
			return
		}
		// Convert the requested fen amount to credits-equivalent for the cap
		// comparison. Topup credits = amount_fen × 1M / credit_price_cny_fen
		// (matches the conversion the delivery handler will apply later).
		creditsRequested := (body.AmountFen * 1_000_000) / euCfg.CreditPriceCNYFen
		if euCfg.DailyTopupLimitCredits > 0 && daily+creditsRequested > euCfg.DailyTopupLimitCredits {
			writeError(w, http.StatusConflict, "daily_topup_limit",
				fmt.Sprintf("daily topup limit %d credits reached", euCfg.DailyTopupLimitCredits))
			return
		}

		order := &types.Order{
			ProjectID:               projectID,
			Periods:                 1,
			UnitPrice:               body.AmountFen,
			Amount:                  body.AmountFen,
			Currency:                "CNY",
			Status:                  types.OrderStatusPending,
			Channel:                 body.Channel,
			Metadata:                "{}",
			OrderType:               types.OrderTypeExtraUsageTopup,
			ExtraUsageAmountCredits: creditsRequested,
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
	Spend7DaysCredits int64 `json:"spend_7d_credits"`
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
			spend, err := st.SumRecentExtraUsageSpendCredits(s.ProjectID, 7)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to sum recent spend")
				return
			}
			out = append(out, adminExtraUsageOverviewRow{ExtraUsageSettings: s, Spend7DaysCredits: spend})
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
			AmountCredits int64  `json:"amount_credits"`
			Description   string `json:"description"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.AmountCredits <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "amount_credits must be > 0")
			return
		}
		bal, err := st.TopUpExtraUsage(store.TopUpExtraUsageReq{
			ProjectID:     projectID,
			AmountCredits: body.AmountCredits,
			Reason:        types.ExtraUsageReasonAdminAdjust,
			Description:   body.Description,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to top up: "+err.Error())
			return
		}
		metrics.SetExtraUsageBalance(projectID, bal)
		writeData(w, http.StatusOK, map[string]interface{}{
			"project_id":      projectID,
			"balance_credits": bal,
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
	if order.ExtraUsageAmountCredits <= 0 {
		return 0, errors.New("topup order has zero amount")
	}
	bal, err := st.TopUpExtraUsage(store.TopUpExtraUsageReq{
		ProjectID:     order.ProjectID,
		AmountCredits: order.ExtraUsageAmountCredits,
		OrderID:       order.ID,
		Reason:        types.ExtraUsageReasonUserTopup,
		Description:   fmt.Sprintf("order=%s channel=%s", order.ID, order.Channel),
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

// handleAdminExtraUsageSetBypass flips the bypass flag on a project's
// extra-usage settings. Superadmin only. Creates the settings row if
// absent so the flag can be set on projects that have never topped up.
func handleAdminExtraUsageSetBypass(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		var body struct {
			Bypass *bool `json:"bypass"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Bypass == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "bypass field required")
			return
		}

		out, err := st.SetExtraUsageBypass(projectID, *body.Bypass)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to set bypass")
			return
		}

		actorID := ""
		if actor := UserFromContext(r.Context()); actor != nil {
			actorID = actor.ID
		}
		slog.Default().Info("extra_usage_bypass_toggled",
			"project_id", projectID,
			"bypass", *body.Bypass,
			"actor_user_id", actorID)

		writeData(w, http.StatusOK, out)
	}
}

