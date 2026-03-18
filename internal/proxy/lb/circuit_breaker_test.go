package lb

import (
	"sync"
	"testing"
	"time"
)

func TestNewCircuitBreakerDefaults(t *testing.T) {
	cb := NewCircuitBreaker(0, 0, 0)
	if cb.failThreshold != 5 {
		t.Errorf("failThreshold = %d, want 5", cb.failThreshold)
	}
	if cb.successThreshold != 2 {
		t.Errorf("successThreshold = %d, want 2", cb.successThreshold)
	}
	if cb.openDuration != 30*time.Second {
		t.Errorf("openDuration = %v, want 30s", cb.openDuration)
	}
}

func TestCircuitStartsClosed(t *testing.T) {
	cb := NewCircuitBreaker(3, 2, 10*time.Millisecond)
	state := cb.State("upstream-1")
	if state != CircuitClosed {
		t.Errorf("initial state = %v, want Closed", state)
	}
}

func TestCanPassClosedAlwaysTrue(t *testing.T) {
	cb := NewCircuitBreaker(3, 2, 10*time.Millisecond)
	for i := 0; i < 10; i++ {
		if !cb.CanPass("upstream-1") {
			t.Fatal("CanPass returned false for Closed circuit")
		}
	}
}

func TestClosedToOpenTransition(t *testing.T) {
	cb := NewCircuitBreaker(3, 2, 10*time.Millisecond)

	// Failures below threshold keep circuit closed
	cb.RecordFailure("upstream-1")
	cb.RecordFailure("upstream-1")
	if cb.State("upstream-1") != CircuitClosed {
		t.Fatal("circuit should still be closed after 2 failures (threshold=3)")
	}

	// Third failure should trip the circuit
	cb.RecordFailure("upstream-1")
	if cb.State("upstream-1") != CircuitOpen {
		t.Fatalf("circuit should be open after %d failures, got %v", 3, cb.State("upstream-1"))
	}
}

func TestOpenCircuitBlocksTraffic(t *testing.T) {
	cb := NewCircuitBreaker(2, 1, 1*time.Hour) // Long open duration so it won't transition

	cb.RecordFailure("upstream-1")
	cb.RecordFailure("upstream-1")
	if cb.State("upstream-1") != CircuitOpen {
		t.Fatal("circuit should be open")
	}

	if cb.CanPass("upstream-1") {
		t.Fatal("CanPass should return false for open circuit (before openDuration)")
	}
}

func TestOpenToHalfOpenTransition(t *testing.T) {
	cb := NewCircuitBreaker(2, 1, 10*time.Millisecond)

	// Trip the circuit
	cb.RecordFailure("upstream-1")
	cb.RecordFailure("upstream-1")
	if cb.State("upstream-1") != CircuitOpen {
		t.Fatal("circuit should be open")
	}

	// Wait for openDuration to elapse
	time.Sleep(15 * time.Millisecond)

	// CanPass should transition to half-open and return true
	if !cb.CanPass("upstream-1") {
		t.Fatal("CanPass should return true after openDuration (half-open)")
	}
	if cb.State("upstream-1") != CircuitHalfOpen {
		t.Fatalf("state should be half-open, got %v", cb.State("upstream-1"))
	}
}

func TestHalfOpenToClosedTransition(t *testing.T) {
	cb := NewCircuitBreaker(2, 2, 10*time.Millisecond)

	// Trip the circuit and transition to half-open
	cb.RecordFailure("upstream-1")
	cb.RecordFailure("upstream-1")
	time.Sleep(15 * time.Millisecond)
	cb.CanPass("upstream-1") // Triggers transition to half-open

	if cb.State("upstream-1") != CircuitHalfOpen {
		t.Fatal("expected half-open state")
	}

	// First success -- not enough to close yet
	cb.RecordSuccess("upstream-1")
	if cb.State("upstream-1") != CircuitHalfOpen {
		t.Fatal("should still be half-open after 1 success (threshold=2)")
	}

	// Second success -- should close the circuit
	cb.RecordSuccess("upstream-1")
	if cb.State("upstream-1") != CircuitClosed {
		t.Fatalf("should be closed after 2 successes, got %v", cb.State("upstream-1"))
	}
}

