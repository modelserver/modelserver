# Per-User Credit Quota Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow project owners/maintainers to assign credit quota percentages to members, enforced at the rate-limiting layer.

**Architecture:** Add `credit_quota_percent` to `project_members` and a denormalized `created_by` to `requests`. Extend the `CompositeRateLimiter` with a `CheckUserQuota` method that sums per-user credits against percentage-scaled project limits. Load quota into request context in auth middleware; check it in rate-limit middleware.

**Tech Stack:** Go, PostgreSQL, React (TypeScript), chi router, pgx, TanStack Query

**Spec:** `docs/superpowers/specs/2026-03-21-per-user-credit-quota-design.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/store/migrations/005_user_credit_quota.sql` | Create | Schema: `credit_quota_percent` column, `requests.created_by` column + index |
| `internal/types/user.go` | Modify | Add `CreditQuotaPct *float64` to `ProjectMember` |
| `internal/types/request.go` | Modify | Add `CreatedBy string` to `Request` |
| `internal/store/projects.go` | Modify | Update all member queries to include quota; new `UpdateProjectMember` |
| `internal/store/requests.go` | Modify | Add `created_by` to INSERT in `CreateRequest` / `BatchCreateRequests` |
| `internal/store/usage.go` | Modify | Add `SumCreditsInWindowByUser` |
| `internal/ratelimit/engine.go` | Modify | Add `CheckUserQuota` to interface; add `userID` param to `PostRecord` |
| `internal/ratelimit/composite.go` | Modify | Implement `CheckUserQuota`; extend `PostRecord` cache invalidation |
| `internal/proxy/executor.go` | Modify | Add `UserID` to `RequestContext`; pass to `PostRecord` |
| `internal/proxy/handler.go` | Modify | Set `reqCtx.UserID = apiKey.CreatedBy` |
| `internal/proxy/auth_middleware.go` | Modify | Load quota into context; add `ctxUserQuotaPct` + helper |
| `internal/proxy/ratelimit_middleware.go` | Modify | Add user quota check after `PreCheck` |
| `internal/admin/handle_projects.go` | Modify | Extend `handleUpdateMember`; add quota-usage + my-quota handlers |
| `internal/admin/routes.go` | Modify | Register 2 new routes |
| `dashboard/src/api/types.ts` | Modify | Add `credit_quota_percent` to `ProjectMember` |
| `dashboard/src/api/members.ts` | Modify | Extend `useUpdateMember`; add `useQuotaUsage`, `useMyQuota` |
| `dashboard/src/pages/members/MembersPage.tsx` | Modify | Quota column, usage column, set-quota dialog |
| `dashboard/src/pages/dashboard/OverviewPage.tsx` | Modify | My-quota panel |

---

### Task 1: Database Migration

**Files:**
- Create: `internal/store/migrations/005_user_credit_quota.sql`

- [ ] **Step 1: Write migration file**

```sql
-- 005_user_credit_quota.sql

-- Per-user credit quota on project_members
ALTER TABLE project_members
  ADD COLUMN credit_quota_percent DOUBLE PRECISION
    CHECK (credit_quota_percent >= 0 AND credit_quota_percent <= 100);

-- Denormalize API key owner into requests for fast per-user credit queries
ALTER TABLE requests ADD COLUMN created_by TEXT;

-- Index for per-user credit sum queries (hot path)
CREATE INDEX idx_requests_project_user_created
  ON requests(project_id, created_by, created_at)
  WHERE created_by IS NOT NULL;
```

- [ ] **Step 2: Verify migration applies cleanly**

Run: `psql $DATABASE_URL -f internal/store/migrations/005_user_credit_quota.sql`
Expected: Three statements succeed, no errors.

- [ ] **Step 3: Verify schema**

Run: `psql $DATABASE_URL -c "\d project_members"` and `psql $DATABASE_URL -c "\d requests"`
Expected: `credit_quota_percent` column on `project_members` (double precision, nullable). `created_by` column on `requests` (text, nullable).

- [ ] **Step 4: Note — Backfill is a post-deployment step**

After deploying, backfill existing `requests.created_by` rows in batches:

```sql
-- Run repeatedly until 0 rows updated
UPDATE requests r SET created_by = k.created_by
FROM api_keys k
WHERE r.api_key_id = k.id AND r.created_by IS NULL
  AND r.id IN (SELECT id FROM requests WHERE created_by IS NULL LIMIT 10000);
```

