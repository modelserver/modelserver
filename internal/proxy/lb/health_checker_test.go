package lb

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestHealthCheckerRegister(t *testing.T) {
	cb := NewCircuitBreaker(5, 2, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 30*time.Second, 5*time.Second)

	if hc.Status("u1") != HealthUnknown {
		t.Errorf("initial status should be HealthUnknown, got %v", hc.Status("u1"))
	}

	// Verify the entry was created
	hc.mu.RLock()
	entry, ok := hc.upstreams["u1"]
	hc.mu.RUnlock()
	if !ok {
		t.Fatal("upstream u1 should be registered")
	}
	if entry.provider != "openai" {
		t.Errorf("provider = %q, want %q", entry.provider, "openai")
	}
	if entry.testModel != "gpt-4" {
		t.Errorf("testModel = %q, want %q", entry.testModel, "gpt-4")
	}
	if entry.interval != 30*time.Second {
		t.Errorf("interval = %v, want 30s", entry.interval)
	}
	if entry.timeout != 5*time.Second {
		t.Errorf("timeout = %v, want 5s", entry.timeout)
	}
}

func TestHealthCheckerRegisterSkipsEmptyTestModel(t *testing.T) {
	cb := NewCircuitBreaker(5, 2, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "", "sk-test", 30*time.Second, 5*time.Second)

	hc.mu.RLock()
	_, ok := hc.upstreams["u1"]
	hc.mu.RUnlock()
	if ok {
		t.Fatal("upstream with empty test model should not be registered")
	}
}

func TestHealthCheckerRegisterDefaults(t *testing.T) {
	cb := NewCircuitBreaker(5, 2, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	// Zero interval/timeout should get defaults
	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 0, 0)

	hc.mu.RLock()
	entry := hc.upstreams["u1"]
	hc.mu.RUnlock()

	if entry.interval != 30*time.Second {
		t.Errorf("interval = %v, want default 30s", entry.interval)
	}
	if entry.timeout != 30*time.Second {
		t.Errorf("timeout = %v, want default 30s", entry.timeout)
	}
}

func TestHealthCheckerDeregister(t *testing.T) {
	cb := NewCircuitBreaker(5, 2, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 30*time.Second, 5*time.Second)
	hc.Deregister("u1")

	hc.mu.RLock()
	_, ok := hc.upstreams["u1"]
	hc.mu.RUnlock()
	if ok {
		t.Fatal("upstream should be deregistered")
	}

	// Status of deregistered upstream should be Unknown
	if hc.Status("u1") != HealthUnknown {
		t.Error("deregistered upstream should return HealthUnknown")
	}
}

func TestHealthCheckerDeregisterNonExistent(t *testing.T) {
	cb := NewCircuitBreaker(5, 2, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	// Should not panic
	hc.Deregister("nonexistent")
}

func TestHealthCheckerStatusUnregistered(t *testing.T) {
	cb := NewCircuitBreaker(5, 2, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	if hc.Status("unknown-upstream") != HealthUnknown {
		t.Errorf("unregistered upstream should have HealthUnknown status")
	}
}

func TestOnProbeResultOK(t *testing.T) {
	cb := NewCircuitBreaker(5, 2, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 30*time.Second, 5*time.Second)

	hc.OnProbeResult("u1", HealthOK)

	if hc.Status("u1") != HealthOK {
		t.Errorf("status should be HealthOK after OK probe, got %v", hc.Status("u1"))
	}

	// Circuit breaker should have recorded a success
	if cb.State("u1") != CircuitClosed {
		t.Errorf("circuit should be closed, got %v", cb.State("u1"))
	}

	// Metrics should have recorded a success
	stats := metrics.GetStats("u1")
	if stats == nil {
		t.Fatal("metrics should have stats for u1")
	}
	if stats.TotalRequests.Load() != 1 {
		t.Errorf("total requests = %d, want 1", stats.TotalRequests.Load())
	}
}

func TestOnProbeResultDown(t *testing.T) {
	cb := NewCircuitBreaker(2, 1, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 30*time.Second, 5*time.Second)

	hc.OnProbeResult("u1", HealthDown)

	if hc.Status("u1") != HealthDown {
		t.Errorf("status should be HealthDown, got %v", hc.Status("u1"))
	}

	// Metrics should have recorded an error
	stats := metrics.GetStats("u1")
	if stats == nil {
		t.Fatal("metrics should have stats for u1")
	}
	if stats.RecentErrors.Load() != 1 {
		t.Errorf("recent errors = %d, want 1", stats.RecentErrors.Load())
	}
}

func TestOnProbeResultDownTripsCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker(2, 1, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 30*time.Second, 5*time.Second)

	// Two consecutive HealthDown should trip the circuit breaker
	hc.OnProbeResult("u1", HealthDown)
	hc.OnProbeResult("u1", HealthDown)

	if cb.State("u1") != CircuitOpen {
		t.Errorf("circuit should be open after 2 down probes, got %v", cb.State("u1"))
	}
}

func TestOnProbeResultDegradedCountsAsSuccess(t *testing.T) {
	cb := NewCircuitBreaker(2, 1, 10*time.Millisecond)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 30*time.Second, 5*time.Second)

	// Trip the circuit breaker
	hc.OnProbeResult("u1", HealthDown)
	hc.OnProbeResult("u1", HealthDown)
	if cb.State("u1") != CircuitOpen {
		t.Fatal("expected open circuit")
	}

	// Wait for open duration, transition to half-open
	time.Sleep(15 * time.Millisecond)
	cb.CanPass("u1")
	if cb.State("u1") != CircuitHalfOpen {
		t.Fatal("expected half-open circuit")
	}

	// Degraded counts as success for CB
	hc.OnProbeResult("u1", HealthDegraded)
	if cb.State("u1") != CircuitClosed {
		t.Errorf("circuit should be closed after degraded probe in half-open (counts as success), got %v", cb.State("u1"))
	}
}

