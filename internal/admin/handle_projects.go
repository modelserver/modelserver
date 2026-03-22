package admin

import (
	"log"
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/ratelimit"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListProjects(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		p := parsePagination(r)

		projects, total, err := st.ListUserProjects(user.ID, p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list projects")
			return
		}
		writeList(w, projects, total, p.Page, p.Limit())
	}
}

func handleListAllProjects(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := parsePagination(r)

		projects, total, err := st.ListAllProjects(p)
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
			Description: body.Description,
			CreatedBy:   user.ID,
			Status:      types.ProjectStatusActive,
		}
		if err := st.CreateProject(project); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create project")
			return
		}

		assignFreePlan(st, project.ID)

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
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"name", "description", "settings", "billing_tags"} {
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

func handleArchiveProject(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		if err := st.UpdateProject(projectID, map[string]interface{}{"status": types.ProjectStatusArchived}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to archive project")
			return
		}
		project, _ := st.GetProjectByID(projectID)
		writeData(w, http.StatusOK, project)
	}
}

func handleUnarchiveProject(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		project, err := st.GetProjectByID(projectID)
		if err != nil || project == nil {
			writeError(w, http.StatusNotFound, "not_found", "project not found")
			return
		}
		if project.Status != types.ProjectStatusArchived {
			writeError(w, http.StatusBadRequest, "bad_request", "project is not archived")
			return
		}
		if err := st.UpdateProject(projectID, map[string]interface{}{"status": types.ProjectStatusActive}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to unarchive project")
			return
		}
		project, _ = st.GetProjectByID(projectID)
		writeData(w, http.StatusOK, project)
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
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		var body struct {
			Email          string   `json:"email"`
			Role           string   `json:"role"`
			CreditQuotaPct *float64 `json:"credit_quota_percent"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Email == "" || body.Role == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "email and role are required")
			return
		}

		// Resolve email to user ID. Generic error to avoid leaking registration status.
		user, err := st.GetUserByEmail(body.Email)
		if err != nil || user == nil {
			writeError(w, http.StatusBadRequest, "bad_request", "failed to add member")
			return
		}
		userID := user.ID

		// Validate quota if provided.
		if body.CreditQuotaPct != nil {
			if *body.CreditQuotaPct < 0 || *body.CreditQuotaPct > 100 {
				writeError(w, http.StatusBadRequest, "bad_request", "credit_quota_percent must be between 0 and 100")
				return
			}
			if body.Role == types.RoleOwner {
				writeError(w, http.StatusBadRequest, "bad_request", "cannot set quota on an owner")
				return
			}
		}

		if err := st.AddProjectMember(projectID, userID, body.Role); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to add member")
			return
		}

		// Set quota if provided.
		if body.CreditQuotaPct != nil {
			quotaPtr := &body.CreditQuotaPct
			if err := st.UpdateProjectMember(projectID, userID, nil, quotaPtr); err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to set member quota")
				return
			}
		}

		w.WriteHeader(http.StatusCreated)
	}
}

func handleUpdateMember(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")

		var body struct {
			Role           *string  `json:"role"`
			CreditQuotaPct *float64 `json:"credit_quota_percent"`
			ClearQuota     bool     `json:"clear_quota"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		// At least one field must be provided.
		if body.Role == nil && body.CreditQuotaPct == nil && !body.ClearQuota {
			writeError(w, http.StatusBadRequest, "bad_request", "at least one of role, credit_quota_percent, or clear_quota must be provided")
			return
		}

		// Validate credit_quota_percent range.
		if body.CreditQuotaPct != nil && (*body.CreditQuotaPct < 0 || *body.CreditQuotaPct > 100) {
			writeError(w, http.StatusBadRequest, "bad_request", "credit_quota_percent must be between 0 and 100")
			return
		}

		caller := UserFromContext(r.Context())
		callerMember := MemberFromContext(r.Context())

		// Cannot set quota on yourself.
		if (body.CreditQuotaPct != nil || body.ClearQuota) && userID == caller.ID {
			writeError(w, http.StatusBadRequest, "bad_request", "cannot set quota on yourself")
			return
		}

		// Load target member to check their role.
		targetMember, err := st.GetProjectMember(projectID, userID)
		if err != nil || targetMember == nil {
			writeError(w, http.StatusNotFound, "not_found", "member not found")
			return
		}

		// Cannot set quota on an owner.
		if (body.CreditQuotaPct != nil || body.ClearQuota) && targetMember.Role == types.RoleOwner {
			writeError(w, http.StatusForbidden, "forbidden", "cannot set quota on an owner")
			return
		}

		// Maintainers cannot set quota on other maintainers.
		if callerMember != nil && callerMember.Role == types.RoleMaintainer &&
			(body.CreditQuotaPct != nil || body.ClearQuota) &&
			targetMember.Role == types.RoleMaintainer {
			writeError(w, http.StatusForbidden, "forbidden", "maintainers cannot set quota on other maintainers")
			return
		}

		// Build quota pointer argument (**float64).
		// nil = don't change; &nilPtr = set NULL; &valuePtr = set value.
		var quotaArg **float64
		if body.ClearQuota {
			var nilPtr *float64
			quotaArg = &nilPtr
		} else if body.CreditQuotaPct != nil {
			// body.CreditQuotaPct is *float64; take its address to get **float64.
			quotaArg = &body.CreditQuotaPct
		}

		// If promoting to owner, quota is auto-cleared in the store layer.
		if err := st.UpdateProjectMember(projectID, userID, body.Role, quotaArg); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update member")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleRemoveMember(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")
		if err := st.RemoveProjectMember(projectID, userID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to remove member")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// quotaWindowStatus holds per-window quota usage for a user.
type quotaWindowStatus struct {
	Window         string  `json:"window"`
	WindowType     string  `json:"window_type"`
	Limit          int64   `json:"limit"`
	Used           float64 `json:"used"`
	Percentage     float64 `json:"percentage"`
	ResetsAt       string  `json:"resets_at,omitempty"`
}

// serveQuotaUsage is the shared core logic for quota usage responses.
func serveQuotaUsage(st *store.Store, w http.ResponseWriter, projectID, userID string) {
	member, err := st.GetProjectMember(projectID, userID)
	if err != nil || member == nil {
		writeError(w, http.StatusNotFound, "not_found", "member not found")
		return
	}

	// Determine effective quota percent (nil → 100%).
	effectiveQuotaPct := 100.0
	if member.CreditQuotaPct != nil {
		effectiveQuotaPct = *member.CreditQuotaPct
	}

	activeSub, err := st.GetActiveSubscription(projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to get active subscription")
		return
	}

	type response struct {
		UserID         string              `json:"user_id"`
		CreditQuotaPct *float64            `json:"credit_quota_percent"`
		Windows        []quotaWindowStatus `json:"windows"`
	}

	resp := response{
		UserID:         userID,
		CreditQuotaPct: member.CreditQuotaPct,
		Windows:        []quotaWindowStatus{},
	}

	if activeSub == nil {
		writeData(w, http.StatusOK, resp)
		return
	}

	plan, err := st.GetPlanBySlug(activeSub.PlanName)
	if err != nil || plan == nil {
		writeData(w, http.StatusOK, resp)
		return
	}

	policy := plan.ToPolicy(projectID, &activeSub.StartsAt)

	for _, rule := range policy.CreditRules {
		if rule.EffectiveScope() != types.CreditScopeProject {
			continue
		}

		userLimit := int64(math.Round(float64(rule.MaxCredits) * (effectiveQuotaPct / 100.0)))
		windowStart := ratelimit.WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)

		used, err := st.SumCreditsInWindowByUser(projectID, userID, windowStart)
		if err != nil {
			used = 0
		}

		var percentage float64
		if userLimit == 0 {
			// quota is 0% → already at limit
			percentage = 100
		} else {
			percentage = (used / float64(userLimit)) * 100
			if percentage > 100 {
				percentage = 100
			}
		}

		// Round to 2 decimal places.
		percentage = math.Round(percentage*100) / 100

		ws := quotaWindowStatus{
			Window:     rule.Window,
			WindowType: rule.WindowType,
			Limit:      userLimit,
			Used:       used,
			Percentage: percentage,
		}

		if rule.WindowType == types.WindowTypeCalendar || rule.WindowType == types.WindowTypeFixed {
			resetDur := ratelimit.WindowResetDuration(rule.Window, rule.WindowType, rule.AnchorTime)
			ws.ResetsAt = time.Now().UTC().Add(resetDur).Format(time.RFC3339)
		}

		resp.Windows = append(resp.Windows, ws)
	}

	writeData(w, http.StatusOK, resp)
}

func handleQuotaUsage(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")
		caller := UserFromContext(r.Context())
		callerMember := MemberFromContext(r.Context())

		// Allow access if: caller is the target user, owner, maintainer, or superadmin.
		isSelf := caller.ID == userID
		isPrivileged := caller.IsSuperadmin ||
			(callerMember != nil && (callerMember.Role == types.RoleOwner || callerMember.Role == types.RoleMaintainer))

		if !isSelf && !isPrivileged {
			writeError(w, http.StatusForbidden, "forbidden", "access denied")
			return
		}

		serveQuotaUsage(st, w, projectID, userID)
	}
}

func handleMyQuota(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		caller := UserFromContext(r.Context())
		serveQuotaUsage(st, w, projectID, caller.ID)
	}
}

// assignFreePlan attaches a perpetual free-tier subscription to a project.
func assignFreePlan(st *store.Store, projectID string) {
	freePlan, err := st.GetPlanBySlug("free")
	if err != nil || freePlan == nil || !freePlan.IsActive {
		return
	}
	now := time.Now()
	expiresAt := now.AddDate(100, 0, 0)
	if _, err := st.CreateSubscriptionFromPlan(projectID, freePlan, now, expiresAt); err != nil {
		log.Printf("WARN: failed to assign free plan to project %s: %v", projectID, err)
	}
}
