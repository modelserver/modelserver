package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// callerCtx wires the user + member values JWTAuthMiddleware would set.
func callerCtx(ctx context.Context, user *types.User, member *types.ProjectMember) context.Context {
	ctx = context.WithValue(ctx, ctxUser, user)
	if member != nil {
		ctx = context.WithValue(ctx, ctxMember, member)
	}
	return ctx
}

func TestHandleTransferOwnership_MaintainerForbidden(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/transfer-ownership", handleTransferOwnership(nil))

	body, _ := json.Marshal(map[string]string{"to_user_id": "11111111-1111-1111-1111-111111111111"})
	req := httptest.NewRequest("POST", "/projects/p1/transfer-ownership", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-maint", IsSuperadmin: false},
		&types.ProjectMember{UserID: "u-maint", Role: types.RoleMaintainer},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleTransferOwnership_InvalidJSON(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/transfer-ownership", handleTransferOwnership(nil))

	req := httptest.NewRequest("POST", "/projects/p1/transfer-ownership", bytes.NewBufferString("{not json"))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleTransferOwnership_MissingToUserID(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/transfer-ownership", handleTransferOwnership(nil))

	req := httptest.NewRequest("POST", "/projects/p1/transfer-ownership", bytes.NewBufferString(`{}`))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleAddMember_RejectsOwnerRole(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/members", handleAddMember(nil))

	body, _ := json.Marshal(map[string]string{"email": "x@y.z", "role": "owner"})
	req := httptest.NewRequest("POST", "/projects/p1/members", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"invalid_role"`)) {
		t.Errorf("body lacks invalid_role code: %s", rr.Body.String())
	}
}

func TestHandleAddMember_RejectsUnknownRole(t *testing.T) {
	r := chi.NewRouter()
	r.Post("/projects/{projectID}/members", handleAddMember(nil))

	body, _ := json.Marshal(map[string]string{"email": "x@y.z", "role": "janitor"})
	req := httptest.NewRequest("POST", "/projects/p1/members", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleUpdateMember_RejectsRoleOwner(t *testing.T) {
	r := chi.NewRouter()
	r.Put("/projects/{projectID}/members/{userID}", handleUpdateMember(nil))

	body, _ := json.Marshal(map[string]string{"role": "owner"})
	req := httptest.NewRequest("PUT", "/projects/p1/members/u-target", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"invalid_role"`)) {
		t.Errorf("body lacks invalid_role: %s", rr.Body.String())
	}
}

func TestHandleUpdateMember_RejectsUnknownRole(t *testing.T) {
	r := chi.NewRouter()
	r.Put("/projects/{projectID}/members/{userID}", handleUpdateMember(nil))

	body, _ := json.Marshal(map[string]string{"role": "janitor"})
	req := httptest.NewRequest("PUT", "/projects/p1/members/u-target", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: "u-owner"},
		&types.ProjectMember{UserID: "u-owner", Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
