package admin

// TestCreateTopup_OrderStoresCreditsNotFen pins the unit invariant:
// the order row's ExtraUsageAmountCredits field must contain the
// credits-equivalent of the user's payment, NOT the raw fen amount.
// This is the regression guard for the bug found in Task 3 review:
// the column was renamed _fen→_credits but the value being assigned
// was left as fen, causing SumDailyExtraUsageTopupCredits to sum
// wrong-unit values and handleDeliveryWebhook to credit only ~1/184
// of what the user paid.

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

func TestCreateTopup_OrderStoresCreditsNotFen(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL to run (e.g. postgres://user:pass@localhost:5432/testdb?sslmode=disable)")
	}

	st, err := store.New(dbURL, slog.Default())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Seed a user + project.
	u := &types.User{
		Email:  fmt.Sprintf("topup-credits-test-%d@test.local", time.Now().UnixNano()),
		Status: "active",
	}
	if err := st.CreateUser(u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	p := &types.Project{
		Name:      "topup-credits-" + u.ID[:8],
		CreatedBy: u.ID,
		Status:    types.ProjectStatusActive,
	}
	if err := st.CreateProject(p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// CreditPriceCNYFen=5438 matches production default.
	// For amount_fen=1000: credits = (1000 × 1_000_000) / 5438 = 183_891.
	euCfg := config.ExtraUsageConfig{
		CreditPriceCNYFen:      5438,
		MinTopupCNYFen:         1000,
		MaxTopupCNYFen:         200000,
		DailyTopupLimitCredits: 0, // no cap
	}
	billingCfg := config.BillingConfig{
		NotifyURL: "https://example.com/notify",
		ReturnURL: "https://example.com/return",
	}
	pay := &stubPayClient{}
	h := handleCreateExtraUsageTopup(st, pay, billingCfg, euCfg)

	router := chi.NewRouter()
	router.Post("/projects/{projectID}/extra-usage/topup", h)

	body, _ := json.Marshal(map[string]any{
		"amount_fen": 1000,
		"channel":    "wechat",
	})
	req := httptest.NewRequest(http.MethodPost,
		"/projects/"+p.ID+"/extra-usage/topup",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Inject a project member with Owner role (required by requireRole guard).
	member := &types.ProjectMember{
		UserID:    u.ID,
		ProjectID: p.ID,
		Role:      types.RoleOwner,
	}
	req = req.WithContext(context.WithValue(req.Context(), ctxMember, member))

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d; body=%s", rr.Code, rr.Body.String())
	}

	// Decode the returned topup response (flattened map, not the full order struct).
	var resp struct {
		Data struct {
			OrderID    string  `json:"order_id"`
			Channel    string  `json:"channel"`
			Currency   string  `json:"currency"`
			Amount     float64 `json:"amount"`
			Credits    int64   `json:"credits"`
			PaymentURL string  `json:"payment_url"`
			PaymentRef string  `json:"payment_ref"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; raw=%s", err, rr.Body.String())
	}

	// Payment-side fields must stay in fen (CNY).
	if resp.Data.Amount != 1000 {
		t.Errorf("amount = %g, want 1000 (fen)", resp.Data.Amount)
	}
	if resp.Data.Currency != "CNY" {
		t.Errorf("currency = %q, want CNY", resp.Data.Currency)
	}

	// The critical invariant: credits in the response must be credits,
	// not fen. With CreditPriceCNYFen=5438 and amount_fen=1000:
	//   credits = (1000 × 1_000_000) / 5438 = 183_891
	// If the bug is present, the field will equal 1000 (fen stored as credits).
	const wantCredits int64 = 183_891
	if resp.Data.Credits != wantCredits {
		t.Errorf("credits = %d, want %d\n"+
			"  (bug: storing fen=%g instead of credits=%d in response)",
			resp.Data.Credits, wantCredits, resp.Data.Amount, wantCredits)
	}

	// Verify the order was persisted with the correct credits value.
	if resp.Data.OrderID == "" {
		t.Fatal("order_id missing from response")
	}

	// Fetch the order from the DB to confirm ExtraUsageAmountCredits was stored correctly.
	order, err := st.GetOrderByID(resp.Data.OrderID)
	if err != nil || order == nil {
		t.Fatalf("GetOrderByID(%s): err=%v order=%v", resp.Data.OrderID, err, order)
	}
	if order.ExtraUsageAmountCredits != wantCredits {
		t.Errorf("DB order.ExtraUsageAmountCredits = %d, want %d\n"+
			"  (bug: storing fen=%d instead of credits=%d in ExtraUsageAmountCredits)",
			order.ExtraUsageAmountCredits, wantCredits, order.Amount, wantCredits)
	}
}
