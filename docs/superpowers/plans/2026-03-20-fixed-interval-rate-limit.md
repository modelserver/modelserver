# Fixed Interval Rate Limiting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `"fixed"` window type to the credit-based rate limiter that anchors to `subscription.starts_at` and resets credits every interval.

**Architecture:** Add `AnchorTime` field to `CreditRule` (runtime-injected by `ToPolicy`), refactor `WindowStartTime`/`WindowResetDuration` into testable `*At` variants with a `now` parameter, then thread the new parameter through all call sites (PreCheck, usage handler, admin handler). Export `ParseDurationStr` to eliminate the duplicate parser in `usage_handler.go`.

**Tech Stack:** Go, PostgreSQL (no schema changes)

**Spec:** `docs/superpowers/specs/2026-03-20-fixed-interval-rate-limit-design.md`

---

## File Structure

| File | Role | Action |
|---|---|---|
| `internal/types/policy.go` | CreditRule struct + window type constant | Modify |
| `internal/types/plan.go` | Plan.ToPolicy() | Modify |
| `internal/ratelimit/composite.go` | Window calculation + PreCheck + cache keys | Modify |
| `internal/ratelimit/composite_test.go` | Unit tests for window calculation | Create |
| `internal/proxy/auth_middleware.go` | Subscription → ToPolicy call | Modify |
| `internal/proxy/usage_handler.go` | Usage endpoint window calculation | Modify |
| `internal/admin/handle_orders.go` | Admin subscription usage handler | Modify |
| `internal/admin/handle_plans.go` | Plan creation/update validation | Modify |
| `internal/types/plan_test.go` | ToPolicy unit test | Create |

---

### Task 1: Add AnchorTime to CreditRule and update ToPolicy

**Files:**
- Modify: `internal/types/policy.go:52-58`
- Modify: `internal/types/plan.go:24-36`

- [ ] **Step 1: Add `WindowTypeFixed` constant and `AnchorTime` field to CreditRule**

In `internal/types/policy.go`, add the constant and field:

```go
// Window type constants.
const (
	WindowTypeSliding  = "sliding"
	WindowTypeCalendar = "calendar"
	WindowTypeFixed    = "fixed"
)
```

Add `AnchorTime` to `CreditRule`:

```go
type CreditRule struct {
	Window     string     `json:"window"`
	WindowType string     `json:"window_type"`
	MaxCredits int64      `json:"max_credits"`
	Scope      string     `json:"scope,omitempty"`
	AnchorTime *time.Time `json:"anchor_time,omitempty"`
}
```

- [ ] **Step 2: Update `ToPolicy` to accept `subscriptionStartsAt` and inject AnchorTime**

Replace the entire `ToPolicy` method in `internal/types/plan.go`:

```go
func (p *Plan) ToPolicy(projectID string, subscriptionStartsAt *time.Time) *RateLimitPolicy {
	rules := make([]CreditRule, len(p.CreditRules))
	copy(rules, p.CreditRules)
	if subscriptionStartsAt != nil {
		for i := range rules {
			if rules[i].WindowType == WindowTypeFixed {
				rules[i].AnchorTime = subscriptionStartsAt
			}
		}
	}
	return &RateLimitPolicy{
		ID:               "plan:" + p.ID,
		ProjectID:        projectID,
		Name:             p.Name,
		CreditRules:      rules,
		ModelCreditRates: p.ModelCreditRates,
		ClassicRules:     p.ClassicRules,
	}
}
```

- [ ] **Step 3: Fix the one call site of `ToPolicy`**

In `internal/proxy/auth_middleware.go:126`, change:
```go
policy = plan.ToPolicy(project.ID)
```
to:
```go
policy = plan.ToPolicy(project.ID, &subscription.StartsAt)
```

