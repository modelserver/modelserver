package lb

import (
	"math/rand"
	"sync/atomic"

	"github.com/modelserver/modelserver/internal/types"
)

// CandidateInfo provides upstream state to the balancer.
type CandidateInfo struct {
	Upstream    *types.Upstream
	Weight      int   // Effective weight (from group member override, or upstream default)
	IsBackup    bool
	ActiveConns int64 // From ConnectionTracker
}

// Balancer selects an upstream from pre-filtered candidates using a single policy.
// Candidates have already been filtered by circuit breaker (open circuits removed)
// and health checker (down upstreams removed). The balancer only picks from healthy options.
type Balancer struct {
	policy  string
	tracker *ConnectionTracker
	counter atomic.Uint64 // for round-robin
}

// NewBalancer creates a balancer with the given policy and connection tracker.
func NewBalancer(policy string, tracker *ConnectionTracker) *Balancer {
	return &Balancer{policy: policy, tracker: tracker}
}

// Select picks one upstream from candidates.
func (b *Balancer) Select(candidates []CandidateInfo) *types.Upstream {
	if len(candidates) == 0 {
		return nil
	}
	switch b.policy {
	case types.LBPolicyRoundRobin:
		return b.roundRobin(candidates)
	case types.LBPolicyLeastConn:
		return b.leastConn(candidates)
	default: // weighted_random
		return b.weightedRandom(candidates)
	}
}

// SelectN picks top N upstreams in preference order (for retry fallback list).
// Primary is [0], fallbacks are [1..N-1].
func (b *Balancer) SelectN(candidates []CandidateInfo, n int) []*types.Upstream {
	if len(candidates) == 0 {
		return nil
	}
	if n > len(candidates) {
		n = len(candidates)
	}
	// For each position, select from remaining candidates using the policy,
	// then remove selected from pool.
	result := make([]*types.Upstream, 0, n)
	remaining := make([]CandidateInfo, len(candidates))
	copy(remaining, candidates)

	for i := 0; i < n && len(remaining) > 0; i++ {
		var selected *types.Upstream
		var selectedIdx int
		switch b.policy {
		case types.LBPolicyRoundRobin:
			selected, selectedIdx = b.roundRobinIdx(remaining)
		case types.LBPolicyLeastConn:
			selected, selectedIdx = b.leastConnIdx(remaining)
		default:
			selected, selectedIdx = b.weightedRandomIdx(remaining)
		}
		if selected != nil {
			result = append(result, selected)
			// Remove selected from remaining
			remaining = append(remaining[:selectedIdx], remaining[selectedIdx+1:]...)
		}
	}
	return result
}

// weightedRandom selects an upstream using cumulative weight sum + random pick.
func (b *Balancer) weightedRandom(candidates []CandidateInfo) *types.Upstream {
	selected, _ := b.weightedRandomIdx(candidates)
	return selected
}

func (b *Balancer) weightedRandomIdx(candidates []CandidateInfo) (*types.Upstream, int) {
	totalWeight := 0
	for _, c := range candidates {
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}
	if totalWeight == 0 {
		return nil, -1
	}

	r := rand.Intn(totalWeight)
	cumulative := 0
	for i, c := range candidates {
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		cumulative += w
		if r < cumulative {
			return c.Upstream, i
		}
	}
	// Fallback (should not happen)
	return candidates[len(candidates)-1].Upstream, len(candidates) - 1
}

// roundRobin selects an upstream using an atomic counter for even distribution.
func (b *Balancer) roundRobin(candidates []CandidateInfo) *types.Upstream {
	selected, _ := b.roundRobinIdx(candidates)
	return selected
}

func (b *Balancer) roundRobinIdx(candidates []CandidateInfo) (*types.Upstream, int) {
	idx := int(b.counter.Add(1)-1) % len(candidates)
	return candidates[idx].Upstream, idx
}

// leastConn selects the upstream with the fewest active connections,
// breaking ties by higher weight.
func (b *Balancer) leastConn(candidates []CandidateInfo) *types.Upstream {
	selected, _ := b.leastConnIdx(candidates)
	return selected
}

func (b *Balancer) leastConnIdx(candidates []CandidateInfo) (*types.Upstream, int) {
	bestIdx := 0
	bestConns := candidates[0].ActiveConns
	bestWeight := candidates[0].Weight

	for i := 1; i < len(candidates); i++ {
		c := candidates[i]
		if c.ActiveConns < bestConns || (c.ActiveConns == bestConns && c.Weight > bestWeight) {
			bestIdx = i
			bestConns = c.ActiveConns
			bestWeight = c.Weight
		}
	}
	return candidates[bestIdx].Upstream, bestIdx
}
