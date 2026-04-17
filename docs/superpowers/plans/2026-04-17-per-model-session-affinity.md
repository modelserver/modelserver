# Per-Model Session Affinity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Pin upstream affinity per `(sessionID, model)` instead of per `sessionID`, so different models in the same session can land on different upstreams.

**Architecture:** Replace `Router.sessionMap`'s string key with a `sessionKey{sessionID, model}` struct. Thread `model` through `SelectWithRetry` and `BindSession`. Short-circuit affinity when either component is empty.

**Tech Stack:** Go 1.x, `sync.Map`, standard library only.

**Spec:** `docs/superpowers/specs/2026-04-17-per-model-session-affinity-design.md`

---

## File Structure

No new files. All edits land in:

- `internal/proxy/router_engine.go` — affinity types, signatures, body changes, comments
- `internal/proxy/router_engine_test.go` — update 5 existing tests, add 2 new
- `internal/proxy/executor.go` — 2 call sites
- `internal/proxy/handler.go` — 1 call site (count_tokens)

The plan is split into two TDD-shaped tasks:

1. **Task 1 — Plumb `model` through public APIs (no behavior change).** Adds the `model` parameter to `SelectWithRetry` and `BindSession`, ignores it inside, updates all callers and existing tests. Code compiles, all existing tests still pass. This isolates the "API shape change" so the next task can be a clean behavior change.

2. **Task 2 — Switch to per-`(session, model)` keying.** Add the failing per-model regression tests first; then introduce `sessionKey`, change the map key type, refresh the comments. Tests go red → green.

---

## Task 1: Plumb `model` through router APIs

**Files:**
- Modify: `internal/proxy/router_engine.go` — `SelectWithRetry` line 262, `BindSession` line 462
- Modify: `internal/proxy/executor.go:106, :461` — call sites
- Modify: `internal/proxy/handler.go:312` — count_tokens call site
- Modify: `internal/proxy/router_engine_test.go` — five existing tests' call sites

### Step 1: Add `model` parameter to `SelectWithRetry`

- [ ] Edit `internal/proxy/router_engine.go`. Change the function signature on line 262:

```go
// Before:
func (r *Router) SelectWithRetry(ctx context.Context, group *resolvedGroup, sessionID string) []*SelectedUpstream {

// After:
func (r *Router) SelectWithRetry(ctx context.Context, group *resolvedGroup, sessionID, model string) []*SelectedUpstream {
```

The body is unchanged in this task. `model` is accepted but unused — Go will not warn for unused function parameters.

### Step 2: Add `model` parameter to `BindSession`

- [ ] Edit `internal/proxy/router_engine.go`. Change the function on line 462:

```go
// Before:
func (r *Router) BindSession(sessionID, upstreamID string) {
	if sessionID == "" {
		return
	}
	r.sessionMap.Store(sessionID, sessionBinding{upstreamID: upstreamID, usedAt: time.Now()})
}

// After:
func (r *Router) BindSession(sessionID, model, upstreamID string) {
	if sessionID == "" {
		return
	}
	r.sessionMap.Store(sessionID, sessionBinding{upstreamID: upstreamID, usedAt: time.Now()})
}
```

The short-circuit and body are unchanged in this task.

### Step 3: Update `executor.go` call sites

- [ ] Edit `internal/proxy/executor.go` line 106:

```go
// Before:
candidates := e.router.SelectWithRetry(r.Context(), group, reqCtx.SessionID)

// After:
candidates := e.router.SelectWithRetry(r.Context(), group, reqCtx.SessionID, reqCtx.Model)
```

- [ ] Edit `internal/proxy/executor.go` line 461:

```go
// Before:
e.router.BindSession(reqCtx.SessionID, upstream.ID)

// After:
e.router.BindSession(reqCtx.SessionID, reqCtx.Model, upstream.ID)
```

### Step 4: Update `handler.go` count_tokens call site

