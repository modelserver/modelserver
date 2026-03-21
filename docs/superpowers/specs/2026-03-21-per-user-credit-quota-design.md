# Per-User Credit Quota Design

**Date:** 2026-03-21

**Goal:** Allow project owners and maintainers to configure a credit quota percentage for each member, controlling how much of the project's subscription-defined credits each user can consume. Quotas can be oversold (sum of all members' percentages > 100%), but the project's total credits remain bounded by the subscription plan.

**Tech Stack:** Go, PostgreSQL, React (TypeScript)

---

## 1. Core Concepts

- **Quota percentage**: A float value in [0, 100] assigned per member. Determines the fraction of each project-scope credit window the user may consume. Setting 0 effectively blocks the user from consuming any credits.
- **NULL quota**: No additional restriction — equivalent to 100%. User can consume up to the full project quota.
- **Overselling**: The sum of all members' quotas may exceed 100%. Each user is individually capped, but not all users are expected to hit their limit simultaneously. The project-level credit limit (from the subscription plan) remains the hard ceiling.
- **Enforcement scope**: User quotas only constrain project-scope credit rules. Key-scope rules remain unchanged (they already limit individual keys).

## 2. Data Model

### 2.1 Schema Migration

```sql
-- migration: 00X_user_credit_quota.sql

-- 1. Per-user quota on project_members
ALTER TABLE project_members
  ADD COLUMN credit_quota_percent DOUBLE PRECISION
    CHECK (credit_quota_percent >= 0 AND credit_quota_percent <= 100);

-- 2. Denormalize API key owner into requests for fast per-user credit queries
ALTER TABLE requests ADD COLUMN created_by TEXT;

-- 3. Index for per-user credit queries (hot path)
CREATE INDEX idx_requests_project_user_created
  ON requests(project_id, created_by, created_at)
  WHERE created_by IS NOT NULL;
```

**project_members.credit_quota_percent:**
- Nullable: NULL means no user-level restriction (effective 100%).
- Type: `DOUBLE PRECISION` (Go `float64`).
- Constraint: [0, 100]. Setting 0 effectively blocks the user from consuming credits.
- No constraint on the sum across members (overselling allowed).

**requests.created_by:**
- Denormalized from `api_keys.created_by` to avoid an expensive JOIN in the hot path.
- Immutable: set once at request insert time.
- Backfill existing rows separately (see Section 2.2).

### 2.2 Backfill Strategy

For existing rows, backfill `requests.created_by` in batches to avoid locking the table:

```sql
-- Run in batches of 10k rows; repeat until no rows updated
UPDATE requests r SET created_by = k.created_by
FROM api_keys k
WHERE r.api_key_id = k.id AND r.created_by IS NULL
  AND r.id IN (SELECT id FROM requests WHERE created_by IS NULL LIMIT 10000);
```

This can be run as a background job after the migration. New requests will have `created_by` populated at insert time immediately.

### 2.3 Go Type Changes

**File:** `internal/types/user.go`

```go
type ProjectMember struct {
    UserID         string    `json:"user_id"`
    ProjectID      string    `json:"project_id"`
    Role           string    `json:"role"`
    CreditQuotaPct *float64  `json:"credit_quota_percent"` // nil = 100%
    CreatedAt      time.Time `json:"created_at"`
    User           *User     `json:"user,omitempty"`
}
```

**File:** `internal/types/request.go`

```go
type Request struct {
    // ... existing fields ...
    CreatedBy string `json:"created_by,omitempty"` // NEW: denormalized from api_keys.created_by
}
```

**File:** `internal/store/requests.go`

`CreateRequest` and `BatchCreateRequests` INSERT statements must include the new `created_by` column. The value comes from `apiKey.CreatedBy` via `RequestContext.UserID`.

## 3. Rate Limiting Integration

### 3.1 New Store Method

**File:** `internal/store/usage.go`