- [ ] **Step 4: Verify compilation**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: compiles with no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/types/policy.go internal/types/plan.go internal/proxy/auth_middleware.go
git commit -m "feat(ratelimit): add AnchorTime to CreditRule and update ToPolicy signature"
```

---

### Task 2: Refactor window functions into testable `*At` variants and add fixed branch

**Files:**
- Modify: `internal/ratelimit/composite.go`

- [ ] **Step 1: Export `parseDurationStr` as `ParseDurationStr`**

In `internal/ratelimit/composite.go`, rename `parseDurationStr` to `ParseDurationStr` (uppercase P). Update all in-file references (lines 104, 128, 132).

- [ ] **Step 2: Create `WindowStartTimeAt` with fixed branch, make `WindowStartTime` a wrapper**

Replace the existing `WindowStartTime` function with:

```go
// WindowStartTimeAt is the testable core that accepts an explicit now.
func WindowStartTimeAt(now time.Time, window, windowType string, anchorTime *time.Time) time.Time {
	if windowType == types.WindowTypeCalendar {
		switch window {
		case "1w":
			weekday := now.Weekday()
			if weekday == 0 {
				weekday = 7
			}
			start := now.AddDate(0, 0, -int(weekday-1))
			return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		case "1M":
			return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
	}

	if windowType == types.WindowTypeFixed {
		if anchorTime == nil {
			d := ParseDurationStr(window)
			return now.Add(-d)
		}
		anchor := anchorTime.UTC()
		d := ParseDurationStr(window)
		elapsed := now.Sub(anchor)
		if elapsed < 0 {
			return anchor
		}
		windowIndex := int64(elapsed / d)
		return anchor.Add(time.Duration(windowIndex) * d)
	}

	// Default: sliding window.
	d := ParseDurationStr(window)
	return now.Add(-d)
}

// WindowStartTime returns the start of the current window.
func WindowStartTime(window, windowType string, anchorTime *time.Time) time.Time {
	return WindowStartTimeAt(time.Now().UTC(), window, windowType, anchorTime)
}
```

- [ ] **Step 3: Create `WindowResetDurationAt` with fixed branch, make `WindowResetDuration` a wrapper**

Replace the existing `WindowResetDuration` function with:

```go
// WindowResetDurationAt is the testable core that accepts an explicit now.
func WindowResetDurationAt(now time.Time, window, windowType string, anchorTime *time.Time) time.Duration {
	if windowType == types.WindowTypeCalendar {
		switch window {
		case "1w":
			weekday := now.Weekday()
			if weekday == 0 {
				weekday = 7
			}
			daysUntilMonday := 8 - int(weekday)
			nextMonday := time.Date(now.Year(), now.Month(), now.Day()+daysUntilMonday, 0, 0, 0, 0, time.UTC)
			return nextMonday.Sub(now)
		case "1M":
			nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			return nextMonth.Sub(now)
		}
	}

	if windowType == types.WindowTypeFixed {
		windowStart := WindowStartTimeAt(now, window, windowType, anchorTime)
		d := ParseDurationStr(window)
		windowEnd := windowStart.Add(d)
		return windowEnd.Sub(now)
	}

	// Default: sliding window.
	return ParseDurationStr(window)
}

// WindowResetDuration returns how long until the window resets.
func WindowResetDuration(window, windowType string, anchorTime *time.Time) time.Duration {
	return WindowResetDurationAt(time.Now().UTC(), window, windowType, anchorTime)
}
```

- [ ] **Step 4: Update `PreCheck` to pass `AnchorTime` and add `windowType` to cache keys**

In the `PreCheck` method, update the credit rules loop (lines 38-66):

```go
	for _, rule := range policy.CreditRules {
		windowStart := WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)
		cacheKey := fmt.Sprintf("%s|%s|%s|%s", projectID, apiKeyID, rule.Window, rule.WindowType)
		if rule.EffectiveScope() == types.CreditScopeProject {
			cacheKey = fmt.Sprintf("p:%s|%s|%s", projectID, rule.Window, rule.WindowType)
		}

		var used float64
		if cached, ok := c.cache.get(cacheKey); ok {
			used = cached
		} else {
			var err error
			if rule.EffectiveScope() == types.CreditScopeProject {
				used, err = c.store.SumCreditsInWindowByProject(projectID, windowStart)
			} else {
				used, err = c.store.SumCreditsInWindow(apiKeyID, windowStart)
			}
			if err != nil {
				c.logger.Error("credit check query failed", "error", err)
				return true, 0, nil
			}
			c.cache.set(cacheKey, used)
		}

		if used >= float64(rule.MaxCredits) {
			retryAfter := WindowResetDuration(rule.Window, rule.WindowType, rule.AnchorTime)
			return false, retryAfter, nil
		}
	}
