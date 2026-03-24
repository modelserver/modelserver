package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// handleListOAuthGrants returns all OAuth grants for a project.
func handleListOAuthGrants(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")

		grants, err := st.ListOAuthGrants(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list oauth grants")
			return
		}

		// Return an empty array rather than null when there are no grants.
		if grants == nil {
			grants = []types.OAuthGrant{}
		}

		writeData(w, http.StatusOK, grants)
	}
}

// handleRevokeOAuthGrant revokes an OAuth grant by ID, removing it from DB and revoking
// the corresponding Hydra consent session.
func handleRevokeOAuthGrant(st *store.Store, hydra *HydraClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleMaintainer, types.RoleOwner) {
			return
		}

		projectID := chi.URLParam(r, "projectID")
		grantID := chi.URLParam(r, "grantID")

		grant, err := st.GetOAuthGrantByID(grantID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to get oauth grant")
			return
		}
		if grant == nil {
			writeError(w, http.StatusNotFound, "not_found", "oauth grant not found")
			return
		}

		// Verify the grant belongs to the project in the URL.
		if grant.ProjectID != projectID {
			writeError(w, http.StatusForbidden, "forbidden", "grant does not belong to this project")
			return
		}

		// Revoke the Hydra consent session if Hydra is configured.
		if hydra != nil {
			if err := hydra.RevokeConsent(r.Context(), grant.UserID, grant.ClientID); err != nil {
				// Log but do not block — the DB deletion is the source of truth for us.
				_ = err
			}
		}

		if err := st.DeleteOAuthGrant(grantID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to delete oauth grant")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
