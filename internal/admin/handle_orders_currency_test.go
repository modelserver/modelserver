package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/billing"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
	"log/slog"
)

// These tests exercise handleCreateOrder against a real test-DB store and a
// stub payment client. Set TEST_DATABASE_URL to run; without it the tests are
// skipped (consistent with store/*_db_test.go).

// ---------------------------------------------------------------------------
// Stub PaymentClient
// ---------------------------------------------------------------------------

type stubPayClient struct {
	captured *billing.PaymentRequest
}

func (s *stubPayClient) CreatePayment(_ context.Context, req billing.PaymentRequest) (*billing.PaymentResponse, error) {
	s.captured = &req
	return &billing.PaymentResponse{
		PaymentRef: "ref-stub",
		PaymentURL: "https://pay.example.com/stub",
		Status:     "pending",
	}, nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type ordersHarness struct {
	t      *testing.T
	st     *store.Store
	pay    *stubPayClient
	router chi.Router
}

func newOrdersHarness(t *testing.T) *ordersHarness {
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
	billingCfg := config.BillingConfig{
		NotifyURL: "https://example.com/notify",
		ReturnURL: "https://example.com/return",
	}

	h := handleCreateOrder(st, pay, billingCfg)
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/orders", h)

	return &ordersHarness{t: t, st: st, pay: pay, router: r}
}

// seedProject creates a user + project and returns (userID, projectID).
func (h *ordersHarness) seedProject(label string) (string, string) {
	h.t.Helper()
	u := &types.User{
		Email:  fmt.Sprintf("orders-test-%s-%d@test.local", label, time.Now().UnixNano()),
		Status: "active",
	}
	if err := h.st.CreateUser(u); err != nil {
		h.t.Fatalf("seedProject(%s) user: %v", label, err)
	}
	p := &types.Project{
		Name:      label + "-" + u.ID[:8],
		CreatedBy: u.ID,
		Status:    types.ProjectStatusActive,
	}
	if err := h.st.CreateProject(p); err != nil {
		h.t.Fatalf("seedProject(%s) project: %v", label, err)
	}
	return u.ID, p.ID
}

// seedFreeSubscription creates a project+user pair with a free active
// subscription. Returns the project UUID.
func (h *ordersHarness) seedFreeSubscription(label string) string {
	h.t.Helper()
	_, projectID := h.seedProject(label)

	freePlan := h.mustGetPlan("free")
	now := time.Now()
	_, err := h.st.CreateSubscriptionFromPlan(projectID, freePlan, now, now.AddDate(100, 0, 0))
	if err != nil {
		h.t.Fatalf("seedFreeSubscription(%s): %v", label, err)
	}
	return projectID
}

// seedPaidSubscription creates a project with a paid subscription in the
// given currency. It inserts an order in "paid" status to lock the currency.
func (h *ordersHarness) seedPaidSubscription(label, planSlug, currency string) string {
	h.t.Helper()
	_, projectID := h.seedProject(label)

	plan := h.mustGetPlan(planSlug)
	now := time.Now()
	sub, err := h.st.CreateSubscriptionFromPlan(projectID, plan, now, now.AddDate(0, 1, 0))
	if err != nil {
		h.t.Fatalf("seedPaidSubscription(%s) subscription: %v", label, err)
	}

	// Insert a "paid" order to lock the currency.
	channel := "wechat"
	unitPrice := plan.PriceCNYFen
	if currency == "USD" {
		channel = "stripe"
		unitPrice = plan.PriceUSDCents
	}
	order := &types.Order{
		ProjectID:              projectID,
		PlanID:                 plan.ID,
		Periods:                1,
		UnitPrice:              unitPrice,
		Amount:                 unitPrice,
		Currency:               currency,
		Status:                 "paid",
		Channel:                channel,
		ExistingSubscriptionID: sub.ID,
		Metadata:               "{}",
	}
	if err := h.st.CreateOrder(order); err != nil {
		h.t.Fatalf("seedPaidSubscription(%s) order: %v", label, err)
	}
	return projectID
}

// zeroPlanUSDPrice sets price_usd_cents to 0 for the given plan slug to
// simulate an ops oversight — ChannelPricing will return ok=false for stripe.
// Restores the original value via t.Cleanup to avoid polluting other tests.
func (h *ordersHarness) zeroPlanUSDPrice(planSlug string) {
	h.t.Helper()
	plan := h.mustGetPlan(planSlug)
	original := plan.PriceUSDCents
	if err := h.st.UpdatePlan(plan.ID, map[string]interface{}{
		"price_usd_cents": int64(0),
	}); err != nil {
		h.t.Fatalf("zeroPlanUSDPrice(%s): %v", planSlug, err)
	}
	h.t.Cleanup(func() {
		_ = h.st.UpdatePlan(plan.ID, map[string]interface{}{
			"price_usd_cents": original,
		})
	})
}

func (h *ordersHarness) mustGetPlan(slug string) *types.Plan {
	h.t.Helper()
	p, err := h.st.GetPlanBySlug(slug)
	if err != nil || p == nil {
		h.t.Fatalf("mustGetPlan(%q): err=%v p=%v", slug, err, p)
	}
	return p
}

func (h *ordersHarness) post(projectID string, body map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/projects/"+projectID+"/orders", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, r)
	return w
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, want, w.Body.String())
	}
}

