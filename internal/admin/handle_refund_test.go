package admin

// handle_refund_test.go — integration tests for handleBillingRefundWebhook
// and Store.RefundExtraUsageTopup. All tests require TEST_DATABASE_URL and
// are gated via t.Skip when it is unset, so `go test ./...` stays green in
// environments without a database.

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

	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// refundHarness holds the test store and a seeded project.
type refundHarness struct {
	t   *testing.T
	st  *store.Store
	h   http.HandlerFunc
}

func newRefundHarness(t *testing.T) *refundHarness {
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

	return &refundHarness{
		t:  t,
		st: st,
		h:  handleBillingRefundWebhook(st, slog.Default()),
	}
}

// seedProject creates a user + project and returns the projectID.
func (rh *refundHarness) seedProject(label string) string {
	rh.t.Helper()
	ctx := context.Background()
	var userID, projectID string
	if err := rh.st.Pool().QueryRow(ctx,
		`INSERT INTO users (email) VALUES ('refund-'||$1||'-'||gen_random_uuid()::text||'@test.local') RETURNING id`,
		label,
	).Scan(&userID); err != nil {
		rh.t.Fatalf("seed user (%s): %v", label, err)
	}
	if err := rh.st.Pool().QueryRow(ctx,
		`INSERT INTO projects (name, created_by) VALUES ($1, $2) RETURNING id`,
		"refund-"+label+"-"+userID[:8], userID,
	).Scan(&projectID); err != nil {
		rh.t.Fatalf("seed project (%s): %v", label, err)
	}
	return projectID
}

// seedDeliveredTopup creates an extra_usage_topup order, delivers it (applies
// the ledger row and sets balance_credits), and returns the order.
func (rh *refundHarness) seedDeliveredTopup(projectID string, credits int64) *types.Order {
	rh.t.Helper()
	order := &types.Order{
		ProjectID:               projectID,
		Periods:                 1,
		UnitPrice:               1000,
		Amount:                  1000,
		Currency:                "CNY",
		Status:                  types.OrderStatusPaid,
		Channel:                 "wechat",
		Metadata:                "{}",
		OrderType:               types.OrderTypeExtraUsageTopup,
		ExtraUsageAmountCredits: credits,
	}
	if err := rh.st.CreateOrder(order); err != nil {
		rh.t.Fatalf("create order: %v", err)
	}
	if _, err := deliverExtraUsageTopupOrder(rh.st, order); err != nil {
		rh.t.Fatalf("deliver order: %v", err)
	}
	return order
}

// postRefund fires POST /billing/webhook/refund with the given order_id.
// The handler is called directly (no HMAC middleware — tests own the handler).
func (rh *refundHarness) postRefund(orderID string) *httptest.ResponseRecorder {
	rh.t.Helper()
	body, _ := json.Marshal(map[string]any{
		"order_id": orderID,
		"amount":   1000,
		"currency": "CNY",
	})
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook/refund", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	rh.h.ServeHTTP(rr, req)
	return rr
}

