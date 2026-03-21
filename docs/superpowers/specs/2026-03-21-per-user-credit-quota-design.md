# Per-User Credit Quota Design

**Date:** 2026-03-21

**Goal:** Allow project owners and maintainers to configure a credit quota percentage for each member, controlling how much of the project's subscription-defined credits each user can consume. Quotas can be oversold (sum of all members' percentages > 100%), but the project's total credits remain bounded by the subscription plan.

**Tech Stack:** Go, PostgreSQL, React (TypeScript)

---

## 1. Core Concepts

- **Quota percentage**: A value in (0, 100] assigned per member. Determines the fraction of each project-scope credit window the user may consume.
- **NULL quota**: No additional restriction — equivalent to 100%. User can consume up to the full project quota.
- **Overselling**: The sum of all members' quotas may exceed 100%. Each user is individually capped, but not all users are expected to hit their limit simultaneously. The project-level credit limit (from the subscription plan) remains the hard ceiling.
- **Enforcement scope**: User quotas only constrain project-scope credit rules. Key-scope rules remain unchanged (they already limit individual keys).

## 2. Data Model

### 2.1 Schema Migration

```sql
-- migration: 00X_user_credit_quota.sql
ALTER TABLE project_members
  ADD COLUMN credit_quota_percent NUMERIC(5,2)
    CHECK (credit_quota_percent >= 1 AND credit_quota_percent <= 100);
```

- Nullable: NULL means no user-level restriction (effective 100%).
- Constraint: [1, 100] with 2 decimal places. No individual user can exceed the full project quota.
- Minimum 1% to prevent accidentally locking users out with tiny values.
- No constraint on the sum across members (overselling allowed).

### 2.2 Denormalized `created_by` on `requests` Table

To avoid an expensive JOIN between `requests` and `api_keys` on every rate limit check, denormalize the API key creator into the requests table:

```sql
-- Same migration file
ALTER TABLE requests ADD COLUMN created_by TEXT;

-- Backfill from api_keys (can be run async for large tables)
UPDATE requests r SET created_by = k.created_by
FROM api_keys k WHERE r.api_key_id = k.id AND r.created_by IS NULL;

-- Index for efficient per-user credit queries
CREATE INDEX idx_requests_project_user_created
  ON requests(project_id, created_by, created_at)
  WHERE created_by IS NOT NULL;
```

The `created_by` value is immutable (set once at request insert time from the API key's `created_by`). This eliminates the JOIN in the hot-path query.

### 2.2 Go Type Change

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
        // Log rejected request and return 429
        ...
    }
}
```

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

This query is cached (10s TTL) using a dedicated `sync.Map` in the auth middleware, separate from the credit cache. Cache key: `{projectID}:{userID}`, value: `*float64`.

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
// Request body (all fields optional)
{
  "role": "developer",
  "credit_quota_percent": 50   // (0, 100] or null to clear
}
```

Validation rules:
- `credit_quota_percent` must be in [1, 100] or null.
- Cannot set quota on a member with role "owner".
- Cannot set quota on yourself.
- Maintainers cannot set quota on other maintainers (only owners can restrict maintainers).

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

**File:** `internal/admin/handle_projects.go` — extend `handleListMembers` (automatic from struct change)

### 4.3 User Quota Usage

**`GET /api/v1/projects/{projectID}/members/{userID}/quota-usage`**

Requires: owner/maintainer, or the user themselves.

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

### 4.4 My Quota (Self-View)

**`GET /api/v1/projects/{projectID}/my-quota`**

Same response format as 4.3, using the authenticated user's ID. Available to all project members.

**File:** `internal/admin/handle_projects.go` — new handlers

### 4.5 Store Methods

**File:** `internal/store/projects.go`

- `UpdateProjectMember(projectID, userID string, role *string, creditQuotaPct **float64)` — bespoke update with explicit WHERE on composite PK `(project_id, user_id)`. Cannot use `buildUpdateQuery` which only supports single-column PKs. Only updates fields that are non-nil.
- `GetProjectMember` — extend query to include `credit_quota_percent`
- `ListProjectMembers` — extend query to include `credit_quota_percent`

Note: When role is changed to "owner", `credit_quota_percent` must be cleared to NULL in the same operation.

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

