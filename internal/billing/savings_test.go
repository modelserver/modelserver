package billing

import (
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func newTestCatalog() modelcatalog.Catalog {
	return modelcatalog.New([]types.Model{
		{
			Name:   "claude-sonnet-4-6",
			Status: types.ModelStatusActive,
			DefaultCreditRate: &types.CreditRate{
				InputRate: 3, OutputRate: 15, CacheCreationRate: 3.75, CacheReadRate: 0.30,
			},
		},
		{
			Name:              "no-rate-model",
			Status:            types.ModelStatusActive,
			DefaultCreditRate: nil,
		},
	})
}

func TestComputeCostBreakdown_PaidPlanWithSavings(t *testing.T) {
	cat := newTestCatalog()
	sub := &types.Subscription{ID: "s1", Status: types.SubscriptionStatusActive,
		StartsAt:  time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		ExpiresAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}
	plan := &types.Plan{PriceCNYFen: 19900} // ¥199.00

	sums := []store.PerModelTokenSums{{
		Model: "claude-sonnet-4-6",
		// 1M input, 1M output → credits = 3*1e6 + 15*1e6 = 18e6
		// fen = ceil(18e6 * 5438 / 1e6) = ceil(97884) = 97884 → ¥978.84
		InputTokens: 1_000_000, OutputTokens: 1_000_000,
	}}

	got := ComputeCostBreakdown(sums, 0, cat, 5438, sub, plan, time.Time{}, time.Time{}, "CNY")

	if got.APIStandardFen != 97884 {
		t.Errorf("APIStandardFen = %d, want 97884", got.APIStandardFen)
	}
	if got.SubscriptionFen != 19900 {
		t.Errorf("SubscriptionFen = %d, want 19900", got.SubscriptionFen)
	}
	if got.ExtraUsageCredits != 0 {
		t.Errorf("ExtraUsageCredits = %d, want 0", got.ExtraUsageCredits)
	}
	if got.ActualPaidFen != 19900 {
		t.Errorf("ActualPaidFen = %d, want 19900", got.ActualPaidFen)
	}
	if got.SavedFen != 77984 {
		t.Errorf("SavedFen = %d, want 77984", got.SavedFen)
	}
	if !got.HasActiveSub {
		t.Errorf("HasActiveSub = false, want true")
	}
	if !got.PeriodStart.Equal(sub.StartsAt) || !got.PeriodEnd.Equal(sub.ExpiresAt) {
		t.Errorf("period mismatch: got [%v, %v]", got.PeriodStart, got.PeriodEnd)
	}
}

func TestComputeCostBreakdown_NoActiveSubscription(t *testing.T) {
	cat := newTestCatalog()
	sums := []store.PerModelTokenSums{{
		Model:       "claude-sonnet-4-6",
		InputTokens: 1_000_000, OutputTokens: 1_000_000,
	}}
	fallbackStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	fallbackEnd := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	// 5_000_000 credits × 5438 fen/M = 27190 fen equivalent
	extraCredits := int64(5_000_000)
	got := ComputeCostBreakdown(sums, extraCredits, cat, 5438, nil, nil, fallbackStart, fallbackEnd, "")

	if got.HasActiveSub {
		t.Errorf("HasActiveSub = true, want false")
	}
	if got.SubscriptionFen != 0 {
		t.Errorf("SubscriptionFen = %d, want 0", got.SubscriptionFen)
	}
	if got.ExtraUsageCredits != extraCredits {
		t.Errorf("ExtraUsageCredits = %d, want %d", got.ExtraUsageCredits, extraCredits)
	}
	// actual_paid_fen = (5_000_000 * 5438) / 1_000_000 = 27190
	if got.ActualPaidFen != 27190 {
		t.Errorf("ActualPaidFen = %d, want 27190", got.ActualPaidFen)
	}
	// api_standard_fen = 97884; saved = 97884 - 27190 = 70694
	if got.SavedFen != 97884-27190 {
		t.Errorf("SavedFen = %d, want %d", got.SavedFen, 97884-27190)
	}
	if !got.PeriodStart.Equal(fallbackStart) || !got.PeriodEnd.Equal(fallbackEnd) {
		t.Errorf("fallback period not used")
	}
}

func TestComputeCostBreakdown_LowUsageClampsSavedToZero(t *testing.T) {
	cat := newTestCatalog()
	sub := &types.Subscription{Status: types.SubscriptionStatusActive,
		StartsAt: time.Now(), ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	plan := &types.Plan{PriceCNYFen: 19900}

	// Tiny usage: 100 input tokens → credits = 300, fen = ceil(300*5438/1e6)=2
	sums := []store.PerModelTokenSums{{Model: "claude-sonnet-4-6", InputTokens: 100}}

	got := ComputeCostBreakdown(sums, 0, cat, 5438, sub, plan, time.Time{}, time.Time{}, "CNY")

	if got.APIStandardFen != 2 {
		t.Errorf("APIStandardFen = %d, want 2", got.APIStandardFen)
	}
	if got.SavedFen != 0 {
		t.Errorf("SavedFen = %d, want 0 (clamped)", got.SavedFen)
	}
}

func TestComputeCostBreakdown_UnknownModelSkipped(t *testing.T) {
	cat := newTestCatalog()
	sums := []store.PerModelTokenSums{
		{Model: "claude-sonnet-4-6", InputTokens: 1_000_000},    // counted
		{Model: "totally-unknown", InputTokens: 1_000_000_000},  // skipped
		{Model: "no-rate-model", InputTokens: 1_000_000_000},    // skipped (DefaultCreditRate==nil)
	}
	got := ComputeCostBreakdown(sums, 0, cat, 5438, nil, nil, time.Time{}, time.Time{}, "")

	// Only claude row contributes: 1e6 input * 3 = 3e6 credits → ceil(3e6*5438/1e6)=16314
	if got.APIStandardFen != 16314 {
		t.Errorf("APIStandardFen = %d, want 16314 (unknown rows skipped)", got.APIStandardFen)
	}
}

func TestComputeCostBreakdown_CacheRatesAndMultipleModels(t *testing.T) {
	cat := modelcatalog.New([]types.Model{
		{Name: "claude-sonnet-4-6", Status: types.ModelStatusActive,
			DefaultCreditRate: &types.CreditRate{
				InputRate: 3, OutputRate: 15, CacheCreationRate: 3.75, CacheReadRate: 0.30,
			}},
		{Name: "tiny-model", Status: types.ModelStatusActive,
			DefaultCreditRate: &types.CreditRate{InputRate: 1, OutputRate: 2}},
	})

	sums := []store.PerModelTokenSums{
		// Exercises CacheCreation + CacheRead rate paths (the only test case that does).
		// credits = 1e6*3.75 + 1e6*0.30 = 4.05e6 → ceil(4.05e6 * 5438 / 1e6) = 22024
		{Model: "claude-sonnet-4-6", CacheCreationTokens: 1_000_000, CacheReadTokens: 1_000_000},
		// Second counted model — proves the per-model accumulator across rows.
		// credits = 5e5*1 + 1e5*2 = 7e5 → ceil(7e5 * 5438 / 1e6) = 3807
		{Model: "tiny-model", InputTokens: 500_000, OutputTokens: 100_000},
	}

	got := ComputeCostBreakdown(sums, 0, cat, 5438, nil, nil, time.Time{}, time.Time{}, "")

	if got.APIStandardFen != 22024+3807 {
		t.Errorf("APIStandardFen = %d, want %d (22024 from claude cache + 3807 from tiny)",
			got.APIStandardFen, 22024+3807)
	}
}

func TestComputeCostBreakdown_SavedZeroAtExactBreakeven(t *testing.T) {
	cat := newTestCatalog()
	sub := &types.Subscription{Status: types.SubscriptionStatusActive,
		StartsAt: time.Now(), ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	// 1M input tokens with InputRate=3 → credits=3e6 → fen = 3e6*5438/1e6 = 16314 exactly.
	// Set plan price to the same value so APIStandard == ActualPaid exactly.
	plan := &types.Plan{PriceCNYFen: 16314}
	sums := []store.PerModelTokenSums{{Model: "claude-sonnet-4-6", InputTokens: 1_000_000}}

	got := ComputeCostBreakdown(sums, 0, cat, 5438, sub, plan, time.Time{}, time.Time{}, "CNY")

	if got.APIStandardFen != 16314 || got.ActualPaidFen != 16314 {
		t.Fatalf("breakeven setup wrong: api=%d paid=%d", got.APIStandardFen, got.ActualPaidFen)
	}
	if got.SavedFen != 0 {
		t.Errorf("SavedFen = %d, want 0 at exact breakeven", got.SavedFen)
	}
}

func TestComputeCostBreakdown_NegativeRateClampedToZero(t *testing.T) {
	// A misconfigured negative rate must not push APIStandardFen negative.
	cat := modelcatalog.New([]types.Model{
		{Name: "buggy-model", Status: types.ModelStatusActive,
			DefaultCreditRate: &types.CreditRate{InputRate: -5, OutputRate: -1}},
		{Name: "good-model", Status: types.ModelStatusActive,
			DefaultCreditRate: &types.CreditRate{InputRate: 1}},
	})
	sums := []store.PerModelTokenSums{
		{Model: "buggy-model", InputTokens: 1_000_000, OutputTokens: 1_000_000},
		// good-model: 1M input * 1 = 1e6 credits → ceil(1e6 * 5438 / 1e6) = 5438
		{Model: "good-model", InputTokens: 1_000_000},
	}
	got := ComputeCostBreakdown(sums, 0, cat, 5438, nil, nil, time.Time{}, time.Time{}, "")
	if got.APIStandardFen != 5438 {
		t.Errorf("APIStandardFen = %d, want 5438 (buggy-model clamped, good-model = 5438)",
			got.APIStandardFen)
	}
}

func TestComputeCostBreakdown_ExtraUsageOnlyCountedThroughExtraField(t *testing.T) {
	cat := newTestCatalog()
	sub := &types.Subscription{Status: types.SubscriptionStatusActive,
		StartsAt: time.Now(), ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	plan := &types.Plan{PriceCNYFen: 0} // free plan

	sums := []store.PerModelTokenSums{{Model: "claude-sonnet-4-6",
		InputTokens: 1_000_000, OutputTokens: 1_000_000}}
	// 9_193_219 credits × 5438 / 1_000_000 = 49,994 fen (~¥499.94)
	// Using a round credit count: 10_000_000 credits × 5438 / 1M = 54_380 fen
	// Use 9_194_558 credits × 5438 / 1M = floor(50001) -- let's pick something clean.
	// 50_000 fen / 5438 * 1_000_000 ≈ 9_194_558 credits → (9_194_558 * 5438)/1M = 49_999
	// Simplest: use 10_000_000 credits → 54_380 fen; api_std = 97884; saved = 97884-54380 = 43504
	extra := int64(10_000_000) // 10M credits × 5438 fen/M = 54380 fen

	got := ComputeCostBreakdown(sums, extra, cat, 5438, sub, plan, time.Time{}, time.Time{}, "")

	if got.ExtraUsageCredits != extra {
		t.Errorf("ExtraUsageCredits = %d, want %d", got.ExtraUsageCredits, extra)
	}
	// actual_paid_fen = (10_000_000 * 5438) / 1_000_000 = 54380
	if got.ActualPaidFen != 54380 {
		t.Errorf("ActualPaidFen = %d, want 54380", got.ActualPaidFen)
	}
	// API standard 97884 − actual 54380 = 43504
	if got.SavedFen != 43504 {
		t.Errorf("SavedFen = %d, want 43504", got.SavedFen)
	}
}

func TestComputeCostBreakdown_ExtraUsageCreditsConverted(t *testing.T) {
	// 5,000,000 credits × 5438 fen / 1M = 27,190 fen
	cb := ComputeCostBreakdown(
		nil, 5_000_000, nil, 5438, nil, nil,
		time.Now(), time.Now(), "",
	)
	if cb.ExtraUsageCredits != 5_000_000 {
		t.Errorf("ExtraUsageCredits = %d, want 5_000_000", cb.ExtraUsageCredits)
	}
	if cb.ActualPaidFen != 27190 {
		t.Errorf("ActualPaidFen = %d, want 27190", cb.ActualPaidFen)
	}
}