- [ ] Edit `internal/proxy/handler.go` line 312:

```go
// Before:
candidates := h.router.SelectWithRetry(r.Context(), group, "")

// After:
candidates := h.router.SelectWithRetry(r.Context(), group, "", reqShape.Model)
```

(Empty `sessionID` continues to short-circuit affinity. Passing the real model value rather than `""` keeps the call site honest about which model is being counted.)

### Step 5: Update existing tests in `router_engine_test.go`

- [ ] Edit `internal/proxy/router_engine_test.go`. The five existing call sites all use the model `"claude-sonnet"` (already the test fixture's only supported model). Pass it through:

In `TestSelectWithRetry_ConcurrentFirstRequestsConverge` (~line 86):
```go
// Before:
sel := r.SelectWithRetry(context.Background(), g, sessionID)

// After:
sel := r.SelectWithRetry(context.Background(), g, sessionID, "claude-sonnet")
```

In `TestSelectWithRetry_SessionStickyWithoutExecutor` (~lines 118 and 125):
```go
// Before:
first := r.SelectWithRetry(context.Background(), g, "sess-seq")
// ...
got := r.SelectWithRetry(context.Background(), g, "sess-seq")

// After:
first := r.SelectWithRetry(context.Background(), g, "sess-seq", "claude-sonnet")
// ...
got := r.SelectWithRetry(context.Background(), g, "sess-seq", "claude-sonnet")
```

In `TestSelectWithRetry_NoSessionUnrestricted` (~line 144):
```go
// Before:
sel := r.SelectWithRetry(context.Background(), g, "")

// After:
sel := r.SelectWithRetry(context.Background(), g, "", "")
```

In `TestSelectWithRetry_ExpiredBindingIsReplaced` (~line 166):
```go
// Before:
sel := r.SelectWithRetry(context.Background(), g, "sess-expired")

// After:
sel := r.SelectWithRetry(context.Background(), g, "sess-expired", "claude-sonnet")
```

In `TestSelectWithRetry_BoundUpstreamUnavailableFallsThrough` (~line 220):
```go
// Before:
sel := r.SelectWithRetry(context.Background(), g, "sess-stale")

// After:
sel := r.SelectWithRetry(context.Background(), g, "sess-stale", "claude-sonnet")
```

(The two direct `r.sessionMap.Store(...)` lines in these tests still use the string key `"sess-expired"` / `"sess-stale"` in this task. They will be updated in Task 2 when the key type changes.)

### Step 6: Build and run package tests

- [ ] Run `cd /root/coding/modelserver && go build ./...`

Expected: clean build (no compile errors).

- [ ] Run `cd /root/coding/modelserver && go test ./internal/proxy/...`

Expected: all tests pass. Behavior is unchanged in this task — `model` is plumbed but not used in the router.

### Step 7: Commit

- [ ] Run:

```bash
cd /root/coding/modelserver
git add internal/proxy/router_engine.go internal/proxy/router_engine_test.go internal/proxy/executor.go internal/proxy/handler.go
git commit -m "$(cat <<'EOF'
refactor: thread model through SelectWithRetry and BindSession

Mechanical refactor: add model parameter to the public router affinity
APIs. The parameter is plumbed through call sites and tests but is not
yet used inside the router; behavior is unchanged. Sets up the next
commit, which switches the affinity key to (sessionID, model).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Switch session affinity to per-`(session, model)` key

**Files:**
- Modify: `internal/proxy/router_engine.go` — add `sessionKey`; change `pinSessionToUpstream`, `BindSession`, `SelectWithRetry` body; refresh comments
- Modify: `internal/proxy/router_engine_test.go` — update two `Store`/`Load` call pairs in existing tests; add two new tests

### Step 1: Add the failing per-model tests

- [ ] Edit `internal/proxy/router_engine_test.go`. Append two new tests at the end of the file:

```go
// TestSelectWithRetry_PerModelBindingsCoexist verifies that two distinct
// models inside the same session establish independent bindings, each
// stable across repeated calls. This is the per-model analogue of
// TestSelectWithRetry_SessionStickyWithoutExecutor.
func TestSelectWithRetry_PerModelBindingsCoexist(t *testing.T) {
	r, g := newTestRouterForSession(t)
	sessID := "sess-pair"

	a := r.SelectWithRetry(context.Background(), g, sessID, "model-A")
	if len(a) == 0 {
		t.Fatal("no candidates for model-A")
	}
	pinnedA := a[0].Upstream.ID

	b := r.SelectWithRetry(context.Background(), g, sessID, "model-B")
	if len(b) == 0 {
		t.Fatal("no candidates for model-B")
	}
	pinnedB := b[0].Upstream.ID

	for i := 0; i < 20; i++ {
		got := r.SelectWithRetry(context.Background(), g, sessID, "model-A")
		if len(got) == 0 || got[0].Upstream.ID != pinnedA {
			t.Fatalf("iter %d: model-A pin drifted to %v", i, got)
		}
		got = r.SelectWithRetry(context.Background(), g, sessID, "model-B")
		if len(got) == 0 || got[0].Upstream.ID != pinnedB {
			t.Fatalf("iter %d: model-B pin drifted to %v", i, got)
		}
	}
}

// TestSelectWithRetry_DifferentModelsSameSessionDoNotShareBinding asserts
// that a (session, model-A) pin does NOT drag (session, model-B) onto the
// same upstream. With keying by sessionID alone, model-B would always
// inherit model-A's binding and the two would be 100% identical across
// every trial. With per-(session, model) keying, the balancer is free to
// pick independently for each model.
func TestSelectWithRetry_DifferentModelsSameSessionDoNotShareBinding(t *testing.T) {
	const trials = 200
	differCount := 0
	for i := 0; i < trials; i++ {
		r, g := newTestRouterForSession(t)
		sessID := "sess-cross"

		a := r.SelectWithRetry(context.Background(), g, sessID, "model-A")
		b := r.SelectWithRetry(context.Background(), g, sessID, "model-B")
		if len(a) == 0 || len(b) == 0 {
			t.Fatalf("iter %d: no candidates", i)
		}
		if a[0].Upstream.ID != b[0].Upstream.ID {
			differCount++
		}
	}
	// With three equal-weight upstreams, expected ~67% trials differ. With
	// the buggy "shared binding" semantics, differCount is exactly 0.
	if differCount == 0 {
		t.Fatalf("expected model-A and model-B to differ in some trials; "+
			"got 0/%d (suggests shared-binding regression)", trials)
	}
}
```

### Step 2: Run the new tests; observe expected outcomes

- [ ] Run:

```bash
cd /root/coding/modelserver
go test ./internal/proxy/ -run "TestSelectWithRetry_PerModelBindingsCoexist|TestSelectWithRetry_DifferentModelsSameSessionDoNotShareBinding" -v
```

Expected:

- `TestSelectWithRetry_DifferentModelsSameSessionDoNotShareBinding` **FAILS** with `differCount == 0`. After Task 1, `model` is ignored inside the router; the affinity key is still `sessionID` alone, so model-B always inherits model-A's binding and the two never differ. This is the load-bearing failing test that Task 2 must turn green.
- `TestSelectWithRetry_PerModelBindingsCoexist` **may pass** at this stage. Its assertion ("each model's pin is stable across repeated calls") happens to hold even when both pins point at the same upstream. It serves as positive regression coverage for after Task 2, when each model is free to land on its own upstream.

If `Differ` passes at this stage, halt and investigate — per-model behavior should not yet exist.

### Step 3: Add `sessionKey` type and refresh `sessionBinding` doc

- [ ] Edit `internal/proxy/router_engine.go`. Replace the `sessionBinding` block (lines 18–22) with:

```go
// sessionBinding tracks which upstream a (session, model) pair is pinned to.
type sessionBinding struct {
	upstreamID string
	usedAt     time.Time
}

// sessionKey buckets affinity by (sessionID, model): same pair pins to the
// same upstream. Using a struct (rather than a concatenated string) is
// type-safe and keeps the two-axis intent visible.
type sessionKey struct {
	sessionID string
	model     string
}
```

### Step 4: Update the `sessionMap` doc on the Router

- [ ] Edit `internal/proxy/router_engine.go` line 48:

```go
// Before:
sessionMap sync.Map // sessionID -> sessionBinding

// After:
sessionMap sync.Map // sessionKey -> sessionBinding
```

### Step 5: Switch `pinSessionToUpstream` to take a `sessionKey`

- [ ] Edit `internal/proxy/router_engine.go`. Replace the function (lines 373–432) with:

```go
// pinSessionToUpstream resolves the (session, model) key to a primary
// upstream from candidates, establishing or refreshing the binding as a
// side effect. It handles three cases atomically against concurrent
// callers:
//
//  1. Valid binding pointing at an available upstream — use it, refresh
//     the timestamp.
//  2. No binding, or binding stale/expired/pointing at an unavailable
//     upstream — pick via balancer and install the binding via LoadOrStore.
//     If another goroutine wrote first, follow the winner (provided the
//     winner's upstream is available); otherwise our pick wins and overwrites.
//
// Returns nil only when the balancer cannot produce a primary.
func (r *Router) pinSessionToUpstream(key sessionKey, candidates []lb.CandidateInfo, balancer *lb.Balancer) *types.Upstream {
	findInCandidates := func(id string) *types.Upstream {
		for _, c := range candidates {
			if c.Upstream.ID == id {
				return c.Upstream
			}
		}
		return nil
	}

	// Fast path: existing, fresh binding to an available upstream.
	if val, ok := r.sessionMap.Load(key); ok {
		binding := val.(sessionBinding)
		if time.Since(binding.usedAt) < r.sessionTTL {
			if u := findInCandidates(binding.upstreamID); u != nil {
				r.sessionMap.Store(key, sessionBinding{
					upstreamID: u.ID,
					usedAt:     time.Now(),
				})
				return u
			}
		}
	}

	// Slow path: pick a fresh primary and try to claim the (session, model).
	primary := balancer.Select(candidates)
	if primary == nil {
		return nil
	}
	newBinding := sessionBinding{upstreamID: primary.ID, usedAt: time.Now()}

	actual, loaded := r.sessionMap.LoadOrStore(key, newBinding)
	if !loaded {
		// We won the race for this previously-unbound (session, model).
		return primary
	}

	// Someone else's binding is already present. Use it only if still usable;
	// otherwise our pick wins and we overwrite the stale/expired binding.
	existing := actual.(sessionBinding)
	if time.Since(existing.usedAt) < r.sessionTTL {
		if u := findInCandidates(existing.upstreamID); u != nil {
			return u
		}
	}
	r.sessionMap.Store(key, newBinding)
	return primary
}
```

### Step 6: Update `SelectWithRetry`'s affinity branch + comment

- [ ] Edit `internal/proxy/router_engine.go`. Replace the affinity block in `SelectWithRetry` (lines 345–359) with:

```go
	// 7. Session affinity. Concurrent requests with the same (sessionID, model)
	//    must converge on the same primary upstream. Writing the binding only
	//    after the upstream responds (as the executor does via BindSession)
	//    leaves a multi-second window in which parallel requests all observe
	//    "no binding" and independently balance-pick — silently splitting one
	//    (session, model) across multiple upstreams. Claim the binding here,
	//    atomically, so that losers in the concurrent race read and follow
	//    the winner.
	if sessionID != "" && model != "" {
		key := sessionKey{sessionID: sessionID, model: model}
		if primary := r.pinSessionToUpstream(key, candidates, balancer); primary != nil {
			return r.resultWithPrimary(primary, candidates, balancer, n)
		}
		// pinSessionToUpstream returns nil only when the balancer can produce
		// no primary (e.g. all candidate weights collapse to zero). Fall
		// through to plain ranking below so the caller still gets a result.
	}
```

(The "8. No session affinity" block immediately below is unchanged.)

### Step 7: Update `BindSession` body

- [ ] Edit `internal/proxy/router_engine.go`. Replace `BindSession` (lines 461–467) with:

```go
// BindSession stores a (session, model)-to-upstream binding for stickiness.
func (r *Router) BindSession(sessionID, model, upstreamID string) {
	if sessionID == "" || model == "" {
		return
	}
	r.sessionMap.Store(sessionKey{sessionID: sessionID, model: model}, sessionBinding{upstreamID: upstreamID, usedAt: time.Now()})
}
```

### Step 8: Update the two direct `sessionMap.Store`/`Load` calls in existing tests

- [ ] Edit `internal/proxy/router_engine_test.go`.

In `TestSelectWithRetry_ExpiredBindingIsReplaced` (around lines 161 and 172):
```go
// Before:
r.sessionMap.Store("sess-expired", sessionBinding{...})
// ...
val, ok := r.sessionMap.Load("sess-expired")

// After:
r.sessionMap.Store(sessionKey{sessionID: "sess-expired", model: "claude-sonnet"}, sessionBinding{...})
// ...
val, ok := r.sessionMap.Load(sessionKey{sessionID: "sess-expired", model: "claude-sonnet"})
```

In `TestSelectWithRetry_BoundUpstreamUnavailableFallsThrough` (around lines 215 and 227):
```go
// Before:
r.sessionMap.Store("sess-stale", sessionBinding{...})
// ...
val, ok := r.sessionMap.Load("sess-stale")

// After:
r.sessionMap.Store(sessionKey{sessionID: "sess-stale", model: "claude-sonnet"}, sessionBinding{...})
// ...
val, ok := r.sessionMap.Load(sessionKey{sessionID: "sess-stale", model: "claude-sonnet"})
```

(The body of each test — what it asserts — does not change. Only the key type changes.)

### Step 9: Build and run all proxy tests

- [ ] Run:

```bash
cd /root/coding/modelserver && go build ./...
```

Expected: clean build.

- [ ] Run:

```bash
cd /root/coding/modelserver && go test ./internal/proxy/... -v
```

Expected: all tests pass — the original five session-affinity tests, the two new per-model tests, and every other proxy test.

If `TestSelectWithRetry_DifferentModelsSameSessionDoNotShareBinding` still fails (`differCount == 0`), the per-model behavior is wrong: re-check that `pinSessionToUpstream` is using `sessionKey` as the map key (Step 5) and that `SelectWithRetry` is composing the key from `sessionID` and `model` (Step 6).

### Step 10: Commit

- [ ] Run:

```bash
cd /root/coding/modelserver
git add internal/proxy/router_engine.go internal/proxy/router_engine_test.go
git commit -m "$(cat <<'EOF'
fix: pin session affinity per (sessionID, model) instead of per sessionID

Different models in the same session no longer concentrate on whichever
upstream the first request happened to land on. The affinity map now
keys by sessionKey{sessionID, model}, and BindSession's short-circuit is
broadened to skip on either component empty.

Adds two regression tests:
- PerModelBindingsCoexist: each model's pin is stable across calls
- DifferentModelsSameSessionDoNotShareBinding: model-B is not dragged
  onto model-A's pin

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Done

After Task 2's commit, the change is complete. Verify:

- [ ] `git log --oneline -3` shows the two new commits on top of the spec commits.
- [ ] `cd /root/coding/modelserver && go test ./...` is green across the whole module.
