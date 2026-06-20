package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/modelserver/modelserver/services/payserver/internal/store"
	"github.com/modelserver/modelserver/services/payserver/internal/tenant"
)

func openTestStoreServer(t *testing.T) *store.Store {
	t.Helper()
	dbURL := os.Getenv("PAYSERVER_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("set PAYSERVER_TEST_DB_URL")
	}
	hash, _ := tenant.HashSecret("default-test-secret")
	bootstrap := store.MigrationBootstrap{
		DefaultTenantSecretHash: hash,
		DefaultCallbackURL:      "https://test.example/webhook",
		DefaultCallbackSecret:   "test-callback-secret",
	}
	st, err := store.New(dbURL, slog.New(slog.NewTextHandler(io.Discard, nil)), bootstrap)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedTestTenant(t *testing.T, st *store.Store, secret string) *tenant.Tenant {
	t.Helper()
	hash, _ := tenant.HashSecret(secret)
	tt := &tenant.Tenant{
		Name:           "auth-test-" + t.Name(),
		SecretHash:     hash,
		CallbackURL:    "https://auth.example/cb",
		CallbackSecret: "cb",
		IsActive:       true,
	}
	if err := st.CreateTenant(tt); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(t.Context(), `DELETE FROM tenants WHERE id = $1`, tt.ID)
	})
	return tt
}

func runAuth(t *testing.T, st *store.Store, hdr string) (int, *tenant.Tenant) {
	t.Helper()
	var captured *tenant.Tenant
	mw := tenantAuthMiddleware(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = TenantFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("POST", "/payments", nil)
	if hdr != "" {
		req.Header.Set("Authorization", hdr)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req.WithContext(context.Background()))
	return w.Code, captured
}

func TestTenantAuth_MissingHeader(t *testing.T) {
	st := openTestStoreServer(t)
	code, _ := runAuth(t, st, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", code)
	}
}

func TestTenantAuth_MalformedToken(t *testing.T) {
	st := openTestStoreServer(t)
	code, _ := runAuth(t, st, "Bearer no-colon-here")
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", code)
	}
}

func TestTenantAuth_UnknownTenantID(t *testing.T) {
	st := openTestStoreServer(t)
	code, _ := runAuth(t, st, "Bearer 00000000-0000-0000-0000-000000000000:any-secret")
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", code)
	}
}

func TestTenantAuth_WrongSecret(t *testing.T) {
	st := openTestStoreServer(t)
	tt := seedTestTenant(t, st, "right-secret")
	code, _ := runAuth(t, st, "Bearer "+tt.ID+":wrong-secret")
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", code)
	}
}

func TestTenantAuth_InactiveTenant(t *testing.T) {
	st := openTestStoreServer(t)
	tt := seedTestTenant(t, st, "secret")
	if err := st.UpdateTenant(tt.ID, map[string]any{"is_active": false}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	code, _ := runAuth(t, st, "Bearer "+tt.ID+":secret")
	if code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", code)
	}
}

func TestTenantAuth_Success(t *testing.T) {
	st := openTestStoreServer(t)
	tt := seedTestTenant(t, st, "secret")
	code, ctxTenant := runAuth(t, st, "Bearer "+tt.ID+":secret")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if ctxTenant == nil || ctxTenant.ID != tt.ID {
		t.Fatalf("ctx tenant = %+v, want id=%s", ctxTenant, tt.ID)
	}
}

// TestTenantAuth_LowercaseBearerScheme guards the RFC 7235 case-
// insensitive scheme match. A lowercase "bearer" prefix must still
// authenticate.
func TestTenantAuth_LowercaseBearerScheme(t *testing.T) {
	st := openTestStoreServer(t)
	tt := seedTestTenant(t, st, "secret")
	code, ctxTenant := runAuth(t, st, "bearer "+tt.ID+":secret")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (lowercase bearer should work)", code)
	}
	if ctxTenant == nil || ctxTenant.ID != tt.ID {
		t.Fatalf("ctx tenant = %+v, want id=%s", ctxTenant, tt.ID)
	}
}