New requests will have `created_by` populated at insert time immediately (Task 3). User quotas will under-count for historical windows until the backfill completes.

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/005_user_credit_quota.sql
git commit -m "feat(quota): add migration for per-user credit quota"
```

---

### Task 2: Go Types — ProjectMember + Request

**Files:**
- Modify: `internal/types/user.go:43-51`
- Modify: `internal/types/request.go:25-52`

- [ ] **Step 1: Add `CreditQuotaPct` to `ProjectMember`**

In `internal/types/user.go`, replace the `ProjectMember` struct (lines 43-51):

```go
// ProjectMember links a User to a Project with an assigned role.
type ProjectMember struct {
	UserID         string    `json:"user_id"`
	ProjectID      string    `json:"project_id"`
	Role           string    `json:"role"`
	CreditQuotaPct *float64  `json:"credit_quota_percent"` // nil = no limit (effective 100%)
	CreatedAt      time.Time `json:"created_at"`

	// User is populated when the record is fetched with a join.
	User *User `json:"user,omitempty"`
}
```

- [ ] **Step 2: Add `CreatedBy` to `Request`**

In `internal/types/request.go`, add this field after `ErrorMessage` (line 43) and before the routing observability comment (line 44):

```go
	CreatedBy       string    `json:"created_by,omitempty"`
```

- [ ] **Step 3: Verify build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: Compiles with no errors.

- [ ] **Step 4: Commit**

```bash
git add internal/types/user.go internal/types/request.go
git commit -m "feat(quota): add CreditQuotaPct to ProjectMember and CreatedBy to Request"
```

---

### Task 3: Store Layer — Member Queries + Usage Query

**Files:**
- Modify: `internal/store/projects.go:168-222`
- Modify: `internal/store/requests.go:13-26,64-75`
- Modify: `internal/store/usage.go` (append)

- [ ] **Step 1: Update `GetProjectMember` to include quota**

In `internal/store/projects.go`, replace `GetProjectMember` (lines 168-183):

```go
// GetProjectMember returns a single member.
func (s *Store) GetProjectMember(projectID, userID string) (*types.ProjectMember, error) {
	m := &types.ProjectMember{}
	err := s.pool.QueryRow(context.Background(), `
		SELECT user_id, project_id, role, credit_quota_percent, created_at
		FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreditQuotaPct, &m.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}
	return m, nil
}
```

- [ ] **Step 2: Update `ListProjectMembers` to include quota**

Replace `ListProjectMembers` (lines 185-214):

```go
// ListProjectMembers returns all members of a project with user info.
func (s *Store) ListProjectMembers(projectID string) ([]types.ProjectMember, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT pm.user_id, pm.project_id, pm.role, pm.credit_quota_percent, pm.created_at,
			u.id, u.email, u.nickname, COALESCE(u.picture, '')
		FROM project_members pm
		JOIN users u ON pm.user_id = u.id
		WHERE pm.project_id = $1
		ORDER BY pm.created_at ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var members []types.ProjectMember
	for rows.Next() {
		var m types.ProjectMember
		var u types.User
		if err := rows.Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreditQuotaPct, &m.CreatedAt,
			&u.ID, &u.Email, &u.Nickname, &u.Picture); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		m.User = &u
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}
	return members, nil
}
```

- [ ] **Step 3: Add `UpdateProjectMember` (bespoke composite-PK update)**

Add after `UpdateProjectMemberRole` in `internal/store/projects.go`:

```go
// UpdateProjectMember updates a member's role and/or credit quota.
// Pass nil pointers to leave fields unchanged.
// If role is set to "owner", credit_quota_percent is forced to NULL.
func (s *Store) UpdateProjectMember(projectID, userID string, role *string, creditQuotaPct **float64) error {
	if role == nil && creditQuotaPct == nil {
		return nil
	}

	// If promoting to owner, clear quota.
	if role != nil && *role == types.RoleOwner {
		_, err := s.pool.Exec(context.Background(), `
			UPDATE project_members SET role = $1, credit_quota_percent = NULL
			WHERE project_id = $2 AND user_id = $3`, *role, projectID, userID)
		return err
	}

	if role != nil && creditQuotaPct != nil {
		_, err := s.pool.Exec(context.Background(), `
			UPDATE project_members SET role = $1, credit_quota_percent = $2
			WHERE project_id = $3 AND user_id = $4`, *role, *creditQuotaPct, projectID, userID)
		return err
	}

	if role != nil {
		_, err := s.pool.Exec(context.Background(), `
			UPDATE project_members SET role = $1
			WHERE project_id = $2 AND user_id = $3`, *role, projectID, userID)
		return err
	}

	// creditQuotaPct only
	_, err := s.pool.Exec(context.Background(), `
		UPDATE project_members SET credit_quota_percent = $1
		WHERE project_id = $2 AND user_id = $3`, *creditQuotaPct, projectID, userID)
	return err
}
```

- [ ] **Step 4: Update `CreateRequest` to include `created_by`**

In `internal/store/requests.go`, replace `CreateRequest` (lines 13-26):

```go
// CreateRequest inserts a new request log.
// UpstreamID may be empty at creation time (set later via CompleteRequest).
func (s *Store) CreateRequest(r *types.Request) error {
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO requests (project_id, api_key_id, upstream_id, trace_id, msg_id, provider, model, streaming,
			status, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			credits_consumed, latency_ms, ttft_ms, error_message, client_ip, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		RETURNING id, created_at`,
		r.ProjectID, r.APIKeyID, nullString(r.UpstreamID),
		nullString(r.TraceID), nullString(r.MsgID),
		r.Provider, r.Model, r.Streaming, r.Status,
		r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens,
		r.CreditsConsumed, r.LatencyMs, r.TTFTMs, nullString(r.ErrorMessage), r.ClientIP,
		nullString(r.CreatedBy),
	).Scan(&r.ID, &r.CreatedAt)
}
```

- [ ] **Step 5: Update `BatchCreateRequests` to include `created_by`**

In `internal/store/requests.go`, replace the inner QueryRow in `BatchCreateRequests` (lines 64-75) similarly — add `created_by` to column list and `nullString(r.CreatedBy)` to values. Follow the exact same pattern as Step 4.

- [ ] **Step 6: Add `SumCreditsInWindowByUser`**

Append to `internal/store/usage.go`:

```go
// SumCreditsInWindowByUser sums credits consumed by a user within a project
// during a time window. Uses the denormalized created_by column on requests.
func (s *Store) SumCreditsInWindowByUser(projectID, userID string, windowStart time.Time) (float64, error) {
	var total float64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(credits_consumed), 0)
		FROM requests
		WHERE project_id = $1 AND created_by = $2 AND created_at >= $3`,
		projectID, userID, windowStart,
	).Scan(&total)
	return total, err
}
```

