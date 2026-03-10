package ratelimit

import (
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

func TestClassicLimiter_RPM(t *testing.T) {
	mem := NewMemoryCounters()
	cl := &ClassicLimiter{counters: mem}

	rules := []types.ClassicRule{
		{Metric: "rpm", Limit: 3, PerModel: false},
	}

	for i := 0; i < 3; i++ {
		allowed, _ := cl.Check("key1", "claude-sonnet-4", rules)
		if !allowed {
			t.Fatalf("request %d should be allowed", i+1)
		}
		cl.Record("key1", "claude-sonnet-4", types.TokenUsage{InputTokens: 10, OutputTokens: 5})
	}

	allowed, retryAfter := cl.Check("key1", "claude-sonnet-4", rules)
	if allowed {
		t.Error("4th request should be blocked")
	}
	if retryAfter <= 0 {
		t.Error("retryAfter should be positive")
	}
}

func TestClassicLimiter_TPM_PerModel(t *testing.T) {
	mem := NewMemoryCounters()
	cl := &ClassicLimiter{counters: mem}

	rules := []types.ClassicRule{
		{Metric: "tpm", Limit: 100, PerModel: true},
	}

	cl.Record("key1", "model-a", types.TokenUsage{InputTokens: 40, OutputTokens: 20})
	cl.Record("key1", "model-b", types.TokenUsage{InputTokens: 20, OutputTokens: 10})

	allowed, _ := cl.Check("key1", "model-a", rules)
	if !allowed {
		t.Error("model-a should be allowed (60/100)")
	}

	allowed, _ = cl.Check("key1", "model-b", rules)
	if !allowed {
		t.Error("model-b should be allowed (30/100)")
	}

	cl.Record("key1", "model-a", types.TokenUsage{InputTokens: 30, OutputTokens: 20})
	allowed, _ = cl.Check("key1", "model-a", rules)
	if allowed {
		t.Error("model-a should be blocked (110/100)")
	}

	allowed, _ = cl.Check("key1", "model-b", rules)
	if !allowed {
		t.Error("model-b should still be allowed (30/100)")
	}
}

func TestMemoryCounters_Cleanup(t *testing.T) {
	mem := NewMemoryCounters()
	mem.AddRequest("key1:rpm")
	mem.AddTokens("key1:tpm", 100)

	if mem.CountRequests("key1:rpm", time.Minute) != 1 {
		t.Error("expected 1 request before cleanup")
	}

	// Cleanup with zero maxAge should remove everything.
	mem.Cleanup(0)

	if mem.CountRequests("key1:rpm", time.Minute) != 0 {
		t.Error("expected 0 requests after cleanup")
	}
	if mem.SumTokens("key1:tpm", time.Minute) != 0 {
		t.Error("expected 0 tokens after cleanup")
	}
}
