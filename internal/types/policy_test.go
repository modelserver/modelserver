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
				CacheReadRate:     0,
			},
			"_default": {
				InputRate:         0.4,
				OutputRate:        2.0,
				CacheCreationRate: 0.4,
				CacheReadRate:     0,
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
			expected: 0.667*1000 + 3.333*500 + 0.667*200 + 0*100,
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

// TestComputeCreditsWithDefault_FallbackOrder pins down the four-step
// resolution order from the model-catalog spec: plan override → catalog
// default → plan _default → 0.
func TestComputeCreditsWithDefault_FallbackOrder(t *testing.T) {
	planOverride := CreditRate{InputRate: 1, OutputRate: 1}
	catalogDefault := CreditRate{InputRate: 2, OutputRate: 2}
	planDefault := CreditRate{InputRate: 3, OutputRate: 3}

	cases := []struct {
		name      string
		policy    *RateLimitPolicy
		catalog   *CreditRate
		wantInput float64
	}{
		{
			"plan override wins over everything else",
			&RateLimitPolicy{ModelCreditRates: map[string]CreditRate{"m": planOverride, "_default": planDefault}},
			&catalogDefault,
			1,
		},
		{
			"catalog default wins when plan has no override",
			&RateLimitPolicy{ModelCreditRates: map[string]CreditRate{"_default": planDefault}},
			&catalogDefault,
			2,
		},
		{
			"plan _default wins when catalog has no default",
			&RateLimitPolicy{ModelCreditRates: map[string]CreditRate{"_default": planDefault}},
			nil,
			3,
		},
		{
			"zero when nothing is configured",
			&RateLimitPolicy{},
			nil,
			0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.policy.ComputeCreditsWithDefault("m", tc.catalog, 1, 0, 0, 0)
			if math.Abs(got-tc.wantInput) > 1e-9 {
				t.Errorf("got %v, want %v", got, tc.wantInput)
			}
		})
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