- [ ] **Step 7: Verify build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: Compiles with no errors.

- [ ] **Step 8: Commit**

```bash
git add internal/store/projects.go internal/store/requests.go internal/store/usage.go
git commit -m "feat(quota): update store layer for per-user credit quota"
```

---

### Task 4: Rate Limiter — Interface + CheckUserQuota

**Files:**
- Modify: `internal/ratelimit/engine.go:10-18`
- Modify: `internal/ratelimit/composite.go:79-84`

- [ ] **Step 1: Update `RateLimiter` interface**

In `internal/ratelimit/engine.go`, replace the interface (lines 10-18):

```go
// RateLimiter checks and records rate limit usage.
type RateLimiter interface {
	// PreCheck validates whether a request should be allowed.
	// Returns (allowed, retryAfter, error).
	PreCheck(ctx context.Context, projectID, apiKeyID, model string, policy *types.RateLimitPolicy) (bool, time.Duration, error)

	// CheckUserQuota validates per-user credit quota against project-scope rules.
	// quotaPct is in [0, 100]. Only project-scope credit rules are checked.
	// Returns (allowed, retryAfter, error).
	CheckUserQuota(ctx context.Context, projectID, userID string, quotaPct float64, policy *types.RateLimitPolicy) (bool, time.Duration, error)

	// PostRecord records actual usage after a response completes.
	PostRecord(ctx context.Context, projectID, apiKeyID, userID, model string, usage types.TokenUsage)
}
```

- [ ] **Step 2: Implement `CheckUserQuota` on `CompositeRateLimiter`**

Add this method to `internal/ratelimit/composite.go` (after `PreCheck`, before `PostRecord`):

```go
// CheckUserQuota validates per-user credit quota against project-scope rules.
func (c *CompositeRateLimiter) CheckUserQuota(ctx context.Context, projectID, userID string, quotaPct float64, policy *types.RateLimitPolicy) (bool, time.Duration, error) {
	if policy == nil {
		return true, 0, nil
	}
	for _, rule := range policy.CreditRules {
		if rule.EffectiveScope() != types.CreditScopeProject {
			continue
		}
		windowStart := WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)
		userLimit := float64(rule.MaxCredits) * (quotaPct / 100.0)

		cacheKey := fmt.Sprintf("u:%s:%s|%s|%s", projectID, userID, rule.Window, rule.WindowType)
		var used float64
		if cached, ok := c.cache.get(cacheKey); ok {
			used = cached
		} else {
			var err error
			used, err = c.store.SumCreditsInWindowByUser(projectID, userID, windowStart)
			if err != nil {
				c.logger.Error("user quota check query failed", "error", err)
				return true, 0, nil // Fail open.
			}
			c.cache.set(cacheKey, used)
		}

		if used >= userLimit {
			retryAfter := WindowResetDuration(rule.Window, rule.WindowType, rule.AnchorTime)
			return false, retryAfter, nil
		}
	}
	return true, 0, nil
}
```

