# Per-Model Session Affinity

**Date:** 2026-04-17
**Status:** Approved, pending implementation

## Problem

`Router.sessionMap` keys upstream affinity by `sessionID` alone. Every request
in a session — regardless of which model it asks for — pins to the single
upstream that the session first landed on.

This is wider than needed. Two requests in the same session asking for
different models do not need to share an upstream; pinning them together
just concentrates load on whichever upstream the first request happened to
pick, and prevents the balancer from spreading per-model traffic.

## Goal

Pin an upstream per `(sessionID, model)` pair, not per `sessionID`. Same
session + same model → same upstream (current behavior, just at finer
granularity). Same session + different model → independent affinity, free
to land on different upstreams.

## Non-Goals

- No change to `sessionTTL` semantics or cleanup interval.
- No change to the persistence story (`sessionMap` remains in-memory and
  is cleared on restart — nothing to migrate).
- No new configuration knobs or metrics.
- No change to the count_tokens routing path's behavior.
- The parallel work in `.claude/worktrees/routing-redesign/` is out of
  scope; do not touch that worktree.

## Design

### Affinity key

```go
// sessionKey is the affinity bucket: same (sessionID, model) pin to the
// same upstream.
type sessionKey struct {
    sessionID string
    model     string
}
```

`sessionKey` is a value type (comparable, hashable) and is used directly as
the `sync.Map` key. This is preferred over a `sessionID|model` concatenation
because:

- It is type-safe — no risk that an unusual character in a model name
  collides with the separator.
- The intent ("two-axis key") is visible in the type.

`sessionBinding` (`{upstreamID, usedAt}`) is unchanged. TTL semantics are
unchanged: each entry is refreshed on use, evicted by the cleanup goroutine
once `time.Since(usedAt) > sessionTTL`.

### Router API

```go
func (r *Router) SelectWithRetry(
    ctx context.Context,
    group *resolvedGroup,
    sessionID, model string,
) []*SelectedUpstream

func (r *Router) BindSession(sessionID, model, upstreamID string)

// internal
func (r *Router) pinSessionToUpstream(
    key sessionKey,
    candidates []lb.CandidateInfo,
    balancer *lb.Balancer,
) *types.Upstream
```

`pinSessionToUpstream` takes the composed `sessionKey` rather than the two
strings, so the call site in `SelectWithRetry` does the composition once.

### Affinity short-circuit

Both `SelectWithRetry` and `BindSession` skip the affinity path (no read,
no write) whenever **either** `sessionID == ""` **or** `model == ""`.

Today only `sessionID == ""` short-circuits in either function. Broadening
to "either empty" means:

- `handler.go:312` (count_tokens) keeps working unchanged — it already
  passes empty `sessionID`.
- A future caller that supplies `sessionID` but happens to have an empty
  `model` cannot write a meaningless `(sess, "")` binding.

`BindSession` therefore changes from `if sessionID == "" { return }` to
`if sessionID == "" || model == "" { return }`.

### Cleanup goroutine

`StartSessionCleanup` is unchanged in shape. The only edit is the type
assertion inside the `Range` callback: the `key any` is now a `sessionKey`,
not a `string`. The eviction predicate (`time.Since(usedAt) > sessionTTL`)
is identical.

### Comments to refresh

Two existing comments in `router_engine.go` reference "session" / "sessionID"
in ways that become misleading once the key is `(sessionID, model)`:

- Line 18 — `sessionBinding` doc: "tracks which upstream a session (trace)
  is pinned to" → reword to mention `(session, model)`.
- Lines 345–352 — the affinity rationale block in `SelectWithRetry`:
  "Concurrent requests with the same sessionID..." → reword to "Concurrent
  requests with the same (sessionID, model)...". The race-window argument
  is unchanged; only the key name in the prose needs updating.

### Call sites

The two production call sites in `executor.go`:

