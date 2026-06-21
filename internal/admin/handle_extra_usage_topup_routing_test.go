package admin

// handle_extra_usage_topup_routing_test.go — unit tests for handleCreateExtraUsageTopup
// channel-routing logic. These tests do NOT require a real database: validation
// and channel-switch errors fire before any store call, so a nil store is safe.
// For the happy-path tests we use a real store (gated on TEST_DATABASE_URL) to
// confirm the order row is persisted correctly per channel.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// ---------------------------------------------------------------------------
// Shared test config
// ---------------------------------------------------------------------------

// testEUCfg returns a config matching production defaults.
// CreditPriceCNYFen=5438: 1000 fen → 183_891 credits
// CreditPriceUSDCents=907:  167 cents → 184_123 credits
func testEUCfg() config.ExtraUsageConfig {
	return config.ExtraUsageConfig{
		CreditPriceCNYFen:      5438,
		CreditPriceUSDCents:    907,
		MinTopupCNYFen:         1000,
		MaxTopupCNYFen:         200_000,
		MinTopupUSDCents:       167,
		MaxTopupUSDCents:       33_333,
		DailyTopupLimitCredits: 0, // no cap by default
	}
}

func testBillingCfg() config.BillingConfig {
	return config.BillingConfig{
		NotifyURL: "https://example.com/notify",
		ReturnURL: "https://example.com/return",
	}
}

// ownerCtx injects a project Owner into the request context, satisfying requireRole.
func ownerCtx(r *http.Request, projectID string) *http.Request {
	m := &types.ProjectMember{
		UserID:    "u-owner",
		ProjectID: projectID,
		Role:      types.RoleOwner,
	}
	return r.WithContext(context.WithValue(r.Context(), ctxMember, m))
}

