package proxy

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// newTestRouterForSession builds a Router with three equally-weighted
// weighted_random upstreams and one permissive route, for testing session
// affinity semantics directly against SelectWithRetry.
func newTestRouterForSession(t *testing.T) (*Router, *resolvedGroup) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	upstreams := []types.Upstream{
		{ID: "up-a", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"claude-sonnet"}},
		{ID: "up-b", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"claude-sonnet"}},
		{ID: "up-c", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"claude-sonnet"}},
	}
	groups := []store.UpstreamGroupWithMembers{{
		UpstreamGroup: types.UpstreamGroup{
			ID:       "grp",
			Name:     "grp",
			LBPolicy: types.LBPolicyWeightedRandom,
			Status:   "active",
		},
		Members: []store.UpstreamGroupMemberDetail{
			{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp", UpstreamID: "up-a"}},
			{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp", UpstreamID: "up-b"}},
			{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp", UpstreamID: "up-c"}},
		},
	}}
	routes := []types.Route{{
		ID:              "r1",
		ModelNames:      []string{"claude-sonnet"},
		RequestKinds:    []string{types.KindAnthropicMessages},
		UpstreamGroupID: "grp",
		MatchPriority:   1,
		Status:          "active",
	}}

	r := NewRouter(upstreams, groups, routes, nil, logger, time.Hour, nil, nil, nil)
	g, err := r.Match("", "claude-sonnet", types.KindAnthropicMessages)
	if err != nil {
		t.Fatalf("Match failed: %v", err)
	}
	return r, g
}

// TestSelectWithRetry_ConcurrentFirstRequestsConverge is the regression test
// for the session-affinity race: two concurrent SelectWithRetry calls for the
// same sessionID must return the same primary upstream, even when no binding
// exists yet at the time either call enters the router.
//
// Without atomic binding at selection time, both goroutines see "no binding",
// both fall through to the weighted-random balancer, and independently pick
// different upstreams — silently routing one Claude Code session across two
// upstreams.
func TestSelectWithRetry_ConcurrentFirstRequestsConverge(t *testing.T) {
	// Run many trials so that, under the racy behaviour, at least one trial
	// would be expected to produce divergent picks across three upstreams.
	const (
		trials      = 50
		concurrency = 32
	)

	for trial := 0; trial < trials; trial++ {
		r, g := newTestRouterForSession(t)

		sessionID := "sess-concurrent"
		results := make([]string, concurrency)

		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				sel := r.SelectWithRetry(context.Background(), g, sessionID, "claude-sonnet")
				if len(sel) == 0 {
					t.Errorf("trial %d: goroutine %d got no candidates", trial, idx)
					return
				}
				results[idx] = sel[0].Upstream.ID
			}(i)
		}
		close(start)
		wg.Wait()

		first := results[0]
		if first == "" {
			t.Fatalf("trial %d: empty primary", trial)
		}
		for i, id := range results {
			if id != first {
				t.Fatalf("trial %d: session affinity broken — goroutine 0 picked %q, goroutine %d picked %q",
					trial, first, i, id)
			}
		}
	}
}

// TestSelectWithRetry_SessionStickyWithoutExecutor verifies that affinity is
// established by SelectWithRetry itself, not only by the executor's
// post-response BindSession. Sequential calls with the same sessionID must
// return the same primary upstream even though no executor ever records
// a success for this session.
func TestSelectWithRetry_SessionStickyWithoutExecutor(t *testing.T) {
	r, g := newTestRouterForSession(t)

	first := r.SelectWithRetry(context.Background(), g, "sess-seq", "claude-sonnet")
	if len(first) == 0 {
		t.Fatal("no candidates on first call")
	}
	want := first[0].Upstream.ID

	for i := 0; i < 20; i++ {
		got := r.SelectWithRetry(context.Background(), g, "sess-seq", "claude-sonnet")
		if len(got) == 0 {
			t.Fatalf("iter %d: no candidates", i)
		}
		if got[0].Upstream.ID != want {
			t.Fatalf("iter %d: primary drifted from %q to %q", i, want, got[0].Upstream.ID)
		}
	}
}