- [ ] **Step 3: Extend `PostRecord` to accept `userID` and invalidate user cache**

Replace `PostRecord` in `internal/ratelimit/composite.go` (lines 79-84):

```go
// PostRecord records actual usage and invalidates caches.
func (c *CompositeRateLimiter) PostRecord(ctx context.Context, projectID, apiKeyID, userID, model string, usage types.TokenUsage) {
	c.classic.Record(apiKeyID, model, usage)
	c.cache.invalidatePrefix(apiKeyID)
	c.cache.invalidatePrefix("p:" + projectID)
	if userID != "" {
		c.cache.invalidatePrefix("u:" + projectID + ":" + userID)
	}
}
```

- [ ] **Step 4: Verify build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: Compile errors about `PostRecord` call sites in `executor.go` — they still pass old argument count. This is expected and will be fixed in Task 5 alongside other executor changes.

- [ ] **Step 5: Commit**

```bash
git add internal/ratelimit/engine.go internal/ratelimit/composite.go
git commit -m "feat(quota): implement CheckUserQuota in rate limiter"
```

---

### Task 5: Proxy Layer — RequestContext, Auth Middleware, Rate Limit Middleware

**Files:**
- Modify: `internal/proxy/executor.go:23-38`
- Modify: `internal/proxy/handler.go:114-128`
- Modify: `internal/proxy/auth_middleware.go:16-23,57-152`
- Modify: `internal/proxy/ratelimit_middleware.go:18-75`

- [ ] **Step 1: Add `UserID` to `RequestContext`**

In `internal/proxy/executor.go`, add `UserID` field to the `RequestContext` struct (after `APIKeyID`, line 25):

```go
	UserID           string
```

- [ ] **Step 2: Fix `PostRecord` call sites in executor**

In `internal/proxy/executor.go`, update both `PostRecord` calls (around lines 640 and 753). Each currently looks like:

```go
e.rateLimiter.PostRecord(context.Background(), reqCtx.Project.ID, reqCtx.APIKeyID, model, types.TokenUsage{
```

Change both to:

```go
e.rateLimiter.PostRecord(context.Background(), reqCtx.Project.ID, reqCtx.APIKeyID, reqCtx.UserID, model, types.TokenUsage{
```

- [ ] **Step 3: Set `UserID` in handler**

In `internal/proxy/handler.go`, add this line inside the `reqCtx` initialization (after `APIKeyID: apiKey.ID,`, around line 116):

```go
		UserID:           apiKey.CreatedBy,
```

- [ ] **Step 4: Add context key and helper for user quota**

In `internal/proxy/auth_middleware.go`, add the context key (after `ctxSubscription`, line 22):

```go
	ctxUserQuotaPct contextKey = "user_quota_pct"
```

Add this helper function after `SubscriptionFromContext`:

```go
// UserQuotaPctFromContext returns the user's credit quota percentage from the context.
// Returns nil if no quota is set (user has full access).
func UserQuotaPctFromContext(ctx context.Context) *float64 {
	if p, ok := ctx.Value(ctxUserQuotaPct).(*float64); ok {
		return p
	}
	return nil
}
```

- [ ] **Step 5: Load user quota in `AuthMiddleware`**

In `internal/proxy/auth_middleware.go`, add a package-level quota cache (add near the top of the file, after imports):

```go
// quotaCache caches per-user credit quota lookups to avoid per-request DB hits.
// Uses the same creditCache pattern (float64 with TTL). Sentinel -1 means "no quota set".
var quotaCache = ratelimit.NewCreditCache(10 * time.Second)
```

Note: This requires exporting `NewCreditCache` from the ratelimit package (rename `newCreditCache` → `NewCreditCache` in `internal/ratelimit/cache.go`).

Then, add this block just before `go st.UpdateAPIKeyLastUsed(apiKey.ID)` (before line 139). The full replacement of lines 139-149 should be:

