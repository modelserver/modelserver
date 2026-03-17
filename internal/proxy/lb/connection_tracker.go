package lb

import (
	"sync"
	"sync/atomic"
)

// ConnectionTracker tracks active connections per upstream.
type ConnectionTracker struct {
	counts sync.Map // upstreamID -> *atomic.Int64
}

// NewConnectionTracker creates a new ConnectionTracker.
func NewConnectionTracker() *ConnectionTracker {
	return &ConnectionTracker{}
}

// Acquire increments the active connection count for the given upstream.
func (ct *ConnectionTracker) Acquire(upstreamID string) {
	ct.getOrCreate(upstreamID).Add(1)
}

// Release decrements the active connection count for the given upstream.
func (ct *ConnectionTracker) Release(upstreamID string) {
	ct.getOrCreate(upstreamID).Add(-1)
}

// Count returns the current active connection count for the given upstream.
func (ct *ConnectionTracker) Count(upstreamID string) int64 {
	return ct.getOrCreate(upstreamID).Load()
}

func (ct *ConnectionTracker) getOrCreate(upstreamID string) *atomic.Int64 {
	if v, ok := ct.counts.Load(upstreamID); ok {
		return v.(*atomic.Int64)
	}
	v, _ := ct.counts.LoadOrStore(upstreamID, &atomic.Int64{})
	return v.(*atomic.Int64)
}