// TestSelectWithRetry_NoSessionUnrestricted sanity-checks that SelectWithRetry
// still uses the balancer freely when no sessionID is supplied (e.g. the
// count_tokens path). Over many calls, the balancer must exercise more than
// one upstream.
func TestSelectWithRetry_NoSessionUnrestricted(t *testing.T) {
	r, g := newTestRouterForSession(t)

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		sel := r.SelectWithRetry(context.Background(), g, "", "")
		if len(sel) == 0 {
			t.Fatalf("iter %d: no candidates", i)
		}
		seen[sel[0].Upstream.ID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected balancer to spread across multiple upstreams when no session; got only %v", seen)
	}
}

// TestSelectWithRetry_ExpiredBindingIsReplaced confirms that an expired
// binding is dropped and a fresh pick is atomically written in its place.
func TestSelectWithRetry_ExpiredBindingIsReplaced(t *testing.T) {
	r, g := newTestRouterForSession(t)

	// Pre-seed an expired binding pointing at up-a.
	r.sessionMap.Store(sessionKey{sessionID: "sess-expired", model: "claude-sonnet"}, sessionBinding{
		upstreamID: "up-a",
		usedAt:     time.Now().Add(-2 * time.Hour), // older than sessionTTL (1h)
	})

	sel := r.SelectWithRetry(context.Background(), g, "sess-expired", "claude-sonnet")
	if len(sel) == 0 {
		t.Fatal("no candidates")
	}
	// The primary can be any upstream, but whatever it is must now be the
	// binding — not the expired up-a (unless the balancer re-picked up-a).
	val, ok := r.sessionMap.Load(sessionKey{sessionID: "sess-expired", model: "claude-sonnet"})
	if !ok {
		t.Fatal("binding was not re-established")
	}
	b := val.(sessionBinding)
	if b.upstreamID != sel[0].Upstream.ID {
		t.Fatalf("binding %q does not match primary %q", b.upstreamID, sel[0].Upstream.ID)
	}
	if time.Since(b.usedAt) > time.Minute {
		t.Fatalf("binding timestamp not refreshed: %v", b.usedAt)
	}
}