```go
			// Load per-user credit quota (cached 10s).
			var userQuotaPct *float64
			quotaCacheKey := project.ID + ":" + apiKey.CreatedBy
			if cached, ok := quotaCache.Get(quotaCacheKey); ok {
				if cached >= 0 { // -1 sentinel = no quota
					v := cached
					userQuotaPct = &v
				}
			} else {
				member, memberErr := st.GetProjectMember(project.ID, apiKey.CreatedBy)
				if memberErr != nil {
					// Fail open: proceed without quota enforcement.
				} else if member != nil && member.CreditQuotaPct != nil {
					userQuotaPct = member.CreditQuotaPct
					quotaCache.Set(quotaCacheKey, *member.CreditQuotaPct)
				} else {
					quotaCache.Set(quotaCacheKey, -1) // sentinel: no quota
				}
			}

			go st.UpdateAPIKeyLastUsed(apiKey.ID)

			ctx := context.WithValue(r.Context(), ctxAPIKey, apiKey)
			ctx = context.WithValue(ctx, ctxProject, project)
			if policy != nil {
				ctx = context.WithValue(ctx, ctxPolicy, policy)
			}
			if subscription != nil {
				ctx = context.WithValue(ctx, ctxSubscription, subscription)
			}
			if userQuotaPct != nil {
				ctx = context.WithValue(ctx, ctxUserQuotaPct, userQuotaPct)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
```

Also add to imports: `"github.com/modelserver/modelserver/internal/ratelimit"`

- [ ] **Step 6: Add user quota check in `RateLimitMiddleware`**

In `internal/proxy/ratelimit_middleware.go`, add the user quota check **after** the existing `PreCheck` block (after line 42, the `}` closing the `if err` block) and **before** `next.ServeHTTP(w, r)` (line 72). Insert this block:

```go
			// Per-user credit quota check.
			if quotaPct := UserQuotaPctFromContext(r.Context()); quotaPct != nil {
				uAllowed, uRetryAfter, uErr := limiter.CheckUserQuota(r.Context(), project.ID, apiKey.CreatedBy, *quotaPct, policy)
				if uErr != nil {
					logger.Error("user quota check error", "error", uErr)
					// Fail open.
				} else if !uAllowed {
					logger.Warn("user quota exceeded",
						"project_id", project.ID,
						"api_key_id", apiKey.ID,
						"user_id", apiKey.CreatedBy,
						"quota_pct", *quotaPct,
					)

					model := peekModel(r)
					traceID := TraceIDFromContext(r.Context())
					clientIP := r.RemoteAddr
					errMsg := fmt.Sprintf("user quota exceeded, retry after %ds", int(uRetryAfter.Seconds()))

					req := &types.Request{
						ProjectID:    project.ID,
						APIKeyID:     apiKey.ID,
						CreatedBy:    apiKey.CreatedBy,
						TraceID:      traceID,
						Model:        model,
						Status:       types.RequestStatusRateLimited,
						ClientIP:     clientIP,
						ErrorMessage: errMsg,
					}
					go st.CreateRequest(req)

					writeRateLimitError(w, uRetryAfter)
					return
				}
			}
```

Also update the existing PreCheck rejection block (lines 56-66) to set `CreatedBy` on the logged request:

Add after `APIKeyID: apiKey.ID,` (line 58):

```go
					CreatedBy:    apiKey.CreatedBy,
```

- [ ] **Step 7: Also set `CreatedBy` on pending request in handler.go**

In `internal/proxy/handler.go`, add `CreatedBy` to the `pendingReq` struct (after `APIKeyID: apiKey.ID,`, around line 102):

```go
		CreatedBy: apiKey.CreatedBy,
```

- [ ] **Step 8: Verify build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: Compiles with no errors.

- [ ] **Step 9: Commit**

```bash
git add internal/proxy/executor.go internal/proxy/handler.go internal/proxy/auth_middleware.go internal/proxy/ratelimit_middleware.go
git commit -m "feat(quota): integrate per-user quota into proxy pipeline"
```

---

### Task 6: Admin API — Update Member Handler + Quota Usage Endpoints

**Files:**
- Modify: `internal/admin/handle_projects.go:206-225`
- Modify: `internal/admin/routes.go:96-99`

- [ ] **Step 1: Rewrite `handleUpdateMember` to support quota**

Replace `handleUpdateMember` in `internal/admin/handle_projects.go` (lines 206-225):

