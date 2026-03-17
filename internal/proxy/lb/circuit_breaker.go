package lb

import (
	"sync"
	"time"
)

// CircuitState represents circuit breaker state.
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // Normal operation
	CircuitOpen                         // Upstream is failing, reject requests
	CircuitHalfOpen                     // Testing if upstream has recovered
)

// String returns a human-readable name for the circuit state.
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreaker implements per-upstream circuit breaker pattern.
//
// State machine:
//
//	Closed  --[failures >= failThreshold]--> Open
//	Open    --[openDuration elapsed]-------> HalfOpen  (transition happens in CanPass)
//	HalfOpen --[successes >= successThreshold]--> Closed
//	HalfOpen --[any failure]---------------> Open
type CircuitBreaker struct {
	mu               sync.RWMutex
	states           map[string]*circuitEntry // upstreamID -> state
	failThreshold    int                      // Failures to open circuit (default: 5)
	successThreshold int                      // Successes in half-open to close (default: 2)
	openDuration     time.Duration            // Time to stay open before half-open (default: 30s)
}

type circuitEntry struct {
	state          CircuitState
	failures       int
	successes      int // Consecutive successes in half-open
	lastFailure    time.Time
	lastTransition time.Time
}

// NewCircuitBreaker creates a CircuitBreaker with the given thresholds.
// Default values: failThreshold=5, successThreshold=2, openDuration=30s.
func NewCircuitBreaker(failThreshold, successThreshold int, openDuration time.Duration) *CircuitBreaker {
	if failThreshold <= 0 {
		failThreshold = 5
	}
	if successThreshold <= 0 {
		successThreshold = 2
	}
	if openDuration <= 0 {
		openDuration = 30 * time.Second
	}
	return &CircuitBreaker{
		states:           make(map[string]*circuitEntry),
		failThreshold:    failThreshold,
		successThreshold: successThreshold,
		openDuration:     openDuration,
	}
}

// CanPass returns true if the upstream is available for traffic.
//   - Closed: always true
//   - Open: true only if openDuration has elapsed (transitions to half-open)
//   - HalfOpen: true (allows probe traffic through)
func (cb *CircuitBreaker) CanPass(upstreamID string) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entry := cb.getOrCreate(upstreamID)
	switch entry.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if time.Since(entry.lastTransition) >= cb.openDuration {
			// Transition to half-open
			entry.state = CircuitHalfOpen
			entry.successes = 0
			entry.lastTransition = time.Now()
			return true
		}
		return false
	case CircuitHalfOpen:
		return true
	default:
		return true
	}
}

// RecordSuccess records a successful request to the upstream.
//   - Closed: resets failures counter
//   - HalfOpen: increments successes, transitions to Closed when successThreshold reached
//   - Open: no-op (shouldn't happen, but be safe)
func (cb *CircuitBreaker) RecordSuccess(upstreamID string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entry := cb.getOrCreate(upstreamID)
	switch entry.state {
	case CircuitClosed:
		entry.failures = 0
	case CircuitHalfOpen:
		entry.successes++
		if entry.successes >= cb.successThreshold {
			entry.state = CircuitClosed
			entry.failures = 0
			entry.successes = 0
			entry.lastTransition = time.Now()
		}
	case CircuitOpen:
		// no-op
	}
}

// RecordFailure records a failed request to the upstream.
//   - Closed: increments failures, transitions to Open when failThreshold reached
//   - HalfOpen: immediately transitions to Open
//   - Open: no-op
func (cb *CircuitBreaker) RecordFailure(upstreamID string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entry := cb.getOrCreate(upstreamID)
	now := time.Now()
	entry.lastFailure = now

	switch entry.state {
	case CircuitClosed:
		entry.failures++
		if entry.failures >= cb.failThreshold {
			entry.state = CircuitOpen
			entry.lastTransition = now
		}
	case CircuitHalfOpen:
		// Any failure immediately reopens the circuit
		entry.state = CircuitOpen
		entry.successes = 0
		entry.lastTransition = now
	case CircuitOpen:
		// no-op
	}
}

// State returns the current circuit state for an upstream.
// Returns CircuitClosed for unknown upstreams.
func (cb *CircuitBreaker) State(upstreamID string) CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	entry, ok := cb.states[upstreamID]
	if !ok {
		return CircuitClosed
	}
	return entry.state
}

// getOrCreate returns the circuit entry for an upstream, creating it if needed.
// Must be called with cb.mu held (either read or write lock).
func (cb *CircuitBreaker) getOrCreate(upstreamID string) *circuitEntry {
	if entry, ok := cb.states[upstreamID]; ok {
		return entry
	}
	entry := &circuitEntry{
		state:          CircuitClosed,
		lastTransition: time.Now(),
	}
	cb.states[upstreamID] = entry
	return entry
}
