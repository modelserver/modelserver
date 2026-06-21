package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/types"
)

// These two tests guard a real security gap that shipped to production:
// `handleUpdateExtraUsage` and `handleCreateExtraUsageTopup` previously
// had no role check, so any project member (including Developer/Viewer)
// could enable extra usage, raise the monthly limit, or mint a paid
// topup order in the project's billing channel without the Owner's
// authorization. The two handlers now begin with
// `if !requireRole(w, r, RoleOwner, RoleMaintainer) { return }`; these
// tests pin that behavior so it can't silently regress.

func TestUpdateExtraUsage_DeveloperForbidden(t *testing.T) {
	// Nil store is fine: the role gate fires before any store call.
	h := handleUpdateExtraUsage(nil)
	router := chi.NewRouter()
	router.Put("/projects/{projectID}/extra-usage", h)

	req := httptest.NewRequest(http.MethodPut,
		"/projects/proj-1/extra-usage",
		bytes.NewBufferString(`{"enabled": true, "monthly_limit_credits": 100000}`))
	dev := &types.ProjectMember{UserID: "u-dev", ProjectID: "proj-1", Role: types.RoleDeveloper}
	req = req.WithContext(context.WithValue(req.Context(), ctxMember, dev))

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("Developer must be 403 on PUT /extra-usage; got %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateExtraUsageTopup_DeveloperForbidden(t *testing.T) {
	// Nil payClient is fine: role gate fires before payment provider call.
	h := handleCreateExtraUsageTopup(nil, nil, config.BillingConfig{}, config.ExtraUsageConfig{})
	router := chi.NewRouter()
	router.Post("/projects/{projectID}/extra-usage/topup", h)

	req := httptest.NewRequest(http.MethodPost,
		"/projects/proj-1/extra-usage/topup",
		bytes.NewBufferString(`{"amount_fen": 1000, "channel": "wechat"}`))
	dev := &types.ProjectMember{UserID: "u-dev", ProjectID: "proj-1", Role: types.RoleDeveloper}
	req = req.WithContext(context.WithValue(req.Context(), ctxMember, dev))

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("Developer must be 403 on POST /extra-usage/topup; got %d, body=%s", rr.Code, rr.Body.String())
	}
}