```go
func handleUpdateMember(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")

		var body struct {
			Role             *string  `json:"role"`
			CreditQuotaPct   *float64 `json:"credit_quota_percent"`
			ClearQuota       bool     `json:"clear_quota"` // explicit null signal
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		// Validate: at least one field must be provided.
		if body.Role == nil && body.CreditQuotaPct == nil && !body.ClearQuota {
			writeError(w, http.StatusBadRequest, "bad_request", "no fields to update")
			return
		}

		// Validate quota range.
		if body.CreditQuotaPct != nil && (*body.CreditQuotaPct < 0 || *body.CreditQuotaPct > 100) {
			writeError(w, http.StatusBadRequest, "bad_request", "credit_quota_percent must be between 0 and 100")
			return
		}

		// Cannot set quota on yourself.
		caller := UserFromContext(r.Context())
		if caller != nil && caller.ID == userID && (body.CreditQuotaPct != nil || body.ClearQuota) {
			writeError(w, http.StatusBadRequest, "bad_request", "cannot set quota on yourself")
			return
		}

		// Load target member.
		target, err := st.GetProjectMember(projectID, userID)
		if err != nil || target == nil {
			writeError(w, http.StatusNotFound, "not_found", "member not found")
			return
		}

		// Cannot set quota on owner.
		targetRole := target.Role
		if body.Role != nil {
			targetRole = *body.Role
		}
		if targetRole == types.RoleOwner && (body.CreditQuotaPct != nil || body.ClearQuota) {
			writeError(w, http.StatusBadRequest, "bad_request", "cannot set quota on an owner")
			return
		}

		// Maintainers cannot restrict other maintainers.
		callerMember := MemberFromContext(r.Context())
		if callerMember != nil && callerMember.Role == types.RoleMaintainer &&
			target.Role == types.RoleMaintainer && (body.CreditQuotaPct != nil || body.ClearQuota) {
			writeError(w, http.StatusForbidden, "forbidden", "maintainers cannot set quota on other maintainers")
			return
		}

		// Build update args.
		var quotaPtr **float64
		if body.ClearQuota {
			var nilFloat *float64
			quotaPtr = &nilFloat // set to NULL
		} else if body.CreditQuotaPct != nil {
			quotaPtr = &body.CreditQuotaPct
		}

		if err := st.UpdateProjectMember(projectID, userID, body.Role, quotaPtr); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update member")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
```

- [ ] **Step 2: Add `handleQuotaUsage` handler**

Add to `internal/admin/handle_projects.go` after `handleRemoveMember`:

```go
func handleQuotaUsage(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")

		// Access control: owner/maintainer or self.
		caller := UserFromContext(r.Context())
		callerMember := MemberFromContext(r.Context())
		if caller != nil && caller.ID != userID {
			if callerMember != nil && callerMember.Role != types.RoleOwner && callerMember.Role != types.RoleMaintainer {
				writeError(w, http.StatusForbidden, "forbidden", "not authorized")
				return
			}
		}

		member, err := st.GetProjectMember(projectID, userID)
		if err != nil || member == nil {
			writeError(w, http.StatusNotFound, "not_found", "member not found")
			return
		}

		// Effective quota: nil means 100%.
		effectiveQuota := 100.0
		if member.CreditQuotaPct != nil {
			effectiveQuota = *member.CreditQuotaPct
		}

		// Load active subscription → plan → policy.
		activeSub, _ := st.GetActiveSubscription(projectID)
		if activeSub == nil {
			writeData(w, http.StatusOK, map[string]interface{}{
				"user_id":              userID,
				"credit_quota_percent": member.CreditQuotaPct,
				"windows":             []interface{}{},
			})
			return
		}

		plan, _ := st.GetPlanBySlug(activeSub.PlanName)
		if plan == nil {
			writeData(w, http.StatusOK, map[string]interface{}{
				"user_id":              userID,
				"credit_quota_percent": member.CreditQuotaPct,
				"windows":             []interface{}{},
			})
			return
		}

		policy := plan.ToPolicy(projectID, &activeSub.StartsAt)

		type windowStatus struct {
			Window     string  `json:"window"`
			WindowType string  `json:"window_type"`
			Limit      float64 `json:"limit"`
			Used       float64 `json:"used"`
			Percentage float64 `json:"percentage"`
			ResetsAt   string  `json:"resets_at,omitempty"`
		}

		var windows []windowStatus
		for _, rule := range policy.CreditRules {
			if rule.EffectiveScope() != types.CreditScopeProject {
				continue
			}
			windowStart := ratelimit.WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)
			limit := float64(rule.MaxCredits) * (effectiveQuota / 100.0)
			used, _ := st.SumCreditsInWindowByUser(projectID, userID, windowStart)

			pct := 100.0
			if limit > 0 {
				pct = (used / limit) * 100
				if pct > 100 {
					pct = 100
				}
			}

			ws := windowStatus{
				Window:     rule.Window,
				WindowType: rule.WindowType,
				Limit:      limit,
				Used:       used,
				Percentage: pct,
			}
			if rule.WindowType == types.WindowTypeCalendar || rule.WindowType == types.WindowTypeFixed {
				resetDur := ratelimit.WindowResetDuration(rule.Window, rule.WindowType, rule.AnchorTime)
				ws.ResetsAt = time.Now().UTC().Add(resetDur).Format(time.RFC3339)
			}
			windows = append(windows, ws)
		}

		writeData(w, http.StatusOK, map[string]interface{}{
			"user_id":              userID,
			"credit_quota_percent": member.CreditQuotaPct,
			"windows":             windows,
		})
	}
}
```