func mustField(t *testing.T, w *httptest.ResponseRecorder, key, want string) {
	t.Helper()
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v; raw=%s", err, w.Body.String())
	}
	got, ok := resp.Data[key]
	if !ok {
		t.Fatalf("field %q missing in data; body=%s", key, w.Body.String())
	}
	if got != want {
		t.Fatalf("field %q = %v, want %q", key, got, want)
	}
}

func mustErrorCode(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body: %v; raw=%s", err, w.Body.String())
	}
	if resp.Error.Code != want {
		t.Fatalf("error.code = %q, want %q; body=%s", resp.Error.Code, want, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Test scenarios
// ---------------------------------------------------------------------------

// TestCreateOrder_FreeFirstBuyCNY: free subscription, wechat channel → CNY order.
func TestCreateOrder_FreeFirstBuyCNY(t *testing.T) {
	h := newOrdersHarness(t)
	projectID := h.seedFreeSubscription("proj-1")
	resp := h.post(projectID, map[string]any{
		"plan_slug": "pro", "periods": 1, "channel": "wechat",
	})
	mustStatus(t, resp, http.StatusCreated)
	mustField(t, resp, "currency", "CNY")
}

// TestCreateOrder_FreeFirstBuyUSD: free subscription, stripe channel → USD order.
func TestCreateOrder_FreeFirstBuyUSD(t *testing.T) {
	h := newOrdersHarness(t)
	projectID := h.seedFreeSubscription("proj-2")
	resp := h.post(projectID, map[string]any{
		"plan_slug": "pro", "periods": 1, "channel": "stripe",
	})
	mustStatus(t, resp, http.StatusCreated)
	mustField(t, resp, "currency", "USD")
}

// TestCreateOrder_CNYLocked_USDRejected: CNY-locked project, stripe upgrade → 409 currency_mismatch.
func TestCreateOrder_CNYLocked_USDRejected(t *testing.T) {
	h := newOrdersHarness(t)
	projectID := h.seedPaidSubscription("proj-3", "pro", "CNY")
	resp := h.post(projectID, map[string]any{
		"plan_slug": "max_5x", "periods": 1, "channel": "stripe",
	})
	mustStatus(t, resp, http.StatusConflict)
	mustErrorCode(t, resp, "currency_mismatch")
}

// TestCreateOrder_USDLocked_CNYRejected: USD-locked project, alipay upgrade → 409 currency_mismatch.
func TestCreateOrder_USDLocked_CNYRejected(t *testing.T) {
	h := newOrdersHarness(t)
	projectID := h.seedPaidSubscription("proj-4", "pro", "USD")
	resp := h.post(projectID, map[string]any{
		"plan_slug": "max_5x", "periods": 1, "channel": "alipay",
	})
	mustStatus(t, resp, http.StatusConflict)
	mustErrorCode(t, resp, "currency_mismatch")
}

// TestCreateOrder_CNYLocked_CNYRenewalAllowed: CNY-locked project, same-plan wechat → 201 CNY.
func TestCreateOrder_CNYLocked_CNYRenewalAllowed(t *testing.T) {
	h := newOrdersHarness(t)
	projectID := h.seedPaidSubscription("proj-5", "pro", "CNY")
	resp := h.post(projectID, map[string]any{
		"plan_slug": "pro", "periods": 1, "channel": "wechat",
	})
	mustStatus(t, resp, http.StatusCreated)
	mustField(t, resp, "currency", "CNY")
}

// TestCreateOrder_PlanMissingUSDPrice_BadRequest: plan with price_usd_cents=0 → 400 bad_request.
func TestCreateOrder_PlanMissingUSDPrice_BadRequest(t *testing.T) {
	h := newOrdersHarness(t)
	projectID := h.seedFreeSubscription("proj-6")
	h.zeroPlanUSDPrice("max_5x")
	resp := h.post(projectID, map[string]any{
		"plan_slug": "max_5x", "periods": 1, "channel": "stripe",
	})
	mustStatus(t, resp, http.StatusBadRequest)
}
