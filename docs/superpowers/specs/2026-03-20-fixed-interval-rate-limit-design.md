# Fixed Interval Rate Limiting

## Summary

Add a new `"fixed"` window type to the existing credit-based rate limiting system. Unlike sliding windows (which continuously move relative to `now`) or calendar windows (which align to calendar boundaries), fixed windows anchor to `subscription.starts_at` and reset every `interval`. This gives each subscription a deterministic, predictable credit refresh cycle tied to when the plan became active.

## Context

The current system supports two `CreditRule.WindowType` values:

- **`"sliding"`**: Window start = `now - duration`. The window slides forward with every request check.
- **`"calendar"`**: Window start = absolute calendar boundary (Monday 00:00 UTC for `"1w"`, 1st of month for `"1M"`).

Both are independent of when a user's subscription started. The new `"fixed"` type addresses the need for subscription-anchored billing periods where credits reset at regular, predictable intervals from the subscription start date.

## Requirements

1. New `WindowType = "fixed"` coexists with `"sliding"` and `"calendar"` — no existing behavior changes.
2. Window start is computed as `subscription.starts_at + floor((now - starts_at) / interval) × interval`.
3. Credits do NOT carry over between windows. Each window resets to `MaxCredits`.
4. `"fixed"` rules only appear in Plans linked via Subscriptions. No fallback logic needed for missing subscriptions.
5. No database schema migration required — `credit_rules` is JSONB and the new window type is just a new string value.

## Design

### 1. Data Structure Changes

#### CreditRule — new `AnchorTime` field

**File:** `internal/types/policy.go`

```go
type CreditRule struct {
    Window     string     `json:"window"`                    // "5h", "7d", "1M"
    WindowType string     `json:"window_type"`               // "sliding", "calendar", "fixed"
    MaxCredits int64      `json:"max_credits"`
    Scope      string     `json:"scope,omitempty"`           // "project" or "key"
    AnchorTime *time.Time `json:"anchor_time,omitempty"`     // fixed window anchor (runtime-injected, not persisted)
}
```

- `AnchorTime` is only meaningful when `WindowType == "fixed"`.
- It is **not persisted** in the database `credit_rules` JSONB. It is injected at runtime by `Plan.ToPolicy()`.
- `json:"anchor_time,omitempty"` ensures it does not appear in API responses for sliding/calendar rules.

#### Plan.ToPolicy() — inject AnchorTime

**File:** `internal/types/plan.go`

Current signature:
```go
func (p *Plan) ToPolicy(projectID string) *RateLimitPolicy
```

New signature:
```go
func (p *Plan) ToPolicy(projectID string, subscriptionStartsAt *time.Time) *RateLimitPolicy
```

Implementation:
- Copy `p.CreditRules` into a new slice (avoid mutating the shared Plan object).
- For each rule with `WindowType == "fixed"`, set `AnchorTime = subscriptionStartsAt`.
- Return the policy with the modified rules.

#### AuthMiddleware — pass subscription start time

**File:** `internal/proxy/auth_middleware.go`

Change the `ToPolicy` call (around line 125) from:
```go
policy = plan.ToPolicy(project.ID)
```
to:
```go
policy = plan.ToPolicy(project.ID, &subscription.StartsAt)
```

### 2. Window Calculation Logic

#### WindowStartTime — add fixed branch

**File:** `internal/ratelimit/composite.go`

Current signature:
```go
func WindowStartTime(window, windowType string) time.Time
```

New signature:
```go
func WindowStartTime(window, windowType string, anchorTime *time.Time) time.Time
```

Fixed window calculation:
```
now = time.Now().UTC()
anchor = anchorTime.UTC()
interval = parseDurationStr(window)
elapsed = now - anchor
windowIndex = floor(elapsed / interval)
windowStart = anchor + windowIndex × interval
```

Edge cases:
- `anchorTime == nil` (defensive): fall back to sliding behavior (`now - interval`).
- `now < anchorTime` (subscription hasn't started yet): return `anchorTime` itself.

#### WindowResetDuration — add fixed branch

Same signature change (add `anchorTime *time.Time`).

Fixed calculation:
```
windowEnd = windowStart + interval
retryAfter = windowEnd - now
```

#### PreCheck — pass AnchorTime through

**File:** `internal/ratelimit/composite.go`

Update the calls inside `PreCheck`:
```go
windowStart := WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)
// ...
retryAfter := WindowResetDuration(rule.Window, rule.WindowType, rule.AnchorTime)
```

### 3. Usage Handler Adaptation

**File:** `internal/proxy/usage_handler.go`

#### computeWindowStart — add fixed branch

Add `anchorTime *time.Time` parameter. For `"fixed"`, use the same algorithm as `WindowStartTime`.

#### computeWindowEnd — add fixed branch

For `"fixed"`:
```go
return windowStart.Add(parseDuration(window))
```

This produces a concrete end time (unlike sliding, which returns `now`).

#### computeCreditProgress — thread AnchorTime

Update the call chain: `HandleUsage` → `computeCreditProgress` → `computeWindowStart`, passing `rule.AnchorTime`.

### 4. What Does NOT Change

| Component | Why unchanged |
|---|---|
| `RateLimiter` interface | Signature unchanged; AnchorTime travels inside CreditRule |
| Credit cache (`cache.go`) | Cache key format `projectID\|apiKeyID\|window` still unique per rule per moment |
| Store SQL queries | `SumCreditsInWindow*` queries use `created_at >= $windowStart` — works for any window type |
| `ratelimit_middleware.go` | Passes policy transparently; no window logic here |
| Database schema | `credit_rules` is JSONB; `"fixed"` is just a new string value in the JSON |

### 5. Example

User subscribes on March 10, plan has rule `{window: "7d", window_type: "fixed", max_credits: 500000}`:

| Period | Window Start | Window End | Credits Available |
|---|---|---|---|
| Week 1 | Mar 10 00:00 | Mar 17 00:00 | 500,000 |
| Week 2 | Mar 17 00:00 | Mar 24 00:00 | 500,000 (reset) |
| Week 3 | Mar 24 00:00 | Mar 31 00:00 | 500,000 (reset) |

If the user hits the limit on Mar 12, `Retry-After` = seconds until Mar 17 00:00.

### 6. Testing Strategy

**Unit tests (core):**
- `WindowStartTime` with `windowType="fixed"`: normal case, anchor in future, exact boundary, multi-window spans.
- `WindowResetDuration` with `windowType="fixed"`: correct retry-after calculation.
- `ToPolicy` correctly injects `AnchorTime` for fixed rules and leaves others untouched.

**Integration tests:**
- Configure a plan with fixed rules, simulate requests exceeding quota, verify rate limiting triggers.
- Advance time past window boundary, verify quota resets.
- Verify `/v1/usage` endpoint returns correct `window_start` and `window_end` for fixed rules.

## Files Changed

| File | Change |
|---|---|
| `internal/types/policy.go` | Add `AnchorTime` field to `CreditRule` |
| `internal/types/plan.go` | Change `ToPolicy` signature, inject `AnchorTime` for fixed rules |
| `internal/proxy/auth_middleware.go` | Pass `&subscription.StartsAt` to `ToPolicy` |
| `internal/ratelimit/composite.go` | Add `anchorTime` param to `WindowStartTime`/`WindowResetDuration`, add fixed branches, update `PreCheck` |
| `internal/proxy/usage_handler.go` | Add fixed branches to `computeWindowStart`/`computeWindowEnd`, thread `AnchorTime` |
| New: `internal/ratelimit/composite_test.go` | Unit tests for fixed window calculations |
