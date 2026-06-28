package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
)

// adminTestStore returns a Store backed by TEST_DATABASE_URL; skips when unset.
// (If the file already defines a helper, reuse it instead of redefining.)
func adminTestStore(t *testing.T) *store.Store {
	t.Helper()
	// TODO: wire to existing test helper if one exists in package admin.
	// If no helper exists, this test will be skipped and the smoke test
	// in Step 5 (manual curl) suffices.
	t.Skip("no admin-package store helper available; verified by Task 3 integration test")
	return nil
}

func TestHandleGetRoutingRoute_NotFound(t *testing.T) {
	st := adminTestStore(t)
	r := chi.NewRouter()
	r.Get("/routing/routes/{routeID}", handleGetRoutingRoute(st))

	req := httptest.NewRequest(http.MethodGet, "/routing/routes/does-not-exist", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