- `executor.go:106`
  - before: `e.router.SelectWithRetry(r.Context(), group, reqCtx.SessionID)`
  - after:  `e.router.SelectWithRetry(r.Context(), group, reqCtx.SessionID, reqCtx.Model)`
- `executor.go:461`
  - before: `e.router.BindSession(reqCtx.SessionID, upstream.ID)`
  - after:  `e.router.BindSession(reqCtx.SessionID, reqCtx.Model, upstream.ID)`

`reqCtx.Model` is the client-facing model name and is set before
`Execute` runs, so it is available at both call sites. We deliberately use
`reqCtx.Model` rather than `reqCtx.ActualModel` because:

- `ActualModel` is a per-attempt value (set after each upstream's
  ModelMap rewrite); it is not stable across the retry loop and is not
  set when `SelectWithRetry` runs.
- The same client-facing model should land on the same upstream
  regardless of how each upstream's ModelMap chooses to rewrite it.

The non-proxy `handler.go:312` call (count_tokens) passes `reqShape.Model`
as the new model argument. The existing empty `sessionID` still
short-circuits affinity, so behavior is unchanged — but the call site is
more honest about which model is being counted.

## Behavior summary

| Scenario | Behavior |
|----------|----------|
| Same session, same model, repeated requests | Pin to same upstream (current behavior, finer-grained key) |
| Same session, different models | Independent bindings; each model balanced separately |
| Empty sessionID | Skip affinity (unchanged) |
| Empty model with non-empty sessionID | Skip affinity (new short-circuit; no caller does this today) |
| Bound upstream becomes unavailable | Re-pick via balancer and overwrite binding (unchanged) |
| Binding older than `sessionTTL` | Re-pick via balancer and overwrite binding (unchanged) |
| Concurrent first requests, same (session, model) | Atomically converge on the LoadOrStore winner (unchanged) |

## Testing

Update existing tests in `internal/proxy/router_engine_test.go` that exercise
session affinity to thread a `model` argument through:

- `TestSelectWithRetry_ConcurrentFirstRequestsConverge` — pass the same
  model in all goroutines; the convergence guarantee must still hold.
- `TestSelectWithRetry_SessionStickyWithoutExecutor` — pass the same model
  on every call.
- `TestSelectWithRetry_NoSessionUnrestricted` — empty session, empty
  model; confirms the spread guarantee is unchanged.
- `TestSelectWithRetry_ExpiredBindingIsReplaced` — pre-seed using
  `sessionKey{sessionID, model}`; assert eviction by composite key.
- `TestSelectWithRetry_BoundUpstreamUnavailableFallsThrough` — same.

Add two new tests that explicitly cover the per-model axis:

1. `TestSelectWithRetry_DifferentModelsSameSessionDoNotShareBinding`
   - Bind `(sess, "model-A") → upstream X` via a single call.
   - Loop many calls with `(sess, "model-B")`. Assert the set of selected
     upstreams contains at least two distinct IDs (i.e. model B is not
     dragged onto X by model A's binding).

2. `TestSelectWithRetry_PerModelBindingsCoexist`
   - Establish `(sess, "model-A") → X` and `(sess, "model-B") → Y` via
     two separate calls.
   - Repeat calls for each: model A still returns X; model B still returns
     Y. Demonstrates the two bindings live independently.

## Risks

- **Memory growth.** `sessionMap` size now scales with
  `(active sessions × distinct models per session)`. In practice each
  session uses 1–3 models; the cleanup goroutine's TTL eviction handles
  the bound. No mitigation needed.
- **API signature change.** `SelectWithRetry` and `BindSession` are
  exported. The only callers are inside this package — no external
  consumers — so this is a free-edit.

## Out of scope

- No change to ModelMap, balancer policies, circuit breaker, or health
  checks.
- No change to the trace/session-ID extraction in `trace_middleware.go`.
- No new admin endpoints or visibility into the affinity map.