```

- [ ] **Step 5: Verify compilation**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: compiles with no errors.

- [ ] **Step 6: Commit**

```bash
git add internal/ratelimit/composite.go
git commit -m "feat(ratelimit): refactor window functions into testable *At variants, add fixed branch"
```

---

### Task 3: Unit tests for fixed window calculation

**Files:**
- Create: `internal/ratelimit/composite_test.go`

- [ ] **Step 1: Write tests for `WindowStartTimeAt` with fixed type**

Create `internal/ratelimit/composite_test.go`:

```go
package ratelimit

import (
	"testing"
	"time"
)

func TestWindowStartTimeAt_Fixed_Normal(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 12, 14, 0, 0, 0, time.UTC) // 2.5 days into window

	got := WindowStartTimeAt(now, "7d", "fixed", &anchor)
	want := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt = %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Fixed_SecondWindow(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC) // 8.5 days in → window 2

	got := WindowStartTimeAt(now, "7d", "fixed", &anchor)
	want := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt = %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Fixed_ExactBoundary(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC) // exactly at boundary

	got := WindowStartTimeAt(now, "7d", "fixed", &anchor)
	want := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC) // start of window 2
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt = %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Fixed_AnchorInFuture(t *testing.T) {
	anchor := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 18, 0, 0, 0, 0, time.UTC) // before anchor

	got := WindowStartTimeAt(now, "7d", "fixed", &anchor)
	want := anchor
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt = %v, want %v (anchor)", got, want)
	}
}

func TestWindowStartTimeAt_Fixed_NilAnchor(t *testing.T) {
	now := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)

	got := WindowStartTimeAt(now, "7d", "fixed", nil)
	want := now.Add(-7 * 24 * time.Hour) // fallback to sliding
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt nil anchor = %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Fixed_HourInterval(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)
	now := time.Date(2026, 3, 10, 19, 15, 0, 0, time.UTC) // 4h45m in, window=5h

	got := WindowStartTimeAt(now, "5h", "fixed", &anchor)
	want := time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC) // still first window
	if !got.Equal(want) {
		t.Errorf("WindowStartTimeAt = %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Sliding_Unchanged(t *testing.T) {
	now := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)

	got := WindowStartTimeAt(now, "5h", "sliding", nil)
	want := now.Add(-5 * time.Hour)
	if !got.Equal(want) {
		t.Errorf("sliding unchanged: got %v, want %v", got, want)
	}
}

func TestWindowResetDurationAt_Fixed(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 12, 0, 0, 0, 0, time.UTC) // 2 days into 7d window

	got := WindowResetDurationAt(now, "7d", "fixed", &anchor)
	want := 5 * 24 * time.Hour // 5 days left
	if got != want {
		t.Errorf("WindowResetDurationAt = %v, want %v", got, want)
	}
}

func TestWindowResetDurationAt_Fixed_AtBoundary(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC) // exactly at boundary

	got := WindowResetDurationAt(now, "7d", "fixed", &anchor)
	want := 7 * 24 * time.Hour // full new window ahead
	if got != want {
		t.Errorf("WindowResetDurationAt at boundary = %v, want %v", got, want)
	}
}

func TestWindowResetDurationAt_Sliding_Unchanged(t *testing.T) {
	now := time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC)

	got := WindowResetDurationAt(now, "5h", "sliding", nil)
	want := 5 * time.Hour
	if got != want {
		t.Errorf("sliding unchanged: got %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Calendar_Weekly(t *testing.T) {
	// Wednesday 2026-03-18 → Monday 2026-03-16
	now := time.Date(2026, 3, 18, 15, 0, 0, 0, time.UTC)

	got := WindowStartTimeAt(now, "1w", "calendar", nil)
	want := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC) // Monday
	if !got.Equal(want) {
		t.Errorf("calendar 1w: got %v, want %v", got, want)
	}
}

func TestWindowStartTimeAt_Calendar_Monthly(t *testing.T) {
	now := time.Date(2026, 3, 18, 15, 0, 0, 0, time.UTC)

	got := WindowStartTimeAt(now, "1M", "calendar", nil)
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("calendar 1M: got %v, want %v", got, want)
	}
}

