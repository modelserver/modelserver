package ratelimit

import (
	"sync"
	"time"
)

// MemoryCounters holds in-memory rate limit counters.
type MemoryCounters struct {
	mu       sync.RWMutex
	requests map[string][]time.Time
	tokens   map[string][]tokenEntry
}

type tokenEntry struct {
	at    time.Time
	count int64
}

// NewMemoryCounters creates a new in-memory counter store.
func NewMemoryCounters() *MemoryCounters {
	return &MemoryCounters{
		requests: make(map[string][]time.Time),
		tokens:   make(map[string][]tokenEntry),
	}
}

// AddRequest records a request timestamp.
func (m *MemoryCounters) AddRequest(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[key] = append(m.requests[key], time.Now())
}

// AddTokens records token usage.
func (m *MemoryCounters) AddTokens(key string, count int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[key] = append(m.tokens[key], tokenEntry{at: time.Now(), count: count})
}

// CountRequests returns the number of requests within a window.
func (m *MemoryCounters) CountRequests(key string, window time.Duration) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cutoff := time.Now().Add(-window)
	var count int64
	for _, t := range m.requests[key] {
		if t.After(cutoff) {
			count++
		}
	}
	return count
}

// SumTokens returns total tokens within a window.
func (m *MemoryCounters) SumTokens(key string, window time.Duration) int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cutoff := time.Now().Add(-window)
	var total int64
	for _, e := range m.tokens[key] {
		if e.at.After(cutoff) {
			total += e.count
		}
	}
	return total
}

// Cleanup removes expired entries older than maxAge.
func (m *MemoryCounters) Cleanup(maxAge time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)

	for key, timestamps := range m.requests {
		var kept []time.Time
		for _, t := range timestamps {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(m.requests, key)
		} else {
			m.requests[key] = kept
		}
	}

	for key, entries := range m.tokens {
		var kept []tokenEntry
		for _, e := range entries {
			if e.at.After(cutoff) {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(m.tokens, key)
		} else {
			m.tokens[key] = kept
		}
	}
}