```go
// SumCreditsInWindowByUser sums credits consumed by a user within a project
// during a time window. Uses the denormalized created_by column on requests
// (no JOIN needed).
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

### 3.2 New CheckUserQuota Method

**File:** `internal/ratelimit/composite.go`

```go
// CheckUserQuota validates per-user credit quota against project-scope rules.
// quotaPct is in [0, 100]. Only project-scope credit rules are checked.
func (c *CompositeRateLimiter) CheckUserQuota(
    ctx context.Context,
    projectID, userID string,
    quotaPct float64,
    policy *types.RateLimitPolicy,
) (bool, time.Duration, error) {
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

### 3.3 Middleware Integration

**File:** `internal/proxy/ratelimit_middleware.go`

After the existing `PreCheck` passes, add user quota check:

```go
// User quota check (after existing PreCheck)
if quotaPct := UserQuotaPctFromContext(r.Context()); quotaPct != nil {
    allowed, retryAfter, err := limiter.CheckUserQuota(
        r.Context(), project.ID, apiKey.CreatedBy, *quotaPct, policy)
    if err != nil {
        logger.Error("user quota check error", "error", err)
        // Fail open
    } else if !allowed {
        logger.Warn("user quota exceeded",
            "project_id", project.ID,
            "user_id", apiKey.CreatedBy,
            "quota_pct", *quotaPct,
        )
        // Log rejected request and return rate limit error
        // Uses same writeRateLimitError as existing PreCheck rejection
        ...
    }
}
```

Note: The existing `writeRateLimitError` returns HTTP 400 (matching Anthropic API convention for rate limits). User quota rejections use the same response format for consistency.

### 3.4 Auth Middleware — Load User Quota into Context

**File:** `internal/proxy/auth_middleware.go`

After API key validation, query the member record to get `credit_quota_percent`:

```go
// After apiKey is validated and project is loaded...
member, err := st.GetProjectMember(project.ID, apiKey.CreatedBy)
if err != nil {
    logger.Error("failed to load member for quota check", "error", err,
        "project_id", project.ID, "user_id", apiKey.CreatedBy)
    // Fail open: proceed without quota enforcement
} else if member != nil && member.CreditQuotaPct != nil {
    ctx = context.WithValue(ctx, ctxUserQuotaPct, member.CreditQuotaPct)
}
// Note: if member is nil (user removed but keys persist), no quota applies.
// This is correct — orphaned keys operate without per-user quota.
```

This query is cached (10s TTL) using the same `creditCache` pattern as the credit limiter: cache key `uq:{projectID}:{userID}`, value is the quota percent (0 sentinel for "no quota / nil"). This reuses the existing cache infrastructure with TTL support rather than introducing a separate `sync.Map`.

### 3.5 RequestContext — Add UserID

**File:** `internal/proxy/executor.go`

Add `UserID` to `RequestContext`, populated from `apiKey.CreatedBy` when building the context in the handler:

```go
type RequestContext struct {
    ProjectID        string
    APIKeyID         string
    UserID           string   // NEW: from apiKey.CreatedBy, for per-user quota tracking
    Model            string
    // ... rest unchanged
}
```

### 3.6 PostRecord — Cache Invalidation

**File:** `internal/ratelimit/composite.go`

Extend `PostRecord` to accept `userID` and invalidate user-scoped cache entries:

```go
func (c *CompositeRateLimiter) PostRecord(ctx context.Context, projectID, apiKeyID, userID, model string, usage types.TokenUsage) {
    c.classic.Record(apiKeyID, model, usage)
    c.cache.invalidatePrefix(apiKeyID)
    c.cache.invalidatePrefix("p:" + projectID)
    if userID != "" {
        c.cache.invalidatePrefix("u:" + projectID + ":" + userID)
    }
}
```

All call sites in `executor.go` (lines ~640, ~753) must pass `reqCtx.UserID`.

### 3.7 RateLimiter Interface Update

**File:** `internal/ratelimit/engine.go`

```go
type RateLimiter interface {
    PreCheck(ctx context.Context, projectID, apiKeyID, model string, policy *types.RateLimitPolicy) (bool, time.Duration, error)
    CheckUserQuota(ctx context.Context, projectID, userID string, quotaPct float64, policy *types.RateLimitPolicy) (bool, time.Duration, error) // NEW
    PostRecord(ctx context.Context, projectID, apiKeyID, userID, model string, usage types.TokenUsage) // CHANGED: added userID
}
```

## 4. API Endpoints

### 4.1 Update Member (Extended)

**`PUT /api/v1/projects/{projectID}/members/{userID}`**

Requires: owner or maintainer role.

```json
// Request body (all fields optional, omitted = no change)
{
  "role": "developer",
  "credit_quota_percent": 50   // [0, 100] or null to clear
}
```

Validation rules:
- `credit_quota_percent` must be in [0, 100] or null (to clear).
- Cannot set quota on a member with role "owner".
- Cannot set quota on yourself.
- Maintainers cannot set quota on other maintainers (only owners can restrict maintainers).
- When role is changed to "owner", `credit_quota_percent` is automatically cleared to null.

**File:** `internal/admin/handle_projects.go` — extend `handleUpdateMember`

### 4.2 List Members (Extended Response)

**`GET /api/v1/projects/{projectID}/members`**

Response now includes `credit_quota_percent`:

```json
[
  {
    "user_id": "abc",
    "role": "developer",
    "credit_quota_percent": 50,
    "created_at": "...",
    "user": { ... }
  }
]
```

**File:** `internal/admin/handle_projects.go` — automatic from `ProjectMember` struct change

### 4.3 User Quota Usage

**`GET /api/v1/projects/{projectID}/members/{userID}/quota-usage`**

Requires: owner/maintainer, or the user themselves.

Implementation follows the same pattern as `handleSubscriptionUsage` in `handle_orders.go`: load active subscription → plan → `plan.ToPolicy()` → iterate credit rules, compute per-user limits and usage.

```json
{
  "user_id": "abc",
  "credit_quota_percent": 50,
  "windows": [
    {
      "window": "5h",
      "window_type": "sliding",
      "limit": 275000,
      "used": 45000,
      "percentage": 16.36,
      "resets_at": "2026-03-21T15:00:00Z"
    }
  ]
}
```

For each project-scope credit rule:
- `limit` = `rule.MaxCredits * (quotaPct / 100)`
- `used` = `SumCreditsInWindowByUser(projectID, userID, windowStart)`
- `percentage` = `used / limit * 100`

### 4.4 My Quota (Self-View)

**`GET /api/v1/projects/{projectID}/my-quota`**

Same response format as 4.3, using the authenticated user's ID. Available to all project members.

**File:** `internal/admin/handle_projects.go` — new handlers

### 4.5 Store Methods

**File:** `internal/store/projects.go`

- `UpdateProjectMember(projectID, userID string, role *string, creditQuotaPct **float64)` — bespoke update with explicit WHERE on composite PK `(project_id, user_id)`. Cannot use `buildUpdateQuery` which only supports single-column PKs. Only updates fields that are non-nil. When role is changed to "owner", `credit_quota_percent` is forced to NULL in the same UPDATE.
- `GetProjectMember` — extend query to include `credit_quota_percent`
- `ListProjectMembers` — extend query to include `credit_quota_percent`

## 5. Frontend Changes

### 5.1 Type Update

**File:** `dashboard/src/api/types.ts`

```typescript
interface ProjectMember {
  user_id: string;
  project_id: string;
  role: string;
  credit_quota_percent: number | null; // NEW
  created_at: string;
  user?: User;
}
```

### 5.2 API Hooks

**File:** `dashboard/src/api/members.ts`

- Extend existing `useUpdateMember` to accept `credit_quota_percent` alongside `role` (same PUT endpoint, no separate hook needed)
- `useQuotaUsage(projectId, userId)` — fetch per-user quota usage (new)
- `useMyQuota(projectId)` — fetch authenticated user's own quota usage (new)

### 5.3 Members Page Changes

**File:** `dashboard/src/pages/members/MembersPage.tsx`

1. **Quota column**: Displays percentage. NULL → "100%", value → "{value}%"
2. **Usage column**: Mini progress bar showing the tightest window's consumption.
3. **Set Quota action**: New item in the per-member DropdownMenu. Opens a dialog with:
   - Number input for percentage (0-100, supports decimals)
   - Checkbox: "Remove quota (reset to 100%)" → sets null
   - Note text: "Total quotas across all members can exceed 100%."
4. **Restrictions**:
   - Hide "Set Quota" for owner-role members
   - Hide "Set Quota" for the current user (cannot set own quota)
   - Only show "Set Quota" to owner/maintainer viewers

### 5.4 Overview Page — My Quota Panel

**File:** `dashboard/src/pages/dashboard/OverviewPage.tsx`

Add a "My Quota" card showing:
- Quota percentage
- Per-window progress bars (used / limit)
- Only visible when user has a quota set (i.e., `credit_quota_percent` is not null)

## 6. File Change Summary

| File | Action | Description |
|------|--------|-------------|
| `internal/store/migrations/00X_user_credit_quota.sql` | Create | Add `credit_quota_percent` column, add `requests.created_by` + index |
| `internal/types/user.go` | Modify | Add `CreditQuotaPct` field to `ProjectMember` |
| `internal/types/request.go` | Modify | Add `CreatedBy` field to `Request` |
| `internal/store/projects.go` | Modify | Update member CRUD to handle quota field (bespoke UPDATE for composite PK) |
| `internal/store/requests.go` | Modify | Populate `created_by` in `CreateRequest` / `BatchCreateRequests` INSERT |
| `internal/store/usage.go` | Modify | Add `SumCreditsInWindowByUser` |
| `internal/ratelimit/engine.go` | Modify | Add `CheckUserQuota` to interface, add `userID` to `PostRecord` |
| `internal/ratelimit/composite.go` | Modify | Implement `CheckUserQuota`, extend PostRecord with user cache invalidation |
| `internal/proxy/executor.go` | Modify | Add `UserID` to `RequestContext`, populate from `apiKey.CreatedBy` |
| `internal/proxy/auth_middleware.go` | Modify | Load user quota into context with cached member lookup |
| `internal/proxy/ratelimit_middleware.go` | Modify | Add user quota check after PreCheck |
| `internal/admin/handle_projects.go` | Modify | Extend member handlers, add quota-usage/my-quota endpoints |
| `internal/admin/routes.go` | Modify | Register new routes |
| `dashboard/src/api/types.ts` | Modify | Add `credit_quota_percent` to `ProjectMember` |
| `dashboard/src/api/members.ts` | Modify | Extend `useUpdateMember`, add quota usage hooks |
| `dashboard/src/pages/members/MembersPage.tsx` | Modify | Quota/usage columns, set-quota dialog |
| `dashboard/src/pages/dashboard/OverviewPage.tsx` | Modify | Add "My Quota" panel |

## 7. Performance Considerations

- **Auth middleware quota lookup**: Cached 10s using `creditCache` pattern. Cache key: `uq:{projectID}:{userID}`.
- **User credit sum query**: Uses the denormalized `created_by` column on `requests` — no JOIN. Covered by the new composite index `idx_requests_project_user_created(project_id, created_by, created_at)`. Cached 10s in credit cache.
- **Request insertion**: Must populate `requests.created_by` from `apiKey.CreatedBy` at insert time. Minimal overhead (one additional column in INSERT).

## 8. Known Trade-Offs

- **Cache staleness**: Both project-level and user-level credit caches have 10s TTL. A user at 99% of their quota could exceed it by the amount of credits consumed in one 10s window before being blocked. This is consistent with the existing eventual-consistency model for project-level limits.
- **Denormalization**: The `requests.created_by` column duplicates data from `api_keys.created_by`. This is acceptable because the value is immutable and the query performance benefit is significant for the hot path.
- **Backfill**: Existing `requests` rows without `created_by` won't be counted toward user quotas until the backfill completes. This means user quotas may under-count for historical windows during the backfill period. For new deployments this is a non-issue.

## 9. Edge Cases

- **Member removed**: Quota is deleted with the member record (CASCADE). API keys created by this user may still exist — they will function without a user quota (effectively 100% until the keys are also cleaned up or the user is re-added).
- **Role changed to owner**: If a member with a quota is promoted to owner, the quota is automatically cleared (set to NULL). Enforced in the update handler.
- **Plan change/renewal**: User's effective limit changes automatically since it's computed as `plan.MaxCredits * quotaPct` at check time. No cascade needed.
- **Subscription expiry → free fallback**: Works automatically — the free plan's lower credit limits apply, and user quotas become fractions of the smaller numbers.
- **No active subscription**: If there's no active subscription (and thus no policy/credit rules), user quotas have no effect since there are no project-scope rules to apply percentages to.