// decodeRefundData extracts data.new_balance_credits from a 200 OK response.
func decodeRefundData(t *testing.T, rr *httptest.ResponseRecorder) int64 {
	t.Helper()
	var resp struct {
		Data struct {
			NewBalanceCredits json.Number `json:"new_balance_credits"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode refund response: %v; raw=%s", err, rr.Body.String())
	}
	v, err := resp.Data.NewBalanceCredits.Int64()
	if err != nil {
		t.Fatalf("parse new_balance_credits: %v", err)
	}
	return v
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRefund_HappyPath seeds a delivered topup of 10_000 credits and verifies
// that the refund webhook: returns 200, inserts a 'refund' ledger row with
// amount_credits=-10_000, and sets balance_credits=0.
func TestRefund_HappyPath(t *testing.T) {
	rh := newRefundHarness(t)
	projectID := rh.seedProject("happy")

	const credits int64 = 10_000
	order := rh.seedDeliveredTopup(projectID, credits)

	rr := rh.postRefund(order.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}

	newBal := decodeRefundData(t, rr)
	if newBal != 0 {
		t.Errorf("new_balance_credits = %d, want 0", newBal)
	}

	// Verify the ledger row.
	ctx := context.Background()
	var txType string
	var txAmount, txBalAfter int64
	err := rh.st.Pool().QueryRow(ctx, `
		SELECT type, amount_credits, balance_after_credits
		FROM extra_usage_transactions
		WHERE order_id = $1 AND type = 'refund'`, order.ID,
	).Scan(&txType, &txAmount, &txBalAfter)
	if err != nil {
		t.Fatalf("fetch refund ledger row: %v", err)
	}
	if txType != "refund" {
		t.Errorf("ledger type = %q, want refund", txType)
	}
	if txAmount != -credits {
		t.Errorf("amount_credits = %d, want %d", txAmount, -credits)
	}
	if txBalAfter != 0 {
		t.Errorf("balance_after_credits = %d, want 0", txBalAfter)
	}

	// Verify the settings row.
	settings, err := rh.st.GetExtraUsageSettings(projectID)
	if err != nil || settings == nil {
		t.Fatalf("get settings: %v (nil=%v)", err, settings == nil)
	}
	if settings.BalanceCredits != 0 {
		t.Errorf("settings.BalanceCredits = %d, want 0", settings.BalanceCredits)
	}
}

// TestRefund_Idempotent calls refund twice on the same order. The first call
// applies the refund (200 with new_balance_credits) and transitions the order
// to OrderStatusRefunded. The second call hits the status-gate short-circuit
// and returns 200 with `status: "already_refunded"` without re-mutating the
// ledger. Exactly one 'refund' row must exist.
func TestRefund_Idempotent(t *testing.T) {
	rh := newRefundHarness(t)
	projectID := rh.seedProject("idempotent")

	const credits int64 = 10_000
	order := rh.seedDeliveredTopup(projectID, credits)

	// First refund — full mutation path.
	rr1 := rh.postRefund(order.ID)
	if rr1.Code != http.StatusOK {
		t.Fatalf("first refund status = %d, want 200; body = %s", rr1.Code, rr1.Body.String())
	}
	if bal1 := decodeRefundData(t, rr1); bal1 != 0 {
		t.Errorf("first refund balance = %d, want 0", bal1)
	}

	// Second refund — must hit the OrderStatusRefunded short-circuit.
	rr2 := rh.postRefund(order.ID)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second refund status = %d, want 200; body = %s", rr2.Code, rr2.Body.String())
	}
	var resp2 struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode 2nd response: %v; raw=%s", err, rr2.Body.String())
	}
	if resp2.Data.Status != "already_refunded" {
		t.Errorf("2nd-call data.status = %q, want already_refunded", resp2.Data.Status)
	}

	// Exactly one 'refund' row must exist.
	ctx := context.Background()
	var rowCount int
	if err := rh.st.Pool().QueryRow(ctx, `
		SELECT COUNT(*) FROM extra_usage_transactions
		WHERE order_id = $1 AND type = 'refund'`, order.ID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count refund rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("refund ledger row count = %d, want 1 (idempotency broken)", rowCount)
	}
}

// TestRefund_PartialAmountRejected verifies that a refund webhook whose
// amount OR currency does not match the original order is rejected with
// 422 — V1 only supports full reversal and silently applying a full
// refund for a partial event would lose money in either direction. The
// handler uses `||`, so both halves of the disjunction are exercised:
// amount-mismatch and currency-mismatch.
func TestRefund_PartialAmountRejected(t *testing.T) {
	cases := []struct {
		name     string
		amount   int64
		currency string
	}{
		{"amount_mismatch", 500, "CNY"},   // != order.Amount (1000)
		{"currency_mismatch", 1000, "USD"}, // != order.Currency ("CNY")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rh := newRefundHarness(t)
			projectID := rh.seedProject("partial-" + tc.name)
			order := rh.seedDeliveredTopup(projectID, 10_000)

			body, _ := json.Marshal(map[string]any{
				"order_id": order.ID,
				"amount":   tc.amount,
				"currency": tc.currency,
			})
			req := httptest.NewRequest(http.MethodPost, "/billing/webhook/refund", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			rh.h.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body = %s", rr.Code, rr.Body.String())
			}

			// No refund ledger row should have been inserted.
			ctx := context.Background()
			var rowCount int
			if err := rh.st.Pool().QueryRow(ctx, `
				SELECT COUNT(*) FROM extra_usage_transactions
				WHERE order_id = $1 AND type = 'refund'`, order.ID,
			).Scan(&rowCount); err != nil {
				t.Fatalf("count refund rows: %v", err)
			}
			if rowCount != 0 {
				t.Errorf("partial-refund rejection leaked a ledger row (%d rows)", rowCount)
			}
		})
	}
}

// TestRefund_NotDeliveredRejected verifies that a refund webhook for an
// order that is NOT in OrderStatusDelivered (e.g. failed, cancelled, paying)
// is rejected with 409 — there is nothing to reverse.
func TestRefund_NotDeliveredRejected(t *testing.T) {
	rh := newRefundHarness(t)
	projectID := rh.seedProject("notdelivered")

	// Create a topup order in OrderStatusFailed (never delivered to wallet).
	order := &types.Order{
		ProjectID:               projectID,
		Periods:                 1,
		UnitPrice:               1000,
		Amount:                  1000,
		Currency:                "CNY",
		Status:                  types.OrderStatusFailed,
		Channel:                 "wechat",
		Metadata:                "{}",
		OrderType:               types.OrderTypeExtraUsageTopup,
		ExtraUsageAmountCredits: 10_000,
	}
	if err := rh.st.CreateOrder(order); err != nil {
		t.Fatalf("create order: %v", err)
	}

	rr := rh.postRefund(order.ID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", rr.Code, rr.Body.String())
	}

	// No refund ledger row, no balance mutation.
	ctx := context.Background()
	var rowCount int
	if err := rh.st.Pool().QueryRow(ctx, `
		SELECT COUNT(*) FROM extra_usage_transactions
		WHERE order_id = $1`, order.ID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count tx rows: %v", err)
	}
	if rowCount != 0 {
		t.Errorf("not-delivered rejection leaked ledger rows (%d rows)", rowCount)
	}
}

// TestRefund_NotFoundOrder verifies that a refund with an unknown order_id
// returns 404.
func TestRefund_NotFoundOrder(t *testing.T) {
	rh := newRefundHarness(t)

	body, _ := json.Marshal(map[string]any{
		"order_id": fmt.Sprintf("nonexistent-%d", time.Now().UnixNano()),
		"amount":   1000,
		"currency": "CNY",
	})
	req := httptest.NewRequest(http.MethodPost, "/billing/webhook/refund", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	rh.h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rr.Code, rr.Body.String())
	}
}

// TestRefund_AllowsNegativeBalance seeds a topup of 10_000 credits then
// deducts 8_000 (leaving 2_000), and refunds the full 10_000. The balance
// must reach -8_000 (negative balance allowed under the guard design).
func TestRefund_AllowsNegativeBalance(t *testing.T) {
	rh := newRefundHarness(t)
	projectID := rh.seedProject("neg-balance")

	const topupCredits int64 = 10_000
	const deductCredits int64 = 8_000
	order := rh.seedDeliveredTopup(projectID, topupCredits)

	// Deduct 8_000 credits to leave balance at 2_000.
	_, err := rh.st.DeductExtraUsage(store.DeductExtraUsageReq{
		ProjectID:        projectID,
		AmountCredits:    deductCredits,
		Reason:           types.ExtraUsageReasonRateLimited,
		Description:      "test deduction",
		MonthWindowStart: store.MonthWindowStart(),
	})
	if err != nil {
		t.Fatalf("deduct: %v", err)
	}

	// Confirm balance is 2_000.
	settings, err := rh.st.GetExtraUsageSettings(projectID)
	if err != nil || settings == nil {
		t.Fatalf("get settings before refund: %v", err)
	}
	if settings.BalanceCredits != topupCredits-deductCredits {
		t.Fatalf("balance before refund = %d, want %d", settings.BalanceCredits, topupCredits-deductCredits)
	}

	// Refund the full topup.
	rr := rh.postRefund(order.ID)
	if rr.Code != http.StatusOK {
		t.Fatalf("refund status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}

	newBal := decodeRefundData(t, rr)
	const wantBal = -(deductCredits) // 2_000 - 10_000 = -8_000
	if newBal != wantBal {
		t.Errorf("new_balance_credits = %d, want %d (negative)", newBal, wantBal)
	}

	// Guard: subsequent extra-usage deduction must fail (balance <= 0).
	_, deductErr := rh.st.DeductExtraUsage(store.DeductExtraUsageReq{
		ProjectID:        projectID,
		AmountCredits:    1,
		Reason:           types.ExtraUsageReasonRateLimited,
		MonthWindowStart: store.MonthWindowStart(),
	})
	if deductErr == nil {
		t.Error("deduct after negative balance should fail, got nil error")
	}
}
