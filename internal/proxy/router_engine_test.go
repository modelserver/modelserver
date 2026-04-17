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
		ModelPattern:    "*",
		UpstreamGroupID: "grp",
		MatchPriority:   1,
		Status:          "active",
	}}

	r := NewRouter(upstreams, groups, routes, nil, logger, time.Hour, nil)
	g, err := r.Match("", "claude-sonnet")
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
	r.sessionMap.Store("sess-expired", sessionBinding{
		upstreamID: "up-a",
		usedAt:     time.Now().Add(-2 * time.Hour), // older than sessionTTL (1h)
	})

	sel := r.SelectWithRetry(context.Background(), g, "sess-expired", "claude-sonnet")
	if len(sel) == 0 {
		t.Fatal("no candidates")
	}
	// The primary can be any upstream, but whatever it is must now be the
	// binding — not the expired up-a (unless the balancer re-picked up-a).
	val, ok := r.sessionMap.Load("sess-expired")
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
	routes := []types.Route{{ID: "r1", ModelPattern: "*", UpstreamGroupID: "grp", MatchPriority: 1, Status: "active"}}
	r := NewRouter(upstreams, groups, routes, nil, logger, time.Hour, nil)
	g, err := r.Match("", "claude-sonnet")
	if err != nil {
		t.Fatalf("Match failed: %v", err)
	}

	// Binding points at the disabled upstream.
	r.sessionMap.Store("sess-stale", sessionBinding{
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
	val, ok := r.sessionMap.Load("sess-stale")
	if !ok {
		t.Fatal("binding was not re-established")
	}
	b := val.(sessionBinding)
	if b.upstreamID != sel[0].Upstream.ID {
		t.Fatalf("binding %q does not match new primary %q", b.upstreamID, sel[0].Upstream.ID)
	}
}
