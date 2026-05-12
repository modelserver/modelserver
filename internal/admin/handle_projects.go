package admin

import (
	"log"
	"math"
	"net/http"
	"strings"
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

// projectOwnerSnapshot is the minimal owner info needed to render the admin
// projects table. Avoids polluting types.User with display-only concerns.
type projectOwnerSnapshot struct {
	ID       string `json:"id"`
	Email    string `json:"email,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Picture  string `json:"picture,omitempty"`
}

// projectSubscriptionOverview is the per-project payload returned by the
// admin subscriptions-overview endpoint.
type projectSubscriptionOverview struct {
	ProjectID     string                         `json:"project_id"`
	PlanID        string                         `json:"plan_id,omitempty"`
	PlanName      string                         `json:"plan_name,omitempty"`
	DisplayName   string                         `json:"display_name,omitempty"`
	Windows       []ratelimit.CreditWindowStatus `json:"windows"`
	Owner         *projectOwnerSnapshot          `json:"owner,omitempty"`
	// PeriodCreditsK is credits consumed since the active subscription's
	// StartsAt, rounded to integer thousands. Absent when there is no
	// active subscription.
	PeriodCreditsK *int64 `json:"period_credits_k,omitempty"`
}

// handleAdminProjectsSubscriptionsOverview returns active subscription + credit
// window usage for many projects in a single response. Replaces the per-row
// N+1 the dashboard used to do via /projects/{id}/subscriptions and
// /projects/{id}/subscription/usage.
//
// Query: ?project_ids=id1,id2,...
func handleAdminProjectsSubscriptionsOverview(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimSpace(r.URL.Query().Get("project_ids"))
		if raw == "" {
			writeData(w, http.StatusOK, []projectSubscriptionOverview{})
			return
		}
		projectIDs := make([]string, 0, 16)
		for _, id := range strings.Split(raw, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				projectIDs = append(projectIDs, id)
			}
		}
		if len(projectIDs) == 0 {
			writeData(w, http.StatusOK, []projectSubscriptionOverview{})
			return
		}

		activeSubs, err := st.GetActiveSubscriptionsByProjectIDs(projectIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to load subscriptions")
			return
		}

		owners, err := st.GetProjectOwnersByProjectIDs(projectIDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to load project owners")
			return
		}

		// Per-project credits since the active subscription's StartsAt.
		// Projects without an active subscription are simply omitted.
		periodStarts := make(map[string]time.Time, len(activeSubs))
		for pid, sub := range activeSubs {
			if sub != nil {
				periodStarts[pid] = sub.StartsAt
			}
		}
		periodCredits, err := st.SumCreditsSinceByProjects(periodStarts)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to load period credits")
			return
		}

		plans, err := st.ListPlans(false)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to load plans")
			return
		}
		// subscription.PlanName stores the plan slug (cf. GetPlanBySlug in the
		// per-project handler). Plan.Name is the human-facing tier name.
		plansBySlug := make(map[string]*types.Plan, len(plans))
		for i := range plans {
			plansBySlug[plans[i].Slug] = &plans[i]
		}

		// Bucket (projectID, rule) by windowStart so we can issue one aggregate
		// query per unique window start across all projects.
		type ruleRef struct {
			window      string
			maxCred     int64
			windowTyp   string
			anchor      *time.Time
			windowStart time.Time
		}
		bucketsByStart := make(map[time.Time]map[string]struct{}) // windowStart -> set of projectIDs
		rulesByProject := make(map[string][]ruleRef, len(projectIDs))

		for _, pid := range projectIDs {
			sub := activeSubs[pid]
			if sub == nil {
				continue
			}
			plan := plansBySlug[sub.PlanName]
			if plan == nil {
				continue
			}
			policy := plan.ToPolicy(pid, &sub.StartsAt)
			for _, rule := range policy.CreditRules {
				ws := ratelimit.WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)
				if bucketsByStart[ws] == nil {
					bucketsByStart[ws] = make(map[string]struct{})
				}
				bucketsByStart[ws][pid] = struct{}{}
				rulesByProject[pid] = append(rulesByProject[pid], ruleRef{
					window:      rule.Window,
					maxCred:     rule.MaxCredits,
					windowTyp:   rule.WindowType,
					anchor:      rule.AnchorTime,
					windowStart: ws,
				})
			}
		}

		// One SUM query per unique windowStart across all projects in that bucket.
		// Keyed by (projectID, windowStart) so duplicate window names on the
		// same project (rare but possible) don't collide.
		type usageKey struct {
			projectID   string
			windowStart time.Time
		}
		usedByRule := make(map[usageKey]float64)
		for ws, pidSet := range bucketsByStart {
			pids := make([]string, 0, len(pidSet))
			for pid := range pidSet {
				pids = append(pids, pid)
			}
			sums, err := st.SumCreditsInWindowByProjects(pids, ws)
			if err != nil {
				continue
			}
			for pid, total := range sums {
				usedByRule[usageKey{pid, ws}] = total
			}
		}

		out := make([]projectSubscriptionOverview, 0, len(projectIDs))
		for _, pid := range projectIDs {
			row := projectSubscriptionOverview{ProjectID: pid, Windows: []ratelimit.CreditWindowStatus{}}
			sub := activeSubs[pid]
			if sub != nil {
				row.PlanID = sub.PlanID
				row.PlanName = sub.PlanName
				if plan := plansBySlug[sub.PlanName]; plan != nil {
					row.DisplayName = plan.DisplayName
				}
			}
			if owner := owners[pid]; owner != nil {
				row.Owner = &projectOwnerSnapshot{
					ID:       owner.ID,
					Email:    owner.Email,
					Nickname: owner.Nickname,
					Picture:  owner.Picture,
				}
			}
			if sub != nil {
				// Round to integer thousands at the API boundary; the
				// dashboard only ever displays credits in K.
				k := int64(math.Round(periodCredits[pid] / 1000))
				row.PeriodCreditsK = &k
			}
			for _, rr := range rulesByProject[pid] {
				used := usedByRule[usageKey{pid, rr.windowStart}]
				percentage := 0.0
				if rr.maxCred > 0 {
					percentage = (used / float64(rr.maxCred)) * 100
					if percentage > 100 {
						percentage = 100
					}
				}
				percentage = math.Round(percentage*100) / 100
				s := ratelimit.CreditWindowStatus{
					Window:     rr.window,
					Percentage: percentage,
				}
				if rr.windowTyp == types.WindowTypeCalendar || rr.windowTyp == types.WindowTypeFixed {
					resetDur := ratelimit.WindowResetDuration(rr.window, rr.windowTyp, rr.anchor)
					s.ResetsAt = time.Now().UTC().Add(resetDur).Format(time.RFC3339)
				}
				row.Windows = append(row.Windows, s)
			}
			out = append(out, row)
		}

		writeData(w, http.StatusOK, out)
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
		p := parsePagination(r)
		members, total, err := st.ListProjectMembersPaginated(projectID, p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list members")
			return
		}
		writeList(w, members, total, p.Page, p.Limit())
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
	Window         string   `json:"window"`
	WindowType     string   `json:"window_type"`
	Limit          *int64   `json:"limit,omitempty"`
	Used           *float64 `json:"used,omitempty"`
	Percentage     float64  `json:"percentage"`
	ResetsAt       string   `json:"resets_at,omitempty"`
}

// serveQuotaUsage is the shared core logic for quota usage responses.
// When showCredits is false, absolute credit values (limit/used) are omitted from the response.
func serveQuotaUsage(st *store.Store, w http.ResponseWriter, projectID, userID string, showCredits bool) {
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
			Percentage: percentage,
		}
		if showCredits {
			ws.Limit = &userLimit
			ws.Used = &used
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

		serveQuotaUsage(st, w, projectID, userID, caller.IsSuperadmin)
	}
}

func handleMyQuota(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		caller := UserFromContext(r.Context())
		serveQuotaUsage(st, w, projectID, caller.ID, caller.IsSuperadmin)
	}
}

func handleMyMembership(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		caller := UserFromContext(r.Context())

		member, err := st.GetProjectMember(projectID, caller.ID)
		if err != nil || member == nil {
			// Superadmins may not be actual members; return a synthetic owner record.
			if caller.IsSuperadmin {
				writeData(w, http.StatusOK, types.ProjectMember{
					UserID:    caller.ID,
					ProjectID: projectID,
					Role:      types.RoleOwner,
				})
				return
			}
			writeError(w, http.StatusNotFound, "not_found", "not a member of this project")
			return
		}

		writeData(w, http.StatusOK, member)
	}
}

// handleMembersUsage returns quota usage for multiple members in a single request.
// Accepts ?user_ids=id1,id2,... query parameter.
func handleMembersUsage(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := UserFromContext(r.Context())
		callerMember := MemberFromContext(r.Context())
		isPrivileged := caller.IsSuperadmin ||
			(callerMember != nil && (callerMember.Role == types.RoleOwner || callerMember.Role == types.RoleMaintainer))
		if !isPrivileged {
			writeError(w, http.StatusForbidden, "forbidden", "access denied")
			return
		}

		projectID := chi.URLParam(r, "projectID")

		raw := r.URL.Query().Get("user_ids")
		if raw == "" {
			writeData(w, http.StatusOK, map[string]interface{}{})
			return
		}
		userIDs := strings.Split(raw, ",")

		activeSub, err := st.GetActiveSubscription(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to get subscription")
			return
		}

		var policy *types.RateLimitPolicy
		if activeSub != nil {
			plan, err := st.GetPlanBySlug(activeSub.PlanName)
			if err == nil && plan != nil {
				policy = plan.ToPolicy(projectID, &activeSub.StartsAt)
			}
		}

		type memberUsage struct {
			UserID  string              `json:"user_id"`
			Windows []quotaWindowStatus `json:"windows"`
		}

		result := make([]memberUsage, 0, len(userIDs))
		for _, uid := range userIDs {
			uid = strings.TrimSpace(uid)
			if uid == "" {
				continue
			}

			mu := memberUsage{UserID: uid, Windows: []quotaWindowStatus{}}

			if policy == nil {
				result = append(result, mu)
				continue
			}

			member, err := st.GetProjectMember(projectID, uid)
			if err != nil || member == nil {
				result = append(result, mu)
				continue
			}

			effectiveQuotaPct := 100.0
			if member.CreditQuotaPct != nil {
				effectiveQuotaPct = *member.CreditQuotaPct
			}

			for _, rule := range policy.CreditRules {
				if rule.EffectiveScope() != types.CreditScopeProject {
					continue
				}

				userLimit := int64(math.Round(float64(rule.MaxCredits) * (effectiveQuotaPct / 100.0)))
				windowStart := ratelimit.WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)

				used, err := st.SumCreditsInWindowByUser(projectID, uid, windowStart)
				if err != nil {
					used = 0
				}

				var percentage float64
				if userLimit == 0 {
					percentage = 100
				} else {
					percentage = (used / float64(userLimit)) * 100
					if percentage > 100 {
						percentage = 100
					}
				}
				percentage = math.Round(percentage*100) / 100

				ws := quotaWindowStatus{
					Window:     rule.Window,
					WindowType: rule.WindowType,
					Percentage: percentage,
				}
				if caller.IsSuperadmin {
					ws.Limit = &userLimit
					ws.Used = &used
				}

				if rule.WindowType == types.WindowTypeCalendar || rule.WindowType == types.WindowTypeFixed {
					resetDur := ratelimit.WindowResetDuration(rule.Window, rule.WindowType, rule.AnchorTime)
					ws.ResetsAt = time.Now().UTC().Add(resetDur).Format(time.RFC3339)
				}

				mu.Windows = append(mu.Windows, ws)
			}
			result = append(result, mu)
		}

		writeData(w, http.StatusOK, result)
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
