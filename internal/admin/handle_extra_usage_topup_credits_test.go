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
	// For amount_fen=1000: credits = (1000 × 1_000_000) / 5438 = 183_890.
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

	// Decode the returned order.
	var resp struct {
		Data types.Order `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; raw=%s", err, rr.Body.String())
	}
	order := resp.Data

	// Payment-side fields must stay in fen (CNY).
	if order.Amount != 1000 {
		t.Errorf("Amount = %d, want 1000 (fen)", order.Amount)
	}
	if order.Currency != "CNY" {
		t.Errorf("Currency = %q, want CNY", order.Currency)
	}

	// The critical invariant: ExtraUsageAmountCredits must be credits,
	// not fen. With CreditPriceCNYFen=5438 and amount_fen=1000:
	//   credits = (1000 × 1_000_000) / 5438 = 183_890
	// If the bug is present, the field will equal 1000 (fen stored as credits).
	const wantCredits int64 = 183_890
	if order.ExtraUsageAmountCredits != wantCredits {
		t.Errorf("ExtraUsageAmountCredits = %d, want %d\n"+
			"  (bug: storing fen=%d instead of credits=%d in ExtraUsageAmountCredits)",
			order.ExtraUsageAmountCredits, wantCredits, order.Amount, wantCredits)
	}
}
