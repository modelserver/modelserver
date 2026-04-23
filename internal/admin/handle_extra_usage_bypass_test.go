package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// TestAdminSetBypass_RequiresBypassField verifies the handler rejects a
// request that omits the bypass field. This runs without a store because
// the validation fires before the store is called.
func TestAdminSetBypass_RequiresBypassField(t *testing.T) {
	// nil store is fine — we never reach it for a 400 path.
	h := handleAdminExtraUsageSetBypass(nil)

	r := chi.NewRouter()
	r.Put("/projects/{projectID}/bypass", h)

	req := httptest.NewRequest("PUT", "/projects/xyz/bypass", bytes.NewBufferString(`{}`))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestAdminSetBypass_InvalidJSON verifies a syntactically invalid body is
// also a 400.
func TestAdminSetBypass_InvalidJSON(t *testing.T) {
	h := handleAdminExtraUsageSetBypass(nil)

	r := chi.NewRouter()
	r.Put("/projects/{projectID}/bypass", h)

	req := httptest.NewRequest("PUT", "/projects/xyz/bypass", bytes.NewBufferString(`{not json`))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestAdminSetBypass_NonSuperadminForbidden verifies that a request from a
// non-superadmin user is rejected with 403 by the RequireSuperadmin
// middleware before ever reaching the handler. Reproduces the route-group
// wiring from routes.go so the test exercises the real guard.
func TestAdminSetBypass_NonSuperadminForbidden(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/admin/extra-usage", func(r chi.Router) {
		r.Use(RequireSuperadmin)
		r.Put("/projects/{projectID}/bypass", handleAdminExtraUsageSetBypass(nil))
	})

	req := httptest.NewRequest("PUT",
		"/admin/extra-usage/projects/xyz/bypass",
		bytes.NewBufferString(`{"bypass": true}`))
	// Populate a non-superadmin user into the context the same way
	// JWTAuthMiddleware does — bypassing JWT so the test focuses on the
	// superadmin guard.
	user := &types.User{ID: "u1", Email: "user@example.com", IsSuperadmin: false}
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, user))

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}