func TestOnProbeResultConsecutiveCounters(t *testing.T) {
	cb := NewCircuitBreaker(100, 2, 30*time.Second) // High threshold so CB doesn't trip
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 30*time.Second, 5*time.Second)

	// 3 OKs
	hc.OnProbeResult("u1", HealthOK)
	hc.OnProbeResult("u1", HealthOK)
	hc.OnProbeResult("u1", HealthOK)

	hc.mu.RLock()
	entry := hc.upstreams["u1"]
	ok := entry.consecutiveOK
	fail := entry.consecutiveFail
	hc.mu.RUnlock()

	if ok != 3 {
		t.Errorf("consecutiveOK = %d, want 3", ok)
	}
	if fail != 0 {
		t.Errorf("consecutiveFail = %d, want 0", fail)
	}

	// 1 Down resets OK counter
	hc.OnProbeResult("u1", HealthDown)

	hc.mu.RLock()
	ok = hc.upstreams["u1"].consecutiveOK
	fail = hc.upstreams["u1"].consecutiveFail
	hc.mu.RUnlock()

	if ok != 0 {
		t.Errorf("consecutiveOK after down = %d, want 0", ok)
	}
	if fail != 1 {
		t.Errorf("consecutiveFail after down = %d, want 1", fail)
	}
}

func TestOnProbeResultUnregisteredUpstream(t *testing.T) {
	cb := NewCircuitBreaker(5, 2, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	// Should not panic for unregistered upstream
	hc.OnProbeResult("nonexistent", HealthOK)
	hc.OnProbeResult("nonexistent", HealthDown)

	// CB still gets the result even without health entry
	// (it just won't update health entry counters)
}

func TestHealthCheckerRecoveryFlow(t *testing.T) {
	// Integration test: upstream goes down, CB trips, probes eventually recover it
	cb := NewCircuitBreaker(2, 2, 10*time.Millisecond)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 30*time.Second, 5*time.Second)

	// Upstream goes down
	hc.OnProbeResult("u1", HealthDown)
	hc.OnProbeResult("u1", HealthDown)
	if cb.State("u1") != CircuitOpen {
		t.Fatal("expected circuit open")
	}
	if hc.Status("u1") != HealthDown {
		t.Fatal("expected health down")
	}

	// Wait for open duration
	time.Sleep(15 * time.Millisecond)
	cb.CanPass("u1") // Transition to half-open

	// Upstream recovers
	hc.OnProbeResult("u1", HealthOK)
	if cb.State("u1") != CircuitHalfOpen {
		t.Fatalf("expected half-open after 1 success (threshold=2), got %v", cb.State("u1"))
	}

	hc.OnProbeResult("u1", HealthOK)
	if cb.State("u1") != CircuitClosed {
		t.Fatalf("expected closed after 2 successes, got %v", cb.State("u1"))
	}
	if hc.Status("u1") != HealthOK {
		t.Fatal("expected health OK after recovery")
	}
}

func TestHealthCheckerMultipleUpstreams(t *testing.T) {
	cb := NewCircuitBreaker(2, 1, 30*time.Second)
	metrics := NewUpstreamMetrics()
	hc := NewHealthChecker(cb, metrics, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 30*time.Second, 5*time.Second)
	hc.Register("u2", "anthropic", "https://api.anthropic.com", "claude-3", "sk-ant", 30*time.Second, 5*time.Second)

	// u1 is healthy, u2 goes down
	hc.OnProbeResult("u1", HealthOK)
	hc.OnProbeResult("u2", HealthDown)
	hc.OnProbeResult("u2", HealthDown)

	if hc.Status("u1") != HealthOK {
		t.Error("u1 should be healthy")
	}
	if hc.Status("u2") != HealthDown {
		t.Error("u2 should be down")
	}
	if cb.State("u1") != CircuitClosed {
		t.Error("u1 circuit should be closed")
	}
	if cb.State("u2") != CircuitOpen {
		t.Error("u2 circuit should be open")
	}
}

func TestHealthStatusString(t *testing.T) {
	tests := []struct {
		status HealthStatus
		want   string
	}{
		{HealthUnknown, "unknown"},
		{HealthOK, "ok"},
		{HealthDegraded, "degraded"},
		{HealthDown, "down"},
		{HealthStatus(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("%d.String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestAverageDuration(t *testing.T) {
	tests := []struct {
		name string
		ds   []time.Duration
		want time.Duration
	}{
		{"empty", nil, 0},
		{"single", []time.Duration{100 * time.Millisecond}, 100 * time.Millisecond},
		{"multiple", []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond}, 200 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := averageDuration(tt.ds)
			if got != tt.want {
				t.Errorf("averageDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOnProbeResultNilMetrics(t *testing.T) {
	cb := NewCircuitBreaker(5, 2, 30*time.Second)
	// nil metrics should not panic
	hc := NewHealthChecker(cb, nil, newTestLogger())

	hc.Register("u1", "openai", "https://api.openai.com", "gpt-4", "sk-test", 30*time.Second, 5*time.Second)

	// These should not panic
	hc.OnProbeResult("u1", HealthOK)
	hc.OnProbeResult("u1", HealthDown)
}