- [ ] **Step 3: Add `handleMyQuota` handler**

Add after `handleQuotaUsage`. Instead of manipulating chi URL params (fragile), call a shared helper directly:

```go
func handleMyQuota(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := UserFromContext(r.Context())
		if caller == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
			return
		}
		projectID := chi.URLParam(r, "projectID")
		serveQuotaUsage(st, w, projectID, caller.ID)
	}
}
```

Then refactor `handleQuotaUsage` to extract the core logic into `serveQuotaUsage(st, w, projectID, userID)` and have `handleQuotaUsage` call it after its access-control checks:

```go
func handleQuotaUsage(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		userID := chi.URLParam(r, "userID")

		// Access control: owner/maintainer or self.
		caller := UserFromContext(r.Context())
		callerMember := MemberFromContext(r.Context())
		if caller != nil && caller.ID != userID {
			if callerMember != nil && callerMember.Role != types.RoleOwner && callerMember.Role != types.RoleMaintainer {
				writeError(w, http.StatusForbidden, "forbidden", "not authorized")
				return
			}
		}

		serveQuotaUsage(st, w, projectID, userID)
	}
}
```

The `serveQuotaUsage` function contains all the logic from the original `handleQuotaUsage` body (member lookup, subscription → plan → policy, per-window computation) without any access control — that's handled by the calling handler.

- [ ] **Step 4: Register new routes**

In `internal/admin/routes.go`, add these routes inside the project member section (after line 99, `r.Delete("/members/{userID}", handleRemoveMember(st))`):

```go
					r.Get("/members/{userID}/quota-usage", handleQuotaUsage(st))
					r.Get("/my-quota", handleMyQuota(st))
```

- [ ] **Step 5: Add necessary imports**

In `internal/admin/handle_projects.go`, ensure these imports are present:

```go
	"github.com/modelserver/modelserver/internal/ratelimit"
```

(The `time` import should already be present. `types` and `chi` are already imported.)

- [ ] **Step 6: Verify build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: Compiles with no errors.

- [ ] **Step 7: Commit**

```bash
git add internal/admin/handle_projects.go internal/admin/routes.go
git commit -m "feat(quota): add admin API for quota management and usage"
```

---

### Task 7: Frontend — Types + API Hooks

**Files:**
- Modify: `dashboard/src/api/types.ts:64-70`
- Modify: `dashboard/src/api/members.ts`

- [ ] **Step 1: Update `ProjectMember` type**

In `dashboard/src/api/types.ts`, replace lines 64-70:

```typescript
export interface ProjectMember {
  user_id: string;
  project_id: string;
  role: "owner" | "maintainer" | "developer";
  credit_quota_percent: number | null;
  created_at: string;
  user?: User;
}
```

- [ ] **Step 2: Add `QuotaWindowStatus` type**

Add after the `ProjectMember` interface:

```typescript
export interface QuotaWindowStatus {
  window: string;
  window_type: string;
  limit: number;
  used: number;
  percentage: number;
  resets_at?: string;
}

export interface QuotaUsageResponse {
  user_id: string;
  credit_quota_percent: number | null;
  windows: QuotaWindowStatus[];
}
```

- [ ] **Step 3: Extend `useUpdateMember` and add quota hooks**

Replace the full content of `dashboard/src/api/members.ts`:

```typescript
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, ProjectMember, QuotaUsageResponse } from "./types";

export function useMembers(projectId: string) {
  return useQuery({
    queryKey: ["members", projectId],
    queryFn: () =>
      api.get<ListResponse<ProjectMember>>(
        `/api/v1/projects/${projectId}/members?per_page=100`,
      ),
  });
}

export function useAddMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { email: string; role: string }) =>
      api.post(`/api/v1/projects/${projectId}/members`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["members", projectId] }),
  });
}

export function useUpdateMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      userId,
      role,
      credit_quota_percent,
      clear_quota,
    }: {
      userId: string;
      role?: string;
      credit_quota_percent?: number;
      clear_quota?: boolean;
    }) =>
      api.put(`/api/v1/projects/${projectId}/members/${userId}`, {
        role,
        credit_quota_percent,
        clear_quota,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["members", projectId] }),
  });
}

export function useRemoveMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) =>
      api.delete(`/api/v1/projects/${projectId}/members/${userId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["members", projectId] }),
  });
}