func TestHalfOpenFailureImmediatelyOpens(t *testing.T) {
	cb := NewCircuitBreaker(2, 3, 10*time.Millisecond)

	// Trip circuit and transition to half-open
	cb.RecordFailure("upstream-1")
	cb.RecordFailure("upstream-1")
	time.Sleep(15 * time.Millisecond)
	cb.CanPass("upstream-1")

	if cb.State("upstream-1") != CircuitHalfOpen {
		t.Fatal("expected half-open state")
	}

	// Any failure in half-open should immediately reopen
	cb.RecordFailure("upstream-1")
	if cb.State("upstream-1") != CircuitOpen {
		t.Fatalf("should be open after failure in half-open, got %v", cb.State("upstream-1"))
	}
}

func TestSuccessResetsFailuresInClosed(t *testing.T) {
	cb := NewCircuitBreaker(3, 2, 10*time.Millisecond)

	// Two failures, then a success should reset the counter
	cb.RecordFailure("upstream-1")
	cb.RecordFailure("upstream-1")
	cb.RecordSuccess("upstream-1")

	// Now two more failures should NOT open the circuit (counter was reset)
	cb.RecordFailure("upstream-1")
	cb.RecordFailure("upstream-1")
	if cb.State("upstream-1") != CircuitClosed {
		t.Fatal("circuit should still be closed after success reset failures")
	}

	// Third consecutive failure opens it
	cb.RecordFailure("upstream-1")
	if cb.State("upstream-1") != CircuitOpen {
		t.Fatal("circuit should now be open after 3 consecutive failures")
	}
}

func TestSuccessThresholdInHalfOpen(t *testing.T) {
	// successThreshold = 3
	cb := NewCircuitBreaker(1, 3, 10*time.Millisecond)

	// Trip and go to half-open
	cb.RecordFailure("upstream-1")
	time.Sleep(15 * time.Millisecond)
	cb.CanPass("upstream-1")

	// Need exactly 3 successes
	cb.RecordSuccess("upstream-1")
	if cb.State("upstream-1") != CircuitHalfOpen {
		t.Fatal("should be half-open after 1/3 successes")
	}
	cb.RecordSuccess("upstream-1")
	if cb.State("upstream-1") != CircuitHalfOpen {
		t.Fatal("should be half-open after 2/3 successes")
	}
	cb.RecordSuccess("upstream-1")
	if cb.State("upstream-1") != CircuitClosed {
		t.Fatal("should be closed after 3/3 successes")
	}
}

func TestFullLifecycle(t *testing.T) {
	// Full state machine test: Closed -> Open -> HalfOpen -> Closed
	cb := NewCircuitBreaker(2, 1, 10*time.Millisecond)

	// Start closed
	if cb.State("u1") != CircuitClosed {
		t.Fatal("expected closed")
	}
	if !cb.CanPass("u1") {
		t.Fatal("expected CanPass=true for closed")
	}

	// Transition to open
	cb.RecordFailure("u1")
	cb.RecordFailure("u1")
	if cb.State("u1") != CircuitOpen {
		t.Fatal("expected open")
	}
	if cb.CanPass("u1") {
		t.Fatal("expected CanPass=false for open")
	}

	// Wait and transition to half-open
	time.Sleep(15 * time.Millisecond)
	if !cb.CanPass("u1") {
		t.Fatal("expected CanPass=true after openDuration")
	}
	if cb.State("u1") != CircuitHalfOpen {
		t.Fatal("expected half-open")
	}

	// Transition back to closed
	cb.RecordSuccess("u1")
	if cb.State("u1") != CircuitClosed {
		t.Fatal("expected closed after success in half-open")
	}
}

func TestFullLifecycleWithHalfOpenFailure(t *testing.T) {
	// Closed -> Open -> HalfOpen -> Open (failure) -> HalfOpen -> Closed
	cb := NewCircuitBreaker(2, 1, 10*time.Millisecond)

	// Trip to open
	cb.RecordFailure("u1")
	cb.RecordFailure("u1")

	// Wait and go half-open
	time.Sleep(15 * time.Millisecond)
	cb.CanPass("u1")
	if cb.State("u1") != CircuitHalfOpen {
		t.Fatal("expected half-open")
	}

	// Fail in half-open -> back to open
	cb.RecordFailure("u1")
	if cb.State("u1") != CircuitOpen {
		t.Fatal("expected open after half-open failure")
	}

	// Wait again and recover
	time.Sleep(15 * time.Millisecond)
	cb.CanPass("u1")
	if cb.State("u1") != CircuitHalfOpen {
		t.Fatal("expected half-open again")
	}

	cb.RecordSuccess("u1")
	if cb.State("u1") != CircuitClosed {
		t.Fatal("expected closed after recovery")
	}
}

