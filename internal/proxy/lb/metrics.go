package lb

import (
	"sync"
	"sync/atomic"
	"time"
)

// UpstreamMetrics tracks per-upstream health/performance metrics in memory.
// Used by: health endpoint, routing events.
// NOT used for LB decisions this phase -- circuit breaker + health checker handle that.
type UpstreamMetrics struct {
	mu      sync.RWMutex
	metrics map[string]*UpstreamStats // upstreamID -> stats
}

// UpstreamStats holds per-upstream counters and timestamps.
type UpstreamStats struct {
	TotalRequests    atomic.Int64
	RecentErrors     atomic.Int64
	ConsecutiveFails atomic.Int64
	LastErrorAt      atomic.Value // time.Time
	LastSuccessAt    atomic.Value // time.Time
}

// NewUpstreamMetrics creates a new UpstreamMetrics store.
func NewUpstreamMetrics() *UpstreamMetrics {
	return &UpstreamMetrics{metrics: make(map[string]*UpstreamStats)}
}

// RecordSuccess records a successful request for the given upstream.
func (m *UpstreamMetrics) RecordSuccess(upstreamID string) {
	s := m.getOrCreate(upstreamID)
	s.TotalRequests.Add(1)
	s.ConsecutiveFails.Store(0)
	s.LastSuccessAt.Store(time.Now())
}

// RecordError records a failed request for the given upstream.
func (m *UpstreamMetrics) RecordError(upstreamID string) {
	s := m.getOrCreate(upstreamID)
	s.TotalRequests.Add(1)
	s.RecentErrors.Add(1)
	s.ConsecutiveFails.Add(1)
	s.LastErrorAt.Store(time.Now())
}

// GetStats returns the stats for the given upstream, or nil if not tracked.
func (m *UpstreamMetrics) GetStats(upstreamID string) *UpstreamStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.metrics[upstreamID]
}

func (m *UpstreamMetrics) getOrCreate(upstreamID string) *UpstreamStats {
	m.mu.RLock()
	if s, ok := m.metrics[upstreamID]; ok {
		m.mu.RUnlock()
		return s
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.metrics[upstreamID]; ok {
		return s
	}
	s := &UpstreamStats{}
	m.metrics[upstreamID] = s
	return s
}
