package types

import (
	"math"
	"testing"
	"time"
)

func TestComputeCredits(t *testing.T) {
	policy := &RateLimitPolicy{
		ModelCreditRates: map[string]CreditRate{
			"claude-opus-4": {
				InputRate:         0.667,
				OutputRate:        3.333,
				CacheCreationRate: 0.667,
				CacheReadRate:     0.667,
			},
			"_default": {
				InputRate:         0.4,
				OutputRate:        2.0,
				CacheCreationRate: 0.4,
				CacheReadRate:     0.4,
			},
		},
	}

	tests := []struct {
		name     string
		model    string
		in, out  int64
		cacheW   int64
		cacheR   int64
		expected float64
	}{
		{
			name:     "opus with all token types",
			model:    "claude-opus-4",
			in:       1000,
			out:      500,
			cacheW:   200,
			cacheR:   100,
			expected: 0.667*1000 + 3.333*500 + 0.667*200 + 0.667*100,
		},
		{
			name:     "unknown model uses default",
			model:    "claude-unknown-99",
			in:       1000,
			out:      500,
			expected: 0.4*1000 + 2.0*500,
		},
		{
			name:     "zero tokens",
			model:    "claude-opus-4",
			in:       0,
			out:      0,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := policy.ComputeCredits(tt.model, tt.in, tt.out, tt.cacheW, tt.cacheR)
			if math.Abs(got-tt.expected) > 0.001 {
				t.Errorf("ComputeCredits() = %f, want %f", got, tt.expected)
			}
		})
	}
}

func TestComputeCreditsNoRates(t *testing.T) {
	policy := &RateLimitPolicy{}
	got := policy.ComputeCredits("claude-opus-4", 1000, 500, 0, 0)
	if got != 0 {
		t.Errorf("expected 0 credits with no rates, got %f", got)
	}
}

func TestPolicyIsActive(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	farPast := now.Add(-2 * time.Hour)

	tests := []struct {
		name     string
		starts   *time.Time
		expires  *time.Time
		expected bool
	}{
		{"no bounds", nil, nil, true},
		{"within window", &past, &future, true},
		{"not started yet", &future, nil, false},
		{"already expired", &farPast, &past, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &RateLimitPolicy{StartsAt: tt.starts, ExpiresAt: tt.expires}
			if p.IsActive() != tt.expected {
				t.Errorf("IsActive() = %v, want %v", p.IsActive(), tt.expected)
			}
		})
	}
}

func TestCreditRuleEffectiveScope(t *testing.T) {
	r1 := CreditRule{Scope: ""}
	if r1.EffectiveScope() != CreditScopeProject {
		t.Errorf("empty scope should default to project, got %s", r1.EffectiveScope())
	}
	r2 := CreditRule{Scope: CreditScopeKey}
	if r2.EffectiveScope() != CreditScopeKey {
		t.Errorf("explicit key scope should stay key, got %s", r2.EffectiveScope())
	}
}
