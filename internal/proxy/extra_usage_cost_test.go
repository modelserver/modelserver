package proxy

import (
	"errors"
	"math"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestComputeExtraUsageCostCredits_ZeroUsage(t *testing.T) {
	m := &types.Model{
		Name: "claude-opus-4-7",
		DefaultCreditRate: &types.CreditRate{
			InputRate:  0.667,
			OutputRate: 3.333,
		},
	}
	cost, err := computeExtraUsageCostCredits(m, types.TokenUsage{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cost != 0 {
		t.Errorf("zero usage → cost=%d, want 0", cost)
	}
}

func TestComputeExtraUsageCostCredits_OnlyCacheRead(t *testing.T) {
	m := &types.Model{
		Name: "claude-opus-4-7",
		DefaultCreditRate: &types.CreditRate{
			CacheReadRate: 0.1,
		},
	}
	cost, err := computeExtraUsageCostCredits(m,
		types.TokenUsage{CacheReadTokens: 10000})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// credits = 0.1 * 10000 = 1000; cost_credits = ceil(1000) = 1000
	if cost != 1000 {
		t.Errorf("cost=%d, want 1000", cost)
	}
}

func TestComputeExtraUsageCostCredits_CeilRoundsUp(t *testing.T) {
	m := &types.Model{
		Name: "x",
		DefaultCreditRate: &types.CreditRate{
			InputRate: 0.5,
		},
	}
	// 0.5 * 1 = 0.5 credits → must ceil to 1.
	cost, err := computeExtraUsageCostCredits(m,
		types.TokenUsage{InputTokens: 1})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cost != 1 {
		t.Errorf("sub-credit charge must round up, got cost=%d", cost)
	}
}

func TestComputeExtraUsageCostCredits_MissingRate(t *testing.T) {
	_, err := computeExtraUsageCostCredits(&types.Model{Name: "x"},
		types.TokenUsage{InputTokens: 1})
	if !errors.Is(err, ErrMissingDefaultCreditRate) {
		t.Errorf("want ErrMissingDefaultCreditRate, got %v", err)
	}
}

func TestComputeExtraUsageCostCredits_MixedTokens(t *testing.T) {
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
	// cost_credits = ceil(2333.5) = 2334
	cost, err := computeExtraUsageCostCredits(m, u)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cost != 2334 {
		t.Errorf("cost=%d, want 2334", cost)
	}
}

// TestGPT55_ExtraUsageVsSubscription_LongContext pins the two-tier pricing
// shape against drift:
//   - extra-usage path (computeExtraUsageCostCredits) reads catalog → official rate
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

	policy := &types.RateLimitPolicy{
		ModelCreditRates: map[string]types.CreditRate{"gpt-5.5": subscriptionPlan},
	}

	type usage struct {
		in, out, cacheRead int64
	}
	short := usage{in: 200000, out: 1000, cacheRead: 1000} // total in 201K ≤ 272K
	long := usage{in: 300000, out: 1000, cacheRead: 1000}  // total in 301K > 272K

	creditsExtraRaw := func(u usage) float64 {
		// compute raw float credits (without ceil) for comparison
		rate := types.ApplyLongContextCreditRate(*officialCatalog, u.in+u.cacheRead)
		return rate.InputRate*float64(u.in) +
			rate.OutputRate*float64(u.out) +
			rate.CacheReadRate*float64(u.cacheRead)
	}
	creditsSub := func(u usage) float64 {
		return policy.ComputeCreditsWithDefault("gpt-5.5", nil, u.in, u.out, 0, u.cacheRead)
	}

	tests := []struct {
		name       string
		got, want  float64
	}{
		{"extra short", creditsExtraRaw(short), 0.667*200000 + 4.0*1000 + 0.067*1000},
		{"extra long", creditsExtraRaw(long), (0.667*2)*300000 + (4.0*1.5)*1000 + (0.067*2)*1000},
		{"sub short", creditsSub(short), 0.044*200000 + 0.261*1000 + 0.0044*1000},
		{"sub long", creditsSub(long), (0.044*2)*300000 + (0.261*1.5)*1000 + (0.0044*2)*1000},
	}
	for _, tc := range tests {
		if math.Abs(tc.got-tc.want) > 0.001 {
			t.Errorf("%s: credits=%v want %v", tc.name, tc.got, tc.want)
		}
	}

	// long_context multipliers must apply identically on both pricing tiers.
	shortAllInput := usage{in: 200000}
	longAllInput := usage{in: 300000}
	extraUplift := creditsExtraRaw(longAllInput) / creditsExtraRaw(shortAllInput)
	subUplift := creditsSub(longAllInput) / creditsSub(shortAllInput)
	wantUplift := (300000.0 * 2) / 200000.0 // 2x rate, 1.5x tokens
	if math.Abs(extraUplift-wantUplift) > 0.0001 {
		t.Errorf("extra-usage uplift = %v, want %v", extraUplift, wantUplift)
	}
	if math.Abs(subUplift-wantUplift) > 0.0001 {
		t.Errorf("subscription uplift = %v, want %v", subUplift, wantUplift)
	}
}

func TestComputeExtraUsageCostCredits_LongContext(t *testing.T) {
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

	cost, err := computeExtraUsageCostCredits(m, types.TokenUsage{
		InputTokens:     271001,
		OutputTokens:    1000,
		CacheReadTokens: 1000,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	wantCredits := (0.333*2)*271001 + (2.0*1.5)*1000 + (0.033*2)*1000
	wantCost := int64(math.Ceil(wantCredits))
	if cost != wantCost {
		t.Fatalf("cost=%d, want %d (%.2f raw credits)", cost, wantCost, wantCredits)
	}
}
