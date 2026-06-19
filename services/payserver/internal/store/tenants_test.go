package store

import (
	"errors"
	"testing"

	"github.com/modelserver/modelserver/services/payserver/internal/tenant"
)

func TestCreateTenant_AndGetByID(t *testing.T) {
	st := openTestStoreWithBootstrap(t)
	hash, _ := tenant.HashSecret("secret-1")
	tt := &tenant.Tenant{
		Name:           "test-create-" + t.Name(),
		SecretHash:     hash,
		CallbackURL:    "https://x.example/cb",
		CallbackSecret: "cb-secret",
		Description:    "test",
		IsActive:       true,
	}
	if err := st.CreateTenant(tt); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, tt.ID) })

	if tt.ID == "" {
		t.Fatal("ID not populated")
	}
	got, err := st.GetTenantByID(tt.ID)
	if err != nil {
		t.Fatalf("GetTenantByID: %v", err)
	}
	if got.Name != tt.Name {
		t.Errorf("Name = %q", got.Name)
	}
	if got.CallbackURL != "https://x.example/cb" {
		t.Errorf("CallbackURL = %q", got.CallbackURL)
	}
}

func TestCreateTenant_DuplicateName(t *testing.T) {
	st := openTestStoreWithBootstrap(t)
	hash, _ := tenant.HashSecret("s")
	tt1 := &tenant.Tenant{Name: "dup-" + t.Name(), SecretHash: hash, IsActive: true}
	tt2 := &tenant.Tenant{Name: "dup-" + t.Name(), SecretHash: hash, IsActive: true}
	if err := st.CreateTenant(tt1); err != nil {
		t.Fatalf("first create: %v", err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, tt1.ID) })

	err := st.CreateTenant(tt2)
	if !errors.Is(err, ErrTenantNameTaken) {
		t.Fatalf("expected ErrTenantNameTaken, got %v", err)
	}
}

func TestGetTenantByID_NotFound(t *testing.T) {
	st := openTestStoreWithBootstrap(t)
	got, err := st.GetTenantByID("00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("GetTenantByID: %v", err)
	}
	if got != nil {
		t.Fatalf("got non-nil tenant: %+v", got)
	}
}

func TestListTenants_Pagination(t *testing.T) {
	st := openTestStoreWithBootstrap(t)
	// default tenant + at least one we seed → total >= 2
	hash, _ := tenant.HashSecret("s")
	seeded := &tenant.Tenant{Name: "list-" + t.Name(), SecretHash: hash, IsActive: true}
	if err := st.CreateTenant(seeded); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, seeded.ID) })

	rows, total, err := st.ListTenants(50, 0)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if total < 2 {
		t.Fatalf("total = %d, want >= 2", total)
	}
	if len(rows) < 2 {
		t.Fatalf("len(rows) = %d, want >= 2", len(rows))
	}
}

func TestUpdateTenant_Whitelist(t *testing.T) {
	st := openTestStoreWithBootstrap(t)
	hash, _ := tenant.HashSecret("s")
	tt := &tenant.Tenant{Name: "upd-" + t.Name(), SecretHash: hash, IsActive: true}
	if err := st.CreateTenant(tt); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, tt.ID) })

	// Allowed update
	if err := st.UpdateTenant(tt.ID, map[string]any{"callback_url": "https://new.example/cb", "is_active": false}); err != nil {
		t.Fatalf("UpdateTenant allowed: %v", err)
	}
	got, _ := st.GetTenantByID(tt.ID)
	if got.CallbackURL != "https://new.example/cb" || got.IsActive {
		t.Errorf("update didn't take: %+v", got)
	}

	// Forbidden field
	err := st.UpdateTenant(tt.ID, map[string]any{"name": "evil"})
	if err == nil {
		t.Error("expected error updating name")
	}
	err = st.UpdateTenant(tt.ID, map[string]any{"secret_hash": "evil"})
	if err == nil {
		t.Error("expected error updating secret_hash via UpdateTenant")
	}
}

func TestRotateTenantSecret(t *testing.T) {
	st := openTestStoreWithBootstrap(t)
	oldHash, _ := tenant.HashSecret("old")
	tt := &tenant.Tenant{Name: "rot-" + t.Name(), SecretHash: oldHash, IsActive: true}
	if err := st.CreateTenant(tt); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { _, _ = st.pool.Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, tt.ID) })

	newHash, _ := tenant.HashSecret("new")
	if err := st.RotateTenantSecret(tt.ID, newHash); err != nil {
		t.Fatalf("RotateTenantSecret: %v", err)
	}
	got, _ := st.GetTenantByID(tt.ID)
	if !tenant.VerifySecret(got.SecretHash, "new") {
		t.Error("new secret doesn't verify")
	}
	if tenant.VerifySecret(got.SecretHash, "old") {
		t.Error("old secret still verifies (rotation didn't replace)")
	}
}

func TestDeleteTenant_BlockedByPayments(t *testing.T) {
	st := openTestStoreWithBootstrap(t)
	hash, _ := tenant.HashSecret("s")
	tt := &tenant.Tenant{Name: "del-" + t.Name(), SecretHash: hash, IsActive: true}
	if err := st.CreateTenant(tt); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(t.Context(), `DELETE FROM payments WHERE tenant_id = $1`, tt.ID)
		_, _ = st.pool.Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, tt.ID)
	})

	// No payments → delete works
	if err := st.DeleteTenant(tt.ID); err != nil {
		t.Fatalf("delete (empty): %v", err)
	}

	// Re-create and seed a payment
	tt2 := &tenant.Tenant{Name: "del2-" + t.Name(), SecretHash: hash, IsActive: true}
	if err := st.CreateTenant(tt2); err != nil {
		t.Fatalf("seed2: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(t.Context(), `DELETE FROM payments WHERE tenant_id = $1`, tt2.ID)
		_, _ = st.pool.Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, tt2.ID)
	})
	if _, err := st.pool.Exec(t.Context(), `
		INSERT INTO payments (tenant_id, order_id, channel, amount, status)
		VALUES ($1, $2, 'wechat', 1, 'pending')`, tt2.ID, "del-test-"+t.Name()); err != nil {
		t.Fatalf("seed payment: %v", err)
	}

	err := st.DeleteTenant(tt2.ID)
	if !errors.Is(err, ErrTenantHasPayments) {
		t.Fatalf("expected ErrTenantHasPayments, got %v", err)
	}
}