func TestOpenDurationTiming(t *testing.T) {
	cb := NewCircuitBreaker(1, 1, 50*time.Millisecond)

	cb.RecordFailure("u1")
	if cb.State("u1") != CircuitOpen {
		t.Fatal("expected open")
	}

	// Should still be open before duration elapses
	time.Sleep(10 * time.Millisecond)
	if cb.CanPass("u1") {
		t.Fatal("CanPass should be false before openDuration")
	}

	// Should transition after duration
	time.Sleep(50 * time.Millisecond)
	if !cb.CanPass("u1") {
		t.Fatal("CanPass should be true after openDuration")
	}
}

func TestIndependentUpstreams(t *testing.T) {
	cb := NewCircuitBreaker(2, 1, 10*time.Millisecond)

	// Trip upstream-1
	cb.RecordFailure("upstream-1")
	cb.RecordFailure("upstream-1")

	// upstream-2 should be unaffected
	if cb.State("upstream-1") != CircuitOpen {
		t.Fatal("upstream-1 should be open")
	}
	if cb.State("upstream-2") != CircuitClosed {
		t.Fatal("upstream-2 should be closed")
	}
	if !cb.CanPass("upstream-2") {
		t.Fatal("upstream-2 should allow traffic")
	}
}

func TestRecordFailureInOpenIsNoOp(t *testing.T) {
	cb := NewCircuitBreaker(2, 1, 1*time.Hour)

	// Trip to open
	cb.RecordFailure("u1")
	cb.RecordFailure("u1")
	if cb.State("u1") != CircuitOpen {
		t.Fatal("expected open")
	}

	// Additional failures should be no-op
	cb.RecordFailure("u1")
	cb.RecordFailure("u1")
	cb.RecordFailure("u1")
	if cb.State("u1") != CircuitOpen {
		t.Fatal("should still be open")
	}
}

func TestRecordSuccessInOpenIsNoOp(t *testing.T) {
	cb := NewCircuitBreaker(2, 1, 1*time.Hour)

	// Trip to open
	cb.RecordFailure("u1")
	cb.RecordFailure("u1")
	if cb.State("u1") != CircuitOpen {
		t.Fatal("expected open")
	}

	// Success in open should not change state
	cb.RecordSuccess("u1")
	if cb.State("u1") != CircuitOpen {
		t.Fatal("should still be open after success (open state)")
	}
}

func TestHalfOpenCanPass(t *testing.T) {
	cb := NewCircuitBreaker(1, 2, 10*time.Millisecond)

	cb.RecordFailure("u1")
	time.Sleep(15 * time.Millisecond)
	cb.CanPass("u1") // transition to half-open

	// Multiple CanPass calls in half-open should all return true
	for i := 0; i < 5; i++ {
		if !cb.CanPass("u1") {
			t.Fatalf("CanPass should be true in half-open (call %d)", i)
		}
	}
}

func TestConcurrentAccess(t *testing.T) {
	cb := NewCircuitBreaker(100, 10, 10*time.Millisecond)

	var wg sync.WaitGroup
	upstreamID := "concurrent-test"

	// Concurrent failures
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.RecordFailure(upstreamID)
		}()
	}

	// Concurrent successes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.RecordSuccess(upstreamID)
		}()
	}

	// Concurrent CanPass
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.CanPass(upstreamID)
		}()
	}

	// Concurrent State
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.State(upstreamID)
		}()
	}

	wg.Wait()
	// If we get here without a race condition, the test passes.
	// The state may be anything depending on goroutine ordering.
	state := cb.State(upstreamID)
	t.Logf("final state after concurrent access: %v", state)
}

func TestConcurrentMultipleUpstreams(t *testing.T) {
	cb := NewCircuitBreaker(5, 2, 10*time.Millisecond)
	upstreams := []string{"u1", "u2", "u3", "u4", "u5"}

	var wg sync.WaitGroup
	for _, uid := range upstreams {
		uid := uid
		wg.Add(3)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				cb.RecordFailure(uid)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				cb.RecordSuccess(uid)
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				cb.CanPass(uid)
			}
		}()
	}
	wg.Wait()

	// Verify no panics and state is valid for each upstream
	for _, uid := range upstreams {
		state := cb.State(uid)
		if state != CircuitClosed && state != CircuitOpen && state != CircuitHalfOpen {
			t.Errorf("upstream %s has invalid state: %v", uid, state)
		}
	}
}

func TestCircuitStateString(t *testing.T) {
	tests := []struct {
		state CircuitState
		want  string
	}{
		{CircuitClosed, "closed"},
		{CircuitOpen, "open"},
		{CircuitHalfOpen, "half-open"},
		{CircuitState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}
