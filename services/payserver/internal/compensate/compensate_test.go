package compensate

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/notify"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
	"github.com/modelserver/modelserver/services/payserver/internal/tenant"
)

func TestBackoffDuration(t *testing.T) {
	tests := []struct {
		retries int
		minWait time.Duration
	}{
		{0, 30 * time.Second},
		{1, 60 * time.Second},
		{2, 120 * time.Second},
		{5, 960 * time.Second},
	}
	for _, tt := range tests {
		got := backoffDuration(tt.retries)
		if got < tt.minWait {
			t.Errorf("backoffDuration(%d) = %v, want >= %v", tt.retries, got, tt.minWait)
		}
	}
}

// TestCompensateWorker tests the per-tenant callback lookup and delivery.
func TestCompensateWorker(t *testing.T) {
	// Setup: create store, tenant, and callback stub.
	st := openTestPayserverStore(t)
	cb := &stubCallback{}
	cbClient, cbURL := newStubCallbackClient(t, cb)

	tenantID := seedTenant(t, st, cbURL)

	// Create a paid payment with pending callback.
	orderID := newTestUUID(t)
	p := &store.Payment{
		TenantID: tenantID,
		OrderID:  orderID,
		Channel:  "stripe",
		Amount:   2000,
		Status:   "paid",
	}
	_, err := st.InsertOrGetPayment(p)
	if err != nil {
		t.Fatalf("insert payment: %v", err)
	}

	// Mark as paid with a timestamp.
	paidAt := time.Now()
	_, err = st.MarkPaymentPaid(orderID, "cs_test", `{}`, paidAt)
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	// Manually set callback_status to 'pending' and backdate updated_at to allow immediate retry.
	// (In real flow, IncrCallbackRetries would be called, but the store method doesn't set callback_status.)
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE payments SET callback_status = 'pending', updated_at = NOW() - INTERVAL '1 minute' WHERE order_id = $1`, orderID); err != nil {
		t.Fatalf("set callback_status: %v", err)
	}

	// Verify payment is in pending callback state.
	p, err = st.GetPaymentByOrderID(orderID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if p.Status != "paid" {
		t.Fatalf("status = %q, want paid", p.Status)
	}
	if p.CallbackStatus != "pending" {
		t.Fatalf("callback_status = %q, want pending", p.CallbackStatus)
	}

	// Create and run the worker.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := NewWorker(st, cbClient, logger)
	worker.processPending(context.Background())

	// Verify callback was called exactly once.
	if cb.calls() != 1 {
		t.Errorf("callback calls = %d, want 1", cb.calls())
	}

	// Verify callback_status is now success.
	p, err = st.GetPaymentByOrderID(orderID)
	if err != nil {
		t.Fatalf("get payment after compensate: %v", err)
	}
	if p.CallbackStatus != "success" {
		t.Errorf("callback_status = %q, want success", p.CallbackStatus)
	}
	if p.CallbackRetries != 0 {
		t.Errorf("callback_retries = %d, want 0", p.CallbackRetries)
	}
}

// TestCompensateWorker_InactiveTenant tests that inactive tenant marks callback failed.
func TestCompensateWorker_InactiveTenant(t *testing.T) {
	st := openTestPayserverStore(t)
	cb := &stubCallback{}
	cbClient, cbURL := newStubCallbackClient(t, cb)

	tenantID := seedTenant(t, st, cbURL)

	// Create a paid payment.
	orderID := newTestUUID(t)
	p := &store.Payment{
		TenantID: tenantID,
		OrderID:  orderID,
		Channel:  "stripe",
		Amount:   2000,
		Status:   "paid",
	}
	_, err := st.InsertOrGetPayment(p)
	if err != nil {
		t.Fatalf("insert payment: %v", err)
	}

	// Mark as paid.
	paidAt := time.Now()
	_, err = st.MarkPaymentPaid(orderID, "cs_test", `{}`, paidAt)
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	// Manually set callback_status to 'pending' and backdate updated_at to allow immediate retry.
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE payments SET callback_status = 'pending', updated_at = NOW() - INTERVAL '1 minute' WHERE order_id = $1`, orderID); err != nil {
		t.Fatalf("set callback_status: %v", err)
	}

	// Deactivate the tenant.
	if err := st.UpdateTenant(tenantID, map[string]any{"is_active": false}); err != nil {
		t.Fatalf("deactivate tenant: %v", err)
	}

	// Run the worker.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := NewWorker(st, cbClient, logger)
	worker.processPending(context.Background())

	// Verify callback was never called.
	if cb.calls() != 0 {
		t.Errorf("callback calls = %d, want 0", cb.calls())
	}

	// Verify callback_status is failed.
	p, err = st.GetPaymentByOrderID(orderID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if p.CallbackStatus != "failed" {
		t.Errorf("callback_status = %q, want failed", p.CallbackStatus)
	}
}

// Helpers mirror those in notify/stripe_test.go

type stubCallback struct {
	callCount atomic.Int64
}

func (s *stubCallback) calls() int {
	return int(s.callCount.Load())
}

func openTestPayserverStore(t *testing.T) *store.Store {
	t.Helper()
	dbURL := os.Getenv("PAYSERVER_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("PAYSERVER_TEST_DB_URL not set; skipping DB-dependent test")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.New(dbURL, logger, store.MigrationBootstrap{})
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Truncate for a clean slate before each test.
	if _, err := st.Pool().Exec(context.Background(), "TRUNCATE payments, tenants RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncate payments, tenants: %v", err)
	}
	return st
}

func newStubCallbackClient(t *testing.T, cb *stubCallback) (*notify.CallbackClient, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cb.callCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return notify.NewCallbackClient(5 * time.Second), srv.URL
}

func seedTenant(t *testing.T, st *store.Store, callbackURL string) string {
	t.Helper()
	hash, _ := tenant.HashSecret("test-secret")
	tt := &tenant.Tenant{
		Name:           "compensate-test-" + t.Name(),
		SecretHash:     hash,
		CallbackURL:    callbackURL,
		CallbackSecret: "test-secret",
		IsActive:       true,
	}
	if err := st.CreateTenant(tt); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return tt.ID
}

func newTestUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand uuid: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%12x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
