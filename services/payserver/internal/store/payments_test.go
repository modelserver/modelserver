package store

import (
	"testing"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/tenant"
)

// TestMarkPaymentPaid_TenantMismatch_NoOp confirms the tenant guard:
// passing the wrong tenant ID for a real order_id must leave the row
// untouched and return updated=false. The guard is load-bearing — without
// it, a cross-tenant CAS-by-order_id could mark a sibling tenant's row
// paid if order_ids ever collided (today blocked by UNIQUE constraint,
// tomorrow not guaranteed).
func TestMarkPaymentPaid_TenantMismatch_NoOp(t *testing.T) {
	st := openTestStoreWithBootstrap(t)
	hash, _ := tenant.HashSecret("s")

	tA := &tenant.Tenant{Name: "tenant-a-" + t.Name(), SecretHash: hash, IsActive: true}
	tB := &tenant.Tenant{Name: "tenant-b-" + t.Name(), SecretHash: hash, IsActive: true}
	if err := st.CreateTenant(tA); err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, tA.ID) })
	if err := st.CreateTenant(tB); err != nil {
		t.Fatalf("create tenant b: %v", err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, tB.ID) })

	// Seed a pending payment owned by tenant B.
	orderID := "order-" + t.Name()
	p := &Payment{
		TenantID: tB.ID,
		OrderID:  orderID,
		Channel:  "stripe",
		Amount:   1000,
		Status:   "pending",
	}
	if _, err := st.InsertOrGetPayment(p); err != nil {
		t.Fatalf("insert payment: %v", err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(t.Context(), `DELETE FROM payments WHERE order_id = $1`, orderID) })

	// Tenant A tries to mark tenant B's order as paid.
	updated, err := st.MarkPaymentPaid(tA.ID, orderID, "trade-x", `{}`, time.Now())
	if err != nil {
		t.Fatalf("MarkPaymentPaid: %v", err)
	}
	if updated {
		t.Fatal("expected updated=false on tenant mismatch; tenant A should not be able to mutate tenant B's payment")
	}

	// Confirm tenant B's payment is still pending.
	got, err := st.GetPaymentByOrderID(orderID)
	if err != nil {
		t.Fatalf("GetPaymentByOrderID: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("status = %q, want pending (the row was mutated)", got.Status)
	}

	// Sanity: the correct tenant CAN mark it paid.
	updated, err = st.MarkPaymentPaid(tB.ID, orderID, "trade-x", `{}`, time.Now())
	if err != nil {
		t.Fatalf("MarkPaymentPaid (correct tenant): %v", err)
	}
	if !updated {
		t.Fatal("expected updated=true when tenant ID matches")
	}
}

// TestMarkCallback_TenantMismatch_NoRowChanged proves the same guard
// works for the callback-status helpers.
func TestMarkCallback_TenantMismatch_NoRowChanged(t *testing.T) {
	st := openTestStoreWithBootstrap(t)
	hash, _ := tenant.HashSecret("s")

	tA := &tenant.Tenant{Name: "cba-" + t.Name(), SecretHash: hash, IsActive: true}
	tB := &tenant.Tenant{Name: "cbb-" + t.Name(), SecretHash: hash, IsActive: true}
	for _, tt := range []*tenant.Tenant{tA, tB} {
		if err := st.CreateTenant(tt); err != nil {
			t.Fatalf("create: %v", err)
		}
		id := tt.ID
		t.Cleanup(func() { _, _ = st.pool.Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, id) })
	}

	orderID := "order-cb-" + t.Name()
	if _, err := st.pool.Exec(t.Context(), `
		INSERT INTO payments (tenant_id, order_id, channel, amount, status, callback_status, callback_retries)
		VALUES ($1, $2, 'stripe', 100, 'paid', 'pending', 0)`, tB.ID, orderID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(t.Context(), `DELETE FROM payments WHERE order_id = $1`, orderID) })

	// Tenant A tries to mark tenant B's callback as success — no-op.
	if err := st.MarkCallbackSuccess(tA.ID, orderID); err != nil {
		t.Fatalf("MarkCallbackSuccess: %v", err)
	}
	got, _ := st.GetPaymentByOrderID(orderID)
	if got.CallbackStatus != "pending" {
		t.Errorf("after wrong-tenant mark, status = %q, want pending (untouched)", got.CallbackStatus)
	}

	// Same for IncrCallbackRetries.
	if err := st.IncrCallbackRetries(tA.ID, orderID); err != nil {
		t.Fatalf("IncrCallbackRetries: %v", err)
	}
	got, _ = st.GetPaymentByOrderID(orderID)
	if got.CallbackRetries != 0 {
		t.Errorf("retries = %d, want 0 (untouched)", got.CallbackRetries)
	}
}