// TestSelectWithRetry_BoundUpstreamUnavailableFallsThrough verifies that when
// the stored binding points to an upstream that is no longer in the candidate
// set (disabled / draining / unhealthy), SelectWithRetry picks a new upstream
// from the available candidates AND rebinds the session to that new pick —
// atomically, so concurrent callers do not diverge.
func TestSelectWithRetry_BoundUpstreamUnavailableFallsThrough(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	upstreams := []types.Upstream{
		// up-a is disabled; it must not be chosen.
		{ID: "up-a", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusDisabled, Weight: 1, SupportedModels: []string{"claude-sonnet"}},
		{ID: "up-b", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"claude-sonnet"}},
		{ID: "up-c", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"claude-sonnet"}},
	}
	groups := []store.UpstreamGroupWithMembers{{
		UpstreamGroup: types.UpstreamGroup{ID: "grp", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
		Members: []store.UpstreamGroupMemberDetail{
			{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp", UpstreamID: "up-a"}},
			{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp", UpstreamID: "up-b"}},
			{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp", UpstreamID: "up-c"}},
		},
	}}
	routes := []types.Route{{ID: "r1", ModelNames: []string{"claude-sonnet"}, RequestKinds: []string{types.KindAnthropicMessages}, UpstreamGroupID: "grp", MatchPriority: 1, Status: "active"}}
	r := NewRouter(upstreams, groups, routes, nil, logger, time.Hour, nil, nil, nil)
	g, err := r.Match("", "claude-sonnet", types.KindAnthropicMessages)
	if err != nil {
		t.Fatalf("Match failed: %v", err)
	}

	// Binding points at the disabled upstream.
	r.sessionMap.Store(sessionKey{sessionID: "sess-stale", model: "claude-sonnet"}, sessionBinding{
		upstreamID: "up-a",
		usedAt:     time.Now(),
	})

	sel := r.SelectWithRetry(context.Background(), g, "sess-stale", "claude-sonnet")
	if len(sel) == 0 {
		t.Fatal("no candidates")
	}
	if sel[0].Upstream.ID == "up-a" {
		t.Fatalf("disabled upstream was selected")
	}
	val, ok := r.sessionMap.Load(sessionKey{sessionID: "sess-stale", model: "claude-sonnet"})
	if !ok {
		t.Fatal("binding was not re-established")
	}
	b := val.(sessionBinding)
	if b.upstreamID != sel[0].Upstream.ID {
		t.Fatalf("binding %q does not match new primary %q", b.upstreamID, sel[0].Upstream.ID)
	}
}

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

// newRouterWithRoutes builds a minimal Router for testing Match's filtering
// logic. All routes share a single placeholder upstream group so the test
// only exercises route-selection, not upstream-load-balancing.
func newRouterWithRoutes(t *testing.T, routes ...*types.Route) *Router {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Build a placeholder group for every distinct UpstreamGroupID referenced.
	seen := map[string]bool{}
	var groups []store.UpstreamGroupWithMembers
	var upstreams []types.Upstream
	for _, r := range routes {
		if seen[r.UpstreamGroupID] {
			continue
		}
		seen[r.UpstreamGroupID] = true
		uid := "u-" + r.UpstreamGroupID
		upstreams = append(upstreams, types.Upstream{
			ID: uid, Provider: types.ProviderAnthropic,
			Status: types.UpstreamStatusActive, Weight: 1,
			SupportedModels: []string{"any"},
		})
		groups = append(groups, store.UpstreamGroupWithMembers{
			UpstreamGroup: types.UpstreamGroup{
				ID: r.UpstreamGroupID, Name: r.UpstreamGroupID,
				LBPolicy: types.LBPolicyWeightedRandom, Status: "active",
			},
			Members: []store.UpstreamGroupMemberDetail{
				{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: r.UpstreamGroupID, UpstreamID: uid}},
			},
		})
	}
	asValues := make([]types.Route, len(routes))
	for i, r := range routes {
		asValues[i] = *r
	}
	return NewRouter(upstreams, groups, asValues, nil, logger, time.Hour, nil, nil, nil)
}

func TestMatch_KindIsRequired_NoMatchingKindReturnsError(t *testing.T) {
	r := newRouterWithRoutes(t, &types.Route{
		ID: "r1", ProjectID: "p", ModelNames: []string{"m"},
		UpstreamGroupID: "g", RequestKinds: []string{types.KindAnthropicMessages},
		Status: "active",
	})
	if _, err := r.Match("p", "m", types.KindOpenAIResponses); err == nil {
		t.Error("expected error when kind doesn't match any route")
	}
}

func TestMatch_MultiKindRouteServesBothEndpoints(t *testing.T) {
	r := newRouterWithRoutes(t, &types.Route{
		ID: "r1", ProjectID: "p", ModelNames: []string{"m"},
		UpstreamGroupID: "g", RequestKinds: []string{
			types.KindAnthropicMessages, types.KindAnthropicCountTokens,
		},
		Status: "active",
	})
	for _, k := range []string{types.KindAnthropicMessages, types.KindAnthropicCountTokens} {
		if _, err := r.Match("p", "m", k); err != nil {
			t.Errorf("kind %s: unexpected error %v", k, err)
		}
	}
}

func TestMatch_KindMismatchSkipsRoute_FallsThroughToGlobal(t *testing.T) {
	r := newRouterWithRoutes(t,
		&types.Route{
			ID: "r_proj", ProjectID: "p", ModelNames: []string{"m"},
			UpstreamGroupID: "g_proj", RequestKinds: []string{types.KindAnthropicMessages},
			MatchPriority: 100, Status: "active",
		},
		&types.Route{
			ID: "r_global", ProjectID: "", ModelNames: []string{"m"},
			UpstreamGroupID: "g_global", RequestKinds: []string{types.KindAnthropicCountTokens},
			MatchPriority: 0, Status: "active",
		},
	)
	g, err := r.Match("p", "m", types.KindAnthropicCountTokens)
	if err != nil {
		t.Fatal(err)
	}
	if g.group.ID != "g_global" {
		t.Errorf("expected fallthrough to g_global, got %s", g.group.ID)
	}
}

func TestMatch_ProjectKindBeatsGlobalKind(t *testing.T) {
	r := newRouterWithRoutes(t,
		&types.Route{
			ID: "r_proj", ProjectID: "p", ModelNames: []string{"m"},
			UpstreamGroupID: "g_proj", RequestKinds: []string{types.KindAnthropicMessages},
			MatchPriority: 0, Status: "active",
		},
		&types.Route{
			ID: "r_global", ProjectID: "", ModelNames: []string{"m"},
			UpstreamGroupID: "g_global", RequestKinds: []string{types.KindAnthropicMessages},
			MatchPriority: 100, Status: "active",
		},
	)
	g, err := r.Match("p", "m", types.KindAnthropicMessages)
	if err != nil {
		t.Fatal(err)
	}
	if g.group.ID != "g_proj" {
		t.Errorf("project route should beat global, got %s", g.group.ID)
	}
}
