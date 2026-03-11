package admin

import (
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListProjects(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		p := parsePagination(r)

		var projects []types.Project
		var total int
		var err error

		if user.IsSuperadmin {
			projects, total, err = st.ListAllProjects(p)
		} else {
			projects, total, err = st.ListUserProjects(user.ID, p)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list projects")
			return
		}
		writeList(w, projects, total, p.Page, p.Limit())
	}
}

func handleCreateProject(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		var body struct {
			Name        string `json:"name"`
			Slug        string `json:"slug"`
			Description string `json:"description"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name is required")
			return
		}
		if body.Slug == "" {
			body.Slug = slugify(body.Name)
		}

		// Check project limit.
		if !user.IsSuperadmin {
			count, _ := st.CountUserOwnedProjects(user.ID)
			if count >= user.MaxProjects {
				writeError(w, http.StatusForbidden, "forbidden", "project limit reached")
				return
			}
		}

		project := &types.Project{
			Name:        body.Name,
			Slug:        body.Slug,
			Description: body.Description,
			CreatedBy:   user.ID,
			Status:      types.ProjectStatusActive,
		}
		if err := st.CreateProject(project); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create project")
			return
		}

		// Auto-assign free plan subscription to the new project.
		if freePlan, err := st.GetPlanBySlug("free"); err == nil && freePlan != nil && freePlan.IsActive {
			now := time.Now()
			expiresAt := now.AddDate(100, 0, 0) // Perpetual free tier.
			if _, err := st.CreateSubscriptionFromPlan(project.ID, freePlan, now, expiresAt); err != nil {
				log.Printf("WARN: failed to assign free plan to project %s: %v", project.ID, err)
			}
		}

		writeData(w, http.StatusCreated, project)
	}
}

func handleGetProject(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		project, err := st.GetProjectByID(projectID)
		if err != nil || project == nil {
			writeError(w, http.StatusNotFound, "not_found", "project not found")
			return
		}
		writeData(w, http.StatusOK, project)
	}
}

func handleUpdateProject(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"name", "description", "status", "settings", "billing_tag"} {
			if v, ok := body[field]; ok {
				updates[field] = v
			}
		}
		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "no valid fields to update")
			return
		}

		if err := st.UpdateProject(projectID, updates); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update project")
			return
		}

		project, _ := st.GetProjectByID(projectID)
		writeData(w, http.StatusOK, project)
	}
}

func handleDeleteProject(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		if err := st.DeleteProject(projectID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to delete project")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Members ---

func handleListMembers(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		members, err := st.ListProjectMembers(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list members")
			return
		}
		writeData(w, http.StatusOK, members)
	}
}

func handleAddMember(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		var body struct {
			UserID string `json:"user_id"`
			Role   string `json:"role"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.UserID == "" || body.Role == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "user_id and role are required")
			return
		}
		if err := st.AddProjectMember(projectID, body.UserID, body.Role); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to add member")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

func handleUpdateMember(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")
		var body struct {
			Role string `json:"role"`
		}
		if err := decodeBody(r, &body); err != nil || body.Role == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "role is required")
			return
		}
		if err := st.UpdateProjectMemberRole(projectID, userID, body.Role); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update member role")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleRemoveMember(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")
		if err := st.RemoveProjectMember(projectID, userID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to remove member")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonAlphaNum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "project"
	}
	return s
}
