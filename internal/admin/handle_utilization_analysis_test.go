package admin

import (
	"math"
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func TestSuggestRatesForFixedLimit(t *testing.T) {
	reset := time.Unix(1_700_000_000, 0)
	snaps := []store.UtilizationSnapshot{
		{OfficialPct: 10, TotalCredits: 1_100_000, ResetsAt: &reset},
		{OfficialPct: 25, TotalCredits: 2_750_000, ResetsAt: &reset},
		{OfficialPct: 50, TotalCredits: 5_500_000, ResetsAt: &reset},
	}

	got := suggestRatesForFixedLimit(snaps, "5h", utilizationAnalysisBaseRates)
	if got == nil {
		t.Fatal("suggestRatesForFixedLimit returned nil")
	}
	if math.Abs(got.KnownLimit-11_000_000) > 0.5 {
		t.Fatalf("KnownLimit = %v, want 11000000", got.KnownLimit)
	}
	if math.Abs(got.SuggestedRateMultiplier-1) > 0.000001 {
		t.Fatalf("SuggestedRateMultiplier = %v, want 1", got.SuggestedRateMultiplier)
	}
	if math.Abs(got.TargetCredits-9_350_000) > 0.5 {
		t.Fatalf("TargetCredits = %v, want 9350000", got.TargetCredits)
	}
	if got.RMSEPct != 0 {
		t.Fatalf("RMSEPct = %v, want 0", got.RMSEPct)
	}
}

func TestSuggestRatesForFixedLimitScalesKnownRates(t *testing.T) {
	got := suggestRatesForFixedLimit([]store.UtilizationSnapshot{
		{
			OfficialPct:  6,
			TotalCredits: 10_097_386.538,
			ModelBreakdown: map[string]*store.UpstreamTokenBreakdown{
				"gpt-5.5": {},
			},
		},
	}, "5h", map[string]types.CreditRate{
		"gpt-5.5": {InputRate: 0.667, OutputRate: 4, CacheCreationRate: 0, CacheReadRate: 0.067},
	})
	if got == nil {
		t.Fatal("suggestRatesForFixedLimit returned nil")
	}
	if math.Abs(got.SuggestedRateMultiplier-0.065363) > 0.000001 {
		t.Fatalf("SuggestedRateMultiplier = %v, want 0.065363", got.SuggestedRateMultiplier)
	}
	rate := got.SuggestedRates["gpt-5.5"]
	if math.Abs(rate.InputRate-0.043596) > 0.000001 {
		t.Fatalf("InputRate = %v, want 0.043596", rate.InputRate)
	}
	if math.Abs(rate.OutputRate-0.261454) > 0.000001 {
		t.Fatalf("OutputRate = %v, want 0.261454", rate.OutputRate)
	}
}

func TestUtilizationAnalysisBaseRates_GPT55SubscriptionDiscount(t *testing.T) {
	rate := utilizationAnalysisBaseRates["gpt-5.5"]
	if math.Abs(rate.InputRate-0.044) > 0.000001 {
		t.Fatalf("InputRate = %v, want subscription discount 0.044", rate.InputRate)
	}
	if math.Abs(rate.OutputRate-0.261) > 0.000001 {
		t.Fatalf("OutputRate = %v, want subscription discount 0.261", rate.OutputRate)
	}
	if math.Abs(rate.CacheReadRate-0.0044) > 0.000001 {
		t.Fatalf("CacheReadRate = %v, want subscription discount 0.0044", rate.CacheReadRate)
	}
	if rate.LongContext == nil {
		t.Fatal("LongContext is nil")
	}
	if rate.LongContext.ThresholdInputTokens != 272000 ||
		rate.LongContext.InputMultiplier != 2.0 ||
		rate.LongContext.OutputMultiplier != 1.5 {
		t.Fatalf("LongContext = %+v, want 272000/2.0/1.5", rate.LongContext)
	}
}

func TestScaleCreditRatePreservesLongContext(t *testing.T) {
	rate := scaleCreditRate(types.CreditRate{
		InputRate:  0.667,
		OutputRate: 4,
		LongContext: &types.LongContextCreditRate{
			ThresholdInputTokens: 272000,
			InputMultiplier:      2,
			OutputMultiplier:     1.5,
		},
	}, 0.5)
	if rate.LongContext == nil {
		t.Fatal("LongContext is nil")
	}
	if rate.LongContext.ThresholdInputTokens != 272000 ||
		rate.LongContext.InputMultiplier != 2 ||
		rate.LongContext.OutputMultiplier != 1.5 {
		t.Fatalf("LongContext = %+v", rate.LongContext)
	}
}

func TestSuggestRatesForFixedLimitNoUsableCredits(t *testing.T) {
	got := suggestRatesForFixedLimit([]store.UtilizationSnapshot{
		{OfficialPct: 10, TotalCredits: 0},
		{OfficialPct: 20, TotalCredits: -1},
	}, "5h", utilizationAnalysisBaseRates)
	if got != nil {
		t.Fatalf("suggestRatesForFixedLimit = %+v, want nil", got)
	}
}