func TestParseDurationStr(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"5h", 5 * time.Hour},
		{"1d", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"2w", 2 * 7 * 24 * time.Hour},
		{"30m", 30 * time.Minute},
		{"1M", time.Hour}, // month strings are not parseable, fallback to 1h — this is why validation must reject "M" suffix for fixed rules
	}
	for _, tt := range tests {
		got := ParseDurationStr(tt.input)
		if got != tt.want {
			t.Errorf("ParseDurationStr(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `cd /root/coding/modelserver && go test ./internal/ratelimit/ -v -run 'TestWindow|TestParse'`
Expected: all tests PASS.

- [ ] **Step 3: Write `ToPolicy` test**

Create `internal/types/plan_test.go`:

```go
package types

import (
	"testing"
	"time"
)

func TestToPolicy_InjectsAnchorTimeForFixedRules(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	plan := &Plan{
		ID:   "plan-1",
		Name: "test",
		CreditRules: []CreditRule{
			{Window: "7d", WindowType: WindowTypeFixed, MaxCredits: 500000},
			{Window: "5h", WindowType: WindowTypeSliding, MaxCredits: 50000},
		},
	}

	policy := plan.ToPolicy("proj-1", &anchor)

	// Fixed rule should have AnchorTime set.
	if policy.CreditRules[0].AnchorTime == nil {
		t.Fatal("fixed rule AnchorTime should not be nil")
	}
	if !policy.CreditRules[0].AnchorTime.Equal(anchor) {
		t.Errorf("fixed rule AnchorTime = %v, want %v", *policy.CreditRules[0].AnchorTime, anchor)
	}

	// Sliding rule should have AnchorTime nil.
	if policy.CreditRules[1].AnchorTime != nil {
		t.Errorf("sliding rule AnchorTime should be nil, got %v", *policy.CreditRules[1].AnchorTime)
	}

	// Original plan's CreditRules should NOT be mutated.
	if plan.CreditRules[0].AnchorTime != nil {
		t.Error("original plan CreditRules was mutated — ToPolicy must copy the slice")
	}
}

func TestToPolicy_NilStartsAt(t *testing.T) {
	plan := &Plan{
		ID:   "plan-2",
		Name: "test",
		CreditRules: []CreditRule{
			{Window: "7d", WindowType: WindowTypeFixed, MaxCredits: 500000},
		},
	}

	policy := plan.ToPolicy("proj-1", nil)

	if policy.CreditRules[0].AnchorTime != nil {
		t.Errorf("AnchorTime should be nil when subscriptionStartsAt is nil")
	}
}
```

- [ ] **Step 4: Run all tests**

Run: `cd /root/coding/modelserver && go test ./internal/ratelimit/ ./internal/types/ -v -run 'TestWindow|TestParse|TestToPolicy'`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ratelimit/composite_test.go internal/types/plan_test.go
git commit -m "test(ratelimit): add unit tests for fixed window calculation, ParseDurationStr, and ToPolicy"
```

---

### Task 4: Update usage handler to delegate to ratelimit package

**Files:**
- Modify: `internal/proxy/usage_handler.go`

- [ ] **Step 1: Add ratelimit import and rewrite `computeWindowStart` to delegate**

In `internal/proxy/usage_handler.go`, add `"github.com/modelserver/modelserver/internal/ratelimit"` to imports.

Replace `computeWindowStart`:

```go
func computeWindowStart(now time.Time, window, windowType string, anchorTime *time.Time) time.Time {
	return ratelimit.WindowStartTimeAt(now, window, windowType, anchorTime)
}
```

- [ ] **Step 2: Add fixed branch to `computeWindowEnd`**

Replace `computeWindowEnd`:

```go
func computeWindowEnd(windowStart time.Time, window, windowType string) time.Time {
	if windowType == types.WindowTypeCalendar {
		switch window {
		case "1w":
			return windowStart.AddDate(0, 0, 7)
		case "1M":
			return windowStart.AddDate(0, 1, 0)
		}
	}
	if windowType == types.WindowTypeFixed {
		return windowStart.Add(ratelimit.ParseDurationStr(window))
	}
	// Sliding window: end is now.
	return time.Now().UTC()
}
```

- [ ] **Step 3: Update `computeCreditProgress` to pass `AnchorTime`**

Change line 104 from:
```go
windowStart := computeWindowStart(now, rule.Window, rule.WindowType)
```
to:
```go
windowStart := computeWindowStart(now, rule.Window, rule.WindowType, rule.AnchorTime)
```

- [ ] **Step 4: Remove the local `parseDuration` function**

Delete the `parseDuration` function (lines 178-195). It is no longer needed since `computeWindowStart` now delegates to the ratelimit package.

- [ ] **Step 5: Verify compilation**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: compiles with no errors.

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/usage_handler.go
git commit -m "refactor(proxy): delegate window calculation to ratelimit package, remove duplicate parser"
```

---

### Task 5: Update admin handler to use ToPolicy and support fixed windows

**Files:**
- Modify: `internal/admin/handle_orders.go:226-274`

- [ ] **Step 1: Rewrite `handleSubscriptionUsage` to use `plan.ToPolicy()`**

Replace the function body from line 246 onward (after `plan` is loaded) with:

```go
		policy := plan.ToPolicy(projectID, &activeSub.StartsAt)

		statuses := make([]ratelimit.CreditWindowStatus, 0, len(policy.CreditRules))
		for _, rule := range policy.CreditRules {
			windowStart := ratelimit.WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)
			used, err := st.SumCreditsInWindowByProject(projectID, windowStart)
			if err != nil {
				used = 0
			}
			percentage := 0.0
			if rule.MaxCredits > 0 {
				percentage = (used / float64(rule.MaxCredits)) * 100
				if percentage > 100 {
					percentage = 100
				}
			}
			s := ratelimit.CreditWindowStatus{
				Window:     rule.Window,
				Percentage: percentage,
			}
			if rule.WindowType == types.WindowTypeCalendar || rule.WindowType == types.WindowTypeFixed {
				resetDur := ratelimit.WindowResetDuration(rule.Window, rule.WindowType, rule.AnchorTime)
				s.ResetsAt = time.Now().UTC().Add(resetDur).Format(time.RFC3339)
			}
			statuses = append(statuses, s)
		}

		writeData(w, http.StatusOK, statuses)
```

Note: `types` import is already present (via `types.Order*` references earlier in the file). The `types.WindowTypeCalendar`/`types.WindowTypeFixed` constants come from `internal/types/policy.go`.

- [ ] **Step 2: Verify compilation**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: compiles with no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/admin/handle_orders.go
git commit -m "refactor(admin): use ToPolicy() in subscription usage handler, support fixed windows"
```

---

### Task 6: Add validation to reject month-based windows for fixed rules

**Files:**
- Modify: `internal/admin/handle_plans.go:23-69` (handleCreatePlan)
- Modify: `internal/admin/handle_plans.go:83-123` (handleUpdatePlan)

- [ ] **Step 1: Add validation helper**

At the bottom of `internal/admin/handle_plans.go` (before the closing of the file), add:

```go
// validateCreditRules checks for invalid CreditRule configurations.
func validateCreditRules(rules []types.CreditRule) error {
	for _, rule := range rules {
		if rule.WindowType == types.WindowTypeFixed && len(rule.Window) > 0 && rule.Window[len(rule.Window)-1] == 'M' {
			return fmt.Errorf("month-based window %q is not supported with window_type \"fixed\" — use duration-based intervals like \"7d\"", rule.Window)
		}
	}
	return nil
}
```

Add `"fmt"` to the imports if not already present.

- [ ] **Step 2: Add validation in `handleCreatePlan`**

In `handleCreatePlan`, after the existing validation block (line 42-48, after `if body.PeriodMonths <= 0`), add:

```go
	if err := validateCreditRules(body.CreditRules); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
```

- [ ] **Step 3: Add validation in `handleUpdatePlan`**

In `handleUpdatePlan`, after the JSON fields marshaling loop (line 100-108), before the `if len(updates) == 0` check, add:

```go
	// Validate credit_rules if present.
	if raw, ok := body["credit_rules"]; ok {
		b, _ := json.Marshal(raw)
		var rules []types.CreditRule
		if err := json.Unmarshal(b, &rules); err == nil {
			if err := validateCreditRules(rules); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
		}
	}
```

- [ ] **Step 4: Verify compilation**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: compiles with no errors.

- [ ] **Step 5: Commit**

```bash
git add internal/admin/handle_plans.go
git commit -m "feat(admin): validate that fixed window type cannot use month-based window strings"
```

---

### Task 7: Run full test suite

**Files:** None (verification only)

- [ ] **Step 1: Run all tests**

Run: `cd /root/coding/modelserver && go test ./... 2>&1 | tail -30`
Expected: all tests PASS. Pay attention to `internal/ratelimit/` and `internal/proxy/` packages.

- [ ] **Step 2: Run the new tests specifically**

Run: `cd /root/coding/modelserver && go test ./internal/ratelimit/ ./internal/types/ -v -run 'TestWindow|TestParse|TestToPolicy'`
Expected: all tests PASS (13 ratelimit tests + 2 ToPolicy tests).
