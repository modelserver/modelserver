package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
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