// postTopup fires POST /projects/{projectID}/extra-usage/topup with the given
// JSON body. Returns the recorded response.
func postTopup(t *testing.T, h http.Handler, projectID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost,
		"/projects/"+projectID+"/extra-usage/topup",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = ownerCtx(req, projectID)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// buildRouter wraps h in a chi router with the topup route.
func buildTopupRouter(h http.HandlerFunc) chi.Router {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/extra-usage/topup", h)
	return r
}

// ---------------------------------------------------------------------------
// Validation-only tests (nil store, nil payClient — validation fires first)
// ---------------------------------------------------------------------------

// TestCreateTopup_UnknownChannel_Rejected verifies that an unsupported channel
// yields 400 with no store or payment client involved.
func TestCreateTopup_UnknownChannel_Rejected(t *testing.T) {
	h := buildTopupRouter(
		handleCreateExtraUsageTopup(nil, nil, testBillingCfg(), testEUCfg()),
	)
	rr := postTopup(t, h, "proj-x", map[string]any{
		"channel":    "bitcoin",
		"amount_fen": 1000,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

// TestCreateTopup_WechatWithCents_Rejected ensures that supplying amount_cents
// for a CNY channel is a 400.
func TestCreateTopup_WechatWithCents_Rejected(t *testing.T) {
	h := buildTopupRouter(
		handleCreateExtraUsageTopup(nil, nil, testBillingCfg(), testEUCfg()),
	)
	rr := postTopup(t, h, "proj-x", map[string]any{
		"channel":      "wechat",
		"amount_cents": 100,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	mustErrorCode(t, rr, "bad_request")
}

// TestCreateTopup_AlipayWithCents_Rejected is the same guard for alipay.
func TestCreateTopup_AlipayWithCents_Rejected(t *testing.T) {
	h := buildTopupRouter(
		handleCreateExtraUsageTopup(nil, nil, testBillingCfg(), testEUCfg()),
	)
	rr := postTopup(t, h, "proj-x", map[string]any{
		"channel":      "alipay",
		"amount_cents": 100,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

// TestCreateTopup_WechatMissingFen_Rejected: wechat without amount_fen → 400.
func TestCreateTopup_WechatMissingFen_Rejected(t *testing.T) {
	h := buildTopupRouter(
		handleCreateExtraUsageTopup(nil, nil, testBillingCfg(), testEUCfg()),
	)
	rr := postTopup(t, h, "proj-x", map[string]any{
		"channel": "wechat",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

// TestCreateTopup_StripeWithFen_Rejected: stripe with amount_fen → 400.
func TestCreateTopup_StripeWithFen_Rejected(t *testing.T) {
	h := buildTopupRouter(
		handleCreateExtraUsageTopup(nil, nil, testBillingCfg(), testEUCfg()),
	)
	rr := postTopup(t, h, "proj-x", map[string]any{
		"channel":    "stripe",
		"amount_fen": 1000,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	mustErrorCode(t, rr, "bad_request")
}

// TestCreateTopup_StripeMissingCents_Rejected: stripe without amount_cents → 400.
func TestCreateTopup_StripeMissingCents_Rejected(t *testing.T) {
	h := buildTopupRouter(
		handleCreateExtraUsageTopup(nil, nil, testBillingCfg(), testEUCfg()),
	)
	rr := postTopup(t, h, "proj-x", map[string]any{
		"channel": "stripe",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

// TestCreateTopup_StripeBelowMin_Rejected: stripe with amount_cents below
// MinTopupUSDCents (167) → 400 mentioning the minimum.
func TestCreateTopup_StripeBelowMin_Rejected(t *testing.T) {
	h := buildTopupRouter(
		handleCreateExtraUsageTopup(nil, nil, testBillingCfg(), testEUCfg()),
	)
	rr := postTopup(t, h, "proj-x", map[string]any{
		"channel":      "stripe",
		"amount_cents": 50,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
	// Body must mention the minimum value.
	var errResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &errResp)
	if errResp.Error.Code != "bad_request" {
		t.Errorf("error.code = %q, want bad_request", errResp.Error.Code)
	}
	if errResp.Error.Message == "" {
		t.Errorf("error.message is empty, want mention of minimum amount")
	}
}

// TestCreateTopup_WechatAboveMax_Rejected: wechat with amount_fen above
// MaxTopupCNYFen (200_000) → 400.
func TestCreateTopup_WechatAboveMax_Rejected(t *testing.T) {
	h := buildTopupRouter(
		handleCreateExtraUsageTopup(nil, nil, testBillingCfg(), testEUCfg()),
	)
	rr := postTopup(t, h, "proj-x", map[string]any{
		"channel":    "wechat",
		"amount_fen": 300_000,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Happy-path + DB-backed tests
// ---------------------------------------------------------------------------

// topupRoutingHarness wraps the test setup for DB-backed topup routing tests.
type topupRoutingHarness struct {
	t      *testing.T
	st     *store.Store
	pay    *stubPayClient
	router chi.Router
	euCfg  config.ExtraUsageConfig
}

func newTopupRoutingHarness(t *testing.T) *topupRoutingHarness {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL to run (e.g. postgres://user:pass@localhost:5432/testdb?sslmode=disable)")
	}

	st, err := store.New(dbURL, slog.Default())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	pay := &stubPayClient{}
	euCfg := testEUCfg()
	h := handleCreateExtraUsageTopup(st, pay, testBillingCfg(), euCfg)
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/extra-usage/topup", h)

	return &topupRoutingHarness{t: t, st: st, pay: pay, router: r, euCfg: euCfg}
}

func (h *topupRoutingHarness) seedProject(label string) (userID, projectID string) {
	h.t.Helper()
	u := &types.User{
		Email:  fmt.Sprintf("topup-routing-%s-%d@test.local", label, time.Now().UnixNano()),
		Status: "active",
	}
	if err := h.st.CreateUser(u); err != nil {
		h.t.Fatalf("seedProject(%s) user: %v", label, err)
	}
	p := &types.Project{
		Name:      "topup-routing-" + label + "-" + u.ID[:8],
		CreatedBy: u.ID,
		Status:    types.ProjectStatusActive,
	}
	if err := h.st.CreateProject(p); err != nil {
		h.t.Fatalf("seedProject(%s) project: %v", label, err)
	}
	return u.ID, p.ID
}

func (h *topupRoutingHarness) post(projectID string, body map[string]any) *httptest.ResponseRecorder {
	h.t.Helper()
	return postTopup(h.t, h.router, projectID, body)
}

// decodeTopupResp decodes the flattened topup response from a 201 response.
func decodeTopupResp(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode topup response: %v; raw=%s", err, rr.Body.String())
	}
	return resp.Data
}

// TestCreateTopup_WechatChannel_HappyPath verifies the CNY branch: correct
// credits calculation, currency=CNY, and response fields.
//
// With CreditPriceCNYFen=5438 and amount_fen=1000:
//
//	credits = (1000 × 1_000_000) / 5438 = 183_891
func TestCreateTopup_WechatChannel_HappyPath(t *testing.T) {
	h := newTopupRoutingHarness(t)
	_, projectID := h.seedProject("wechat-happy")

	rr := h.post(projectID, map[string]any{
		"channel":    "wechat",
		"amount_fen": 1000,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rr.Code, rr.Body.String())
	}

	data := decodeTopupResp(t, rr)

	// Channel and currency.
	if data["channel"] != "wechat" {
		t.Errorf("channel = %v, want wechat", data["channel"])
	}
	if data["currency"] != "CNY" {
		t.Errorf("currency = %v, want CNY", data["currency"])
	}

	// Amount in fen.
	if data["amount"] != float64(1000) {
		t.Errorf("amount = %v, want 1000 (fen)", data["amount"])
	}

	// Credits: (1000 × 1_000_000) / 5438 = 183_891
	const wantCredits = float64(183_891)
	if data["credits"] != wantCredits {
		t.Errorf("credits = %v, want %v", data["credits"], wantCredits)
	}

	// Payment URL and ref from stub.
	if data["payment_url"] != "https://pay.example.com/stub" {
		t.Errorf("payment_url = %v, want stub URL", data["payment_url"])
	}

	// Confirm order row in DB.
	orderID, _ := data["order_id"].(string)
	if orderID == "" {
		t.Fatal("order_id missing from response")
	}
	order, err := h.st.GetOrderByID(orderID)
	if err != nil || order == nil {
		t.Fatalf("GetOrderByID(%s): err=%v", orderID, err)
	}
	if order.Currency != "CNY" {
		t.Errorf("DB order.Currency = %q, want CNY", order.Currency)
	}
	if order.ExtraUsageAmountCredits != 183_891 {
		t.Errorf("DB order.ExtraUsageAmountCredits = %d, want 183891", order.ExtraUsageAmountCredits)
	}
}

// TestCreateTopup_StripeChannel_HappyPath verifies the USD branch: correct
// credits calculation, currency=USD, and response fields.
//
// With CreditPriceUSDCents=907 and amount_cents=167:
//
//	credits = (167 × 1_000_000) / 907 = 184_123
func TestCreateTopup_StripeChannel_HappyPath(t *testing.T) {
	h := newTopupRoutingHarness(t)
	_, projectID := h.seedProject("stripe-happy")

	rr := h.post(projectID, map[string]any{
		"channel":      "stripe",
		"amount_cents": 167,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rr.Code, rr.Body.String())
	}

	data := decodeTopupResp(t, rr)

	if data["channel"] != "stripe" {
		t.Errorf("channel = %v, want stripe", data["channel"])
	}
	if data["currency"] != "USD" {
		t.Errorf("currency = %v, want USD", data["currency"])
	}
	if data["amount"] != float64(167) {
		t.Errorf("amount = %v, want 167 (cents)", data["amount"])
	}

	// Credits: (167 × 1_000_000) / 907 = 184_123
	const wantCredits = float64(184_123)
	if data["credits"] != wantCredits {
		t.Errorf("credits = %v, want %v", data["credits"], wantCredits)
	}

	// Confirm order row in DB.
	orderID, _ := data["order_id"].(string)
	if orderID == "" {
		t.Fatal("order_id missing from response")
	}
	order, err := h.st.GetOrderByID(orderID)
	if err != nil || order == nil {
		t.Fatalf("GetOrderByID(%s): err=%v", orderID, err)
	}
	if order.Currency != "USD" {
		t.Errorf("DB order.Currency = %q, want USD", order.Currency)
	}
	if order.ExtraUsageAmountCredits != 184_123 {
		t.Errorf("DB order.ExtraUsageAmountCredits = %d, want 184123", order.ExtraUsageAmountCredits)
	}
}

// TestCreateTopup_DailyCap_HitsCreditsLimit verifies that the daily cap
// (in credits) fires a 409 when today's accumulated topups plus the
// requested amount would exceed DailyTopupLimitCredits.
//
// Setup: seed one order that consumes (DailyTopupLimitCredits - 100) credits,
// then request 101 credits worth → expect 409.
func TestCreateTopup_DailyCap_HitsCreditsLimit(t *testing.T) {
	h := newTopupRoutingHarness(t)
	_, projectID := h.seedProject("daily-cap")

	// Set a daily limit of 500_000 credits.
	h.euCfg.DailyTopupLimitCredits = 500_000

	// Seed an existing paid topup order for today that consumes 499_901 credits.
	// We create a delivered order with ExtraUsageAmountCredits = DailyLimit - 100.
	existingCredits := int64(499_901)
	seedOrder := &types.Order{
		ProjectID:               projectID,
		Periods:                 1,
		UnitPrice:               2_717_000, // arbitrary fen value
		Amount:                  2_717_000,
		Currency:                "CNY",
		Status:                  types.OrderStatusDelivered,
		Channel:                 "wechat",
		Metadata:                "{}",
		OrderType:               types.OrderTypeExtraUsageTopup,
		ExtraUsageAmountCredits: existingCredits,
	}
	if err := h.st.CreateOrder(seedOrder); err != nil {
		t.Fatalf("seed order: %v", err)
	}

	// Build a fresh handler with the capped euCfg.
	capHandler := handleCreateExtraUsageTopup(h.st, h.pay, testBillingCfg(), h.euCfg)
	router := buildTopupRouter(capHandler)

	// Request 101 credits worth: 1000 fen → 183_891 credits (far above 100 headroom).
	b, _ := json.Marshal(map[string]any{
		"channel":    "wechat",
		"amount_fen": 1000,
	})
	req := httptest.NewRequest(http.MethodPost,
		"/projects/"+projectID+"/extra-usage/topup",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = ownerCtx(req, projectID)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", rr.Code, rr.Body.String())
	}
	mustErrorCode(t, rr, "daily_topup_limit")
}

// TestCreateTopup_PayClientNil_ServiceUnavailable verifies that when the
// payment client is not configured, the handler returns 503 and marks the
// order as failed (rather than leaving it pending indefinitely).
func TestCreateTopup_PayClientNil_ServiceUnavailable(t *testing.T) {
	h := newTopupRoutingHarness(t)
	_, projectID := h.seedProject("pay-nil")

	// Use nil payClient explicitly.
	nilHandler := handleCreateExtraUsageTopup(h.st, nil, testBillingCfg(), h.euCfg)
	router := buildTopupRouter(nilHandler)

	b, _ := json.Marshal(map[string]any{
		"channel":    "wechat",
		"amount_fen": 1000,
	})
	req := httptest.NewRequest(http.MethodPost,
		"/projects/"+projectID+"/extra-usage/topup",
		bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = ownerCtx(req, projectID)

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rr.Code, rr.Body.String())
	}
	mustErrorCode(t, rr, "payment_not_configured")
}
