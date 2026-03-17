package lb

import (
	"math"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func makeUpstream(id string) *types.Upstream {
	return &types.Upstream{ID: id, Name: id}
}

func makeCandidates(weights ...int) []CandidateInfo {
	candidates := make([]CandidateInfo, len(weights))
	for i, w := range weights {
		candidates[i] = CandidateInfo{
			Upstream: makeUpstream(string(rune('A' + i))),
			Weight:   w,
		}
	}
	return candidates
}

func TestSelectEmptyCandidates(t *testing.T) {
	tracker := NewConnectionTracker()
	for _, policy := range []string{
		types.LBPolicyWeightedRandom,
		types.LBPolicyRoundRobin,
		types.LBPolicyLeastConn,
	} {
		b := NewBalancer(policy, tracker)
		if got := b.Select(nil); got != nil {
			t.Errorf("Select(%s) with nil candidates = %v, want nil", policy, got)
		}
		if got := b.Select([]CandidateInfo{}); got != nil {
			t.Errorf("Select(%s) with empty candidates = %v, want nil", policy, got)
		}
	}
}

func TestWeightedRandomDistribution(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer(types.LBPolicyWeightedRandom, tracker)

	// Weights: A=3, B=1, C=2 -> total=6
	// Expected proportions: A=50%, B=16.7%, C=33.3%
	candidates := makeCandidates(3, 1, 2)

	iterations := 60000
	counts := make(map[string]int)
	for i := 0; i < iterations; i++ {
		u := b.Select(candidates)
		if u == nil {
			t.Fatal("Select returned nil for non-empty candidates")
		}
		counts[u.ID]++
	}

	totalWeight := 6.0
	expectedRatios := map[string]float64{
		"A": 3.0 / totalWeight,
		"B": 1.0 / totalWeight,
		"C": 2.0 / totalWeight,
	}

	for id, expected := range expectedRatios {
		actual := float64(counts[id]) / float64(iterations)
		if math.Abs(actual-expected) > 0.03 {
			t.Errorf("weighted_random: upstream %s got ratio %.4f, expected ~%.4f (tolerance 0.03)", id, actual, expected)
		}
	}
}

func TestRoundRobinDistribution(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer(types.LBPolicyRoundRobin, tracker)
	candidates := makeCandidates(1, 1, 1)

	iterations := 300
	counts := make(map[string]int)
	for i := 0; i < iterations; i++ {
		u := b.Select(candidates)
		if u == nil {
			t.Fatal("Select returned nil for non-empty candidates")
		}
		counts[u.ID]++
	}

	// Each candidate should get exactly 1/3 of requests
	expected := iterations / len(candidates)
	for _, c := range candidates {
		got := counts[c.Upstream.ID]
		if got != expected {
			t.Errorf("round_robin: upstream %s got %d requests, want %d", c.Upstream.ID, got, expected)
		}
	}
}

func TestRoundRobinCyclesInOrder(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer(types.LBPolicyRoundRobin, tracker)
	candidates := makeCandidates(1, 1, 1)

	// First cycle should be A, B, C
	for i := 0; i < 2; i++ {
		for j, c := range candidates {
			u := b.Select(candidates)
			if u.ID != c.Upstream.ID {
				t.Errorf("round_robin cycle %d position %d: got %s, want %s", i, j, u.ID, c.Upstream.ID)
			}
		}
	}
}

func TestLeastConnSelectsFewestConnections(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer(types.LBPolicyLeastConn, tracker)

	candidates := []CandidateInfo{
		{Upstream: makeUpstream("A"), Weight: 1, ActiveConns: 5},
		{Upstream: makeUpstream("B"), Weight: 1, ActiveConns: 2},
		{Upstream: makeUpstream("C"), Weight: 1, ActiveConns: 8},
	}

	u := b.Select(candidates)
	if u.ID != "B" {
		t.Errorf("least_conn: selected %s, want B (fewest conns)", u.ID)
	}
}

func TestLeastConnTieBreakByWeight(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer(types.LBPolicyLeastConn, tracker)

	candidates := []CandidateInfo{
		{Upstream: makeUpstream("A"), Weight: 1, ActiveConns: 3},
		{Upstream: makeUpstream("B"), Weight: 5, ActiveConns: 3},
		{Upstream: makeUpstream("C"), Weight: 3, ActiveConns: 3},
	}

	u := b.Select(candidates)
	if u.ID != "B" {
		t.Errorf("least_conn tie-break: selected %s, want B (highest weight)", u.ID)
	}
}

func TestLeastConnPrefersFewConnOverWeight(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer(types.LBPolicyLeastConn, tracker)

	candidates := []CandidateInfo{
		{Upstream: makeUpstream("A"), Weight: 10, ActiveConns: 5},
		{Upstream: makeUpstream("B"), Weight: 1, ActiveConns: 1},
	}

	u := b.Select(candidates)
	if u.ID != "B" {
		t.Errorf("least_conn: selected %s, want B (fewer conns despite lower weight)", u.ID)
	}
}

func TestSelectNReturnsAtMostN(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer(types.LBPolicyWeightedRandom, tracker)
	candidates := makeCandidates(1, 1, 1)

	result := b.SelectN(candidates, 2)
	if len(result) != 2 {
		t.Fatalf("SelectN(3 candidates, 2) returned %d results, want 2", len(result))
	}

	// All results should be different upstreams
	seen := make(map[string]bool)
	for _, u := range result {
		if seen[u.ID] {
			t.Errorf("SelectN returned duplicate upstream %s", u.ID)
		}
		seen[u.ID] = true
	}
}

func TestSelectNReturnsAllWhenNExceedsCandidates(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer(types.LBPolicyRoundRobin, tracker)
	candidates := makeCandidates(1, 1)

	result := b.SelectN(candidates, 5)
	if len(result) != 2 {
		t.Fatalf("SelectN(2 candidates, 5) returned %d results, want 2", len(result))
	}

	seen := make(map[string]bool)
	for _, u := range result {
		if seen[u.ID] {
			t.Errorf("SelectN returned duplicate upstream %s", u.ID)
		}
		seen[u.ID] = true
	}
}

func TestSelectNEmptyCandidates(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer(types.LBPolicyWeightedRandom, tracker)

	result := b.SelectN(nil, 3)
	if result != nil {
		t.Errorf("SelectN(nil, 3) = %v, want nil", result)
	}

	result = b.SelectN([]CandidateInfo{}, 3)
	if result != nil {
		t.Errorf("SelectN(empty, 3) = %v, want nil", result)
	}
}

func TestSelectNAllDifferentUpstreams(t *testing.T) {
	tracker := NewConnectionTracker()

	for _, policy := range []string{
		types.LBPolicyWeightedRandom,
		types.LBPolicyRoundRobin,
		types.LBPolicyLeastConn,
	} {
		b := NewBalancer(policy, tracker)
		candidates := []CandidateInfo{
			{Upstream: makeUpstream("A"), Weight: 3, ActiveConns: 1},
			{Upstream: makeUpstream("B"), Weight: 1, ActiveConns: 5},
			{Upstream: makeUpstream("C"), Weight: 2, ActiveConns: 3},
			{Upstream: makeUpstream("D"), Weight: 4, ActiveConns: 2},
		}

		result := b.SelectN(candidates, 4)
		if len(result) != 4 {
			t.Fatalf("SelectN(%s, 4 candidates, 4) returned %d results, want 4", policy, len(result))
		}
		seen := make(map[string]bool)
		for _, u := range result {
			if seen[u.ID] {
				t.Errorf("SelectN(%s) returned duplicate upstream %s", policy, u.ID)
			}
			seen[u.ID] = true
		}
	}
}

func TestSelectNLeastConnOrder(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer(types.LBPolicyLeastConn, tracker)

	candidates := []CandidateInfo{
		{Upstream: makeUpstream("A"), Weight: 1, ActiveConns: 10},
		{Upstream: makeUpstream("B"), Weight: 1, ActiveConns: 1},
		{Upstream: makeUpstream("C"), Weight: 1, ActiveConns: 5},
	}

	result := b.SelectN(candidates, 3)
	if len(result) != 3 {
		t.Fatalf("SelectN returned %d results, want 3", len(result))
	}
	// Should be ordered by fewest connections: B(1), C(5), A(10)
	if result[0].ID != "B" {
		t.Errorf("SelectN least_conn [0] = %s, want B", result[0].ID)
	}
	if result[1].ID != "C" {
		t.Errorf("SelectN least_conn [1] = %s, want C", result[1].ID)
	}
	if result[2].ID != "A" {
		t.Errorf("SelectN least_conn [2] = %s, want A", result[2].ID)
	}
}

func TestDefaultPolicyIsWeightedRandom(t *testing.T) {
	tracker := NewConnectionTracker()
	b := NewBalancer("unknown_policy", tracker)
	candidates := makeCandidates(1, 1)

	// Should not panic -- falls through to default (weighted_random)
	u := b.Select(candidates)
	if u == nil {
		t.Error("Select with unknown policy returned nil")
	}
}
