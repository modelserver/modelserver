package proxy

import (
	"errors"
	"math"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestComputeExtraUsageCostFen_ZeroUsage(t *testing.T) {
	m := &types.Model{
		Name: "claude-opus-4-7",
		DefaultCreditRate: &types.CreditRate{
			InputRate:  0.667,
			OutputRate: 3.333,
		},
	}
	cost, credits, err := computeExtraUsageCostFen(m, types.TokenUsage{}, 5438)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cost != 0 || credits != 0 {
		t.Errorf("zero usage → cost=%d credits=%v, want (0, 0)", cost, credits)
	}
}

func TestComputeExtraUsageCostFen_OnlyCacheRead(t *testing.T) {
	m := &types.Model{
		Name: "claude-opus-4-7",
		DefaultCreditRate: &types.CreditRate{
			CacheReadRate: 0.1,
		},
	}
	cost, credits, err := computeExtraUsageCostFen(m,
		types.TokenUsage{CacheReadTokens: 10000}, 5438)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// credits = 0.1 * 10000 = 1000
	// cost_fen = ceil(1000 * 5438 / 1e6) = ceil(5.438) = 6
	if cost != 6 {
		t.Errorf("cost=%d, want 6", cost)
	}
	if credits != 1000 {
		t.Errorf("credits=%v, want 1000", credits)
	}
}

func TestComputeExtraUsageCostFen_CeilRoundsUp(t *testing.T) {
	m := &types.Model{
		Name: "x",
		DefaultCreditRate: &types.CreditRate{
			InputRate: 1,
		},
	}
	// 0.0001 credits → cost would be 0.00054 fen → must ceil to 1.
	cost, _, err := computeExtraUsageCostFen(m,
		types.TokenUsage{InputTokens: 1}, 1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cost != 1 {
		t.Errorf("sub-fen charge must round up, got cost=%d", cost)
	}
}

func TestComputeExtraUsageCostFen_MissingRate(t *testing.T) {
	_, _, err := computeExtraUsageCostFen(&types.Model{Name: "x"},
		types.TokenUsage{InputTokens: 1}, 5438)
	if !errors.Is(err, ErrMissingDefaultCreditRate) {
		t.Errorf("want ErrMissingDefaultCreditRate, got %v", err)
	}
}

func TestComputeExtraUsageCostFen_MixedTokens(t *testing.T) {
	m := &types.Model{
		Name: "claude-opus-4-7",
		DefaultCreditRate: &types.CreditRate{
			InputRate:         0.667,
			OutputRate:        3.333,
			CacheCreationRate: 0.667,
			CacheReadRate:     0,
		},
	}
	u := types.TokenUsage{
		InputTokens:         1000,
		OutputTokens:        500,
		CacheCreationTokens: 0,
		CacheReadTokens:     2000,
	}
	// credits = 0.667*1000 + 3.333*500 + 0 + 0 = 667 + 1666.5 = 2333.5
	// cost_fen = ceil(2333.5 * 5438 / 1e6) = ceil(12.69) = 13
	cost, _, err := computeExtraUsageCostFen(m, u, 5438)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cost != 13 {
		t.Errorf("cost=%d, want 13", cost)
	}
}

// TestGPT55_ExtraUsageVsSubscription_LongContext pins the two-tier pricing
// shape introduced by migration 032 against drift:
//   - extra-usage path (computeExtraUsageCostFen) reads catalog → official rate
//   - subscription path (policy.ComputeCreditsWithDefault) reads plan rate first
//   - both apply the same long-context multipliers (2x input/cache, 1.5x output)
//     when total input tokens exceed 272K
func TestGPT55_ExtraUsageVsSubscription_LongContext(t *testing.T) {
	longCtx := &types.LongContextCreditRate{
		ThresholdInputTokens: 272000,
		InputMultiplier:      2,
		OutputMultiplier:     1.5,
	}
	officialCatalog := &types.CreditRate{
		InputRate:     0.667,
		OutputRate:    4.0,
		CacheReadRate: 0.067,
		LongContext:   longCtx,
	}
	subscriptionPlan := types.CreditRate{
		InputRate:     0.044,
		OutputRate:    0.261,
		CacheReadRate: 0.0044,
		LongContext:   longCtx,
	}

	m := &types.Model{Name: "gpt-5.5", DefaultCreditRate: officialCatalog}
	policy := &types.RateLimitPolicy{
		ModelCreditRates: map[string]types.CreditRate{"gpt-5.5": subscriptionPlan},
	}

	type usage struct {
		in, out, cacheRead int64
	}
	short := usage{in: 200000, out: 1000, cacheRead: 1000} // total in 201K ≤ 272K
	long := usage{in: 300000, out: 1000, cacheRead: 1000}  // total in 301K > 272K

	creditsExtra := func(u usage) float64 {
		_, c, err := computeExtraUsageCostFen(m, types.TokenUsage{
			InputTokens:     u.in,
			OutputTokens:    u.out,
			CacheReadTokens: u.cacheRead,
		}, 5438)
		if err != nil {
			t.Fatalf("extra-usage err: %v", err)
		}
		return c
	}
	creditsSub := func(u usage) float64 {
		return policy.ComputeCreditsWithDefault("gpt-5.5", nil, u.in, u.out, 0, u.cacheRead)
	}

	tests := []struct {
		name           string
		got, want      float64
	}{
		{"extra short", creditsExtra(short), 0.667*200000 + 4.0*1000 + 0.067*1000},
		{"extra long", creditsExtra(long), (0.667*2)*300000 + (4.0*1.5)*1000 + (0.067*2)*1000},
		{"sub short", creditsSub(short), 0.044*200000 + 0.261*1000 + 0.0044*1000},
		{"sub long", creditsSub(long), (0.044*2)*300000 + (0.261*1.5)*1000 + (0.0044*2)*1000},
	}
	for _, tc := range tests {
		if math.Abs(tc.got-tc.want) > 0.001 {
			t.Errorf("%s: credits=%v want %v", tc.name, tc.got, tc.want)
		}
	}

	// long_context multipliers must apply identically on both pricing tiers.
	// Compare long/short uplift per tier on a usage where input is the only
	// dimension that crosses the threshold (so cache/output don't muddy it).
	shortAllInput := usage{in: 200000}
	longAllInput := usage{in: 300000}
	extraUplift := creditsExtra(longAllInput) / creditsExtra(shortAllInput)
	subUplift := creditsSub(longAllInput) / creditsSub(shortAllInput)
	wantUplift := (300000.0 * 2) / 200000.0 // 2x rate, 1.5x tokens
	if math.Abs(extraUplift-wantUplift) > 0.0001 {
		t.Errorf("extra-usage uplift = %v, want %v", extraUplift, wantUplift)
	}
	if math.Abs(subUplift-wantUplift) > 0.0001 {
		t.Errorf("subscription uplift = %v, want %v", subUplift, wantUplift)
	}
}

func TestComputeExtraUsageCostFen_LongContext(t *testing.T) {
	m := &types.Model{
		Name: "gpt-5.4",
		DefaultCreditRate: &types.CreditRate{
			InputRate:     0.333,
			OutputRate:    2,
			CacheReadRate: 0.033,
			LongContext: &types.LongContextCreditRate{
				ThresholdInputTokens: 272000,
				InputMultiplier:      2,
				OutputMultiplier:     1.5,
			},
		},
	}

	cost, credits, err := computeExtraUsageCostFen(m, types.TokenUsage{
		InputTokens:     271001,
		OutputTokens:    1000,
		CacheReadTokens: 1000,
	}, 5438)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	wantCredits := (0.333*2)*271001 + (2.0*1.5)*1000 + (0.033*2)*1000
	if math.Abs(credits-wantCredits) > 0.001 {
		t.Fatalf("credits=%v, want %v", credits, wantCredits)
	}
	wantCost := int64(999)
	if cost != wantCost {
		t.Fatalf("cost=%d, want %d", cost, wantCost)
	}
}
