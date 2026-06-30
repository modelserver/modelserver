package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
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

func TestHandleTransferOwnership_E2EHappyPath(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL to run")
	}
	st, err := store.New(dbURL, slog.Default())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	var ownerID, targetID, projectID string
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.Pool().Exec(ctx, q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustOne := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := st.Pool().QueryRow(ctx, q, args...).Scan(&s); err != nil {
			t.Fatalf("query %s: %v", q, err)
		}
		return s
	}
	ownerID = mustOne(`INSERT INTO users (email) VALUES ('owner-' || gen_random_uuid()::text || '@t.local') RETURNING id`)
	targetID = mustOne(`INSERT INTO users (email) VALUES ('target-' || gen_random_uuid()::text || '@t.local') RETURNING id`)
	projectID = mustOne(`INSERT INTO projects (name, created_by) VALUES ('transfer-e2e', $1) RETURNING id`, ownerID)
	mustExec(`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')`, projectID, ownerID)
	mustExec(`INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'developer')`, projectID, targetID)

	r := chi.NewRouter()
	r.Post("/projects/{projectID}/transfer-ownership", handleTransferOwnership(st))

	body, _ := json.Marshal(map[string]string{"to_user_id": targetID})
	req := httptest.NewRequest("POST", "/projects/"+projectID+"/transfer-ownership", bytes.NewReader(body))
	req = req.WithContext(callerCtx(req.Context(),
		&types.User{ID: ownerID, IsSuperadmin: false},
		&types.ProjectMember{UserID: ownerID, Role: types.RoleOwner},
	))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// Verify post-state.
	gotOwner := mustOne(`SELECT user_id FROM project_members WHERE project_id=$1 AND role='owner'`, projectID)
	if gotOwner != targetID {
		t.Errorf("new owner = %s, want %s", gotOwner, targetID)
	}
	gotOldRole := mustOne(`SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`, projectID, ownerID)
	if gotOldRole != types.RoleDeveloper {
		t.Errorf("old owner demoted to %s, want developer", gotOldRole)
	}
}