export function useQuotaUsage(projectId: string, userId: string) {
  return useQuery({
    queryKey: ["quota-usage", projectId, userId],
    queryFn: () =>
      api.get<DataResponse<QuotaUsageResponse>>(
        `/api/v1/projects/${projectId}/members/${userId}/quota-usage`,
      ),
    enabled: !!userId,
  });
}

export function useMyQuota(projectId: string) {
  return useQuery({
    queryKey: ["my-quota", projectId],
    queryFn: () =>
      api.get<DataResponse<QuotaUsageResponse>>(
        `/api/v1/projects/${projectId}/my-quota`,
      ),
  });
}
```

- [ ] **Step 4: Verify frontend build**

Run: `cd /root/coding/modelserver/dashboard && npm run build`
Expected: Build succeeds (MembersPage may show type errors about the changed `useUpdateMember` signature — that's expected and will be fixed in Task 8).

- [ ] **Step 5: Commit**

```bash
git add dashboard/src/api/types.ts dashboard/src/api/members.ts
git commit -m "feat(quota): update frontend types and API hooks for quota"
```

---

### Task 8: Frontend — Members Page (Quota Column + Dialog)

**Files:**
- Modify: `dashboard/src/pages/members/MembersPage.tsx`

- [ ] **Step 1: Rewrite MembersPage with quota support**

Replace the entire content of `dashboard/src/pages/members/MembersPage.tsx` with the updated version that adds:
1. Quota column (NULL → "100%", value → "{value}%")
2. Set Quota action in the dropdown menu
3. Set Quota dialog with number input + "Remove quota" checkbox
4. Restrictions: hide Set Quota for owners, for self, and for non-owner/maintainer viewers

The key changes are:
- Add a "Quota" column after "Role" that shows `m.credit_quota_percent ?? 100` + "%"
- Add a "Set Quota" DropdownMenuItem that opens a dialog
- The dialog has a number input (0-100) and a "Remove quota" checkbox
- Wire the dialog to `useUpdateMember` with `credit_quota_percent` or `clear_quota`
- Fix the existing role-change calls to use the new `useUpdateMember` signature (only pass `role`)

This is a UI-heavy step — implement the dialog as a new state variable (`showQuota`, `quotaTarget`, `quotaValue`, `removeQuota`) and handle the submit.

- [ ] **Step 2: Verify frontend build**

Run: `cd /root/coding/modelserver/dashboard && npm run build`
Expected: Build succeeds with no errors.

- [ ] **Step 3: Manual test**

Run the dashboard, navigate to Members page, verify:
- Quota column shows "100%" for members without quota
- "Set Quota" option appears in dropdown for non-owner members
- Dialog allows setting quota value
- "Remove quota" checkbox clears the quota

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/pages/members/MembersPage.tsx
git commit -m "feat(quota): add quota column and set-quota dialog to members page"
```

---

### Task 9: Frontend — My Quota Panel on Overview Page

**Files:**
- Modify: `dashboard/src/pages/dashboard/OverviewPage.tsx`

- [ ] **Step 1: Add My Quota panel**

In `dashboard/src/pages/dashboard/OverviewPage.tsx`, add:
- Import `useMyQuota` from `@/api/members`
- Call `useMyQuota(projectId)` to fetch quota data
- Add a Card component showing quota percentage and per-window progress bars
- Only render the card when `credit_quota_percent` is not null (user has a quota set)

The panel should show:
- Title: "My Quota"
- Subtitle: "Your credit allocation: {quota}%"
- For each window: a progress bar with "{used} / {limit}" label and percentage

- [ ] **Step 2: Verify frontend build**

Run: `cd /root/coding/modelserver/dashboard && npm run build`
Expected: Build succeeds with no errors.

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/pages/dashboard/OverviewPage.tsx
git commit -m "feat(quota): add my-quota panel to overview page"
```

---

### Task 10: End-to-End Verification

- [ ] **Step 1: Run the full Go backend**

Run: `cd /root/coding/modelserver && go build ./... && go vet ./...`
Expected: Clean build and vet.

- [ ] **Step 2: Run the full frontend build**

Run: `cd /root/coding/modelserver/dashboard && npm run build`
Expected: Clean build.

- [ ] **Step 3: Manual E2E test**

1. Start the server
2. Create a project, add a member
3. Set a quota (e.g. 50%) on the member via the Members page
4. Verify the member's API key requests are limited to 50% of the project quota
5. Check the /my-quota endpoint returns correct usage data
6. Remove the quota and verify the member gets 100% again

- [ ] **Step 4: Final commit if any cleanup needed**

```bash
git add -A
git commit -m "feat(quota): per-user credit quota - final cleanup"
```