### 5.2 New API Hooks

**File:** `dashboard/src/api/members.ts`

- `useUpdateMemberQuota(projectId)` — PATCH quota for a member
- `useQuotaUsage(projectId, userId)` — fetch per-user quota usage
- `useMyQuota(projectId)` — fetch authenticated user's own quota usage

### 5.3 Members Page Changes

**File:** `dashboard/src/pages/members/MembersPage.tsx`

1. **Quota column**: Displays percentage. NULL → "100%", value → "{value}%"
2. **Usage column**: Mini progress bar showing the tightest window's consumption.
3. **Set Quota action**: New item in the per-member DropdownMenu. Opens a dialog with:
   - Number input for percentage (1-100)
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
- Only visible when user has a quota set (i.e., not null/100%)

## 6. File Change Summary

| File | Action | Description |
|------|--------|-------------|
| `internal/store/migrations/00X_user_credit_quota.sql` | Create | Add `credit_quota_percent` column, add `requests.created_by` + index |
| `internal/types/user.go` | Modify | Add `CreditQuotaPct` field to `ProjectMember` |
| `internal/store/projects.go` | Modify | Update member CRUD to handle quota field (bespoke UPDATE for composite PK) |
| `internal/store/usage.go` | Modify | Add `SumCreditsInWindowByUser` |
| `internal/store/requests.go` | Modify | Populate `created_by` on request insert |
| `internal/ratelimit/engine.go` | Modify | Add `CheckUserQuota` to interface, add `userID` to `PostRecord` |
| `internal/ratelimit/composite.go` | Modify | Implement `CheckUserQuota`, extend PostRecord with user cache invalidation |
| `internal/proxy/executor.go` | Modify | Add `UserID` to `RequestContext`, populate from `apiKey.CreatedBy` |
| `internal/proxy/auth_middleware.go` | Modify | Load user quota into context with dedicated cache |
| `internal/proxy/ratelimit_middleware.go` | Modify | Add user quota check after PreCheck |
| `internal/admin/handle_projects.go` | Modify | Extend member handlers, add quota-usage/my-quota endpoints |
| `internal/admin/routes.go` | Modify | Register new routes |
| `dashboard/src/api/types.ts` | Modify | Add `credit_quota_percent` to `ProjectMember` |
| `dashboard/src/api/members.ts` | Modify | Add quota-related hooks |
| `dashboard/src/pages/members/MembersPage.tsx` | Modify | Quota/usage columns, set-quota dialog |
| `dashboard/src/pages/dashboard/OverviewPage.tsx` | Modify | Add "My Quota" panel |

## 7. Performance Considerations

- **Auth middleware quota lookup**: Cached 10s in a dedicated `sync.Map`. Cache key: `{projectID}:{userID}`.
- **User credit sum query**: Uses the denormalized `created_by` column on `requests` — no JOIN. Covered by the new composite index `idx_requests_project_user_created(project_id, created_by, created_at)`. Cached 10s in credit cache.
- **Request insertion**: Must populate `requests.created_by` from `apiKey.CreatedBy` at insert time. Minimal overhead.

## 8. Known Trade-Offs

- **Cache staleness**: Both project-level and user-level credit caches have 10s TTL. A user at 99% of their quota could exceed it by the amount of credits consumed in one 10s window before being blocked. This is consistent with the existing eventual-consistency model for project-level limits.
- **Denormalization**: The `requests.created_by` column duplicates data from `api_keys.created_by`. This is acceptable because the value is immutable and the query performance benefit is significant for the hot path.

## 9. Edge Cases

- **Member removed**: Quota is deleted with the member record (CASCADE). API keys created by this user may still exist — they will function without a user quota (effectively 100% until the keys are also cleaned up or the user is re-added).
- **Role changed to owner**: If a member with a quota is promoted to owner, the quota should be cleared (set to NULL). Enforced in the update handler.
- **Plan change/renewal**: User's effective limit changes automatically since it's computed as `plan.MaxCredits * quotaPct` at check time. No cascade needed.
- **Subscription expiry → free fallback**: Works automatically — the free plan's lower credit limits apply, and user quotas become fractions of the smaller numbers.
