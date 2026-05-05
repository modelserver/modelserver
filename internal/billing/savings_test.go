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
	plan := &types.Plan{PricePerPeriod: 19900} // ¥199.00

	sums := []store.PerModelTokenSums{{
		Model: "claude-sonnet-4-6",
		// 1M input, 1M output → credits = 3*1e6 + 15*1e6 = 18e6
		// fen = ceil(18e6 * 5438 / 1e6) = ceil(97884) = 97884 → ¥978.84
		InputTokens: 1_000_000, OutputTokens: 1_000_000,
	}}

	got := ComputeCostBreakdown(sums, 0, cat, 5438, sub, plan, time.Time{}, time.Time{})

	if got.APIStandardFen != 97884 {
		t.Errorf("APIStandardFen = %d, want 97884", got.APIStandardFen)
	}
	if got.SubscriptionFen != 19900 {
		t.Errorf("SubscriptionFen = %d, want 19900", got.SubscriptionFen)
	}
	if got.ExtraUsageFen != 0 {
		t.Errorf("ExtraUsageFen = %d, want 0", got.ExtraUsageFen)
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

	got := ComputeCostBreakdown(sums, 1234, cat, 5438, nil, nil, fallbackStart, fallbackEnd)

	if got.HasActiveSub {
		t.Errorf("HasActiveSub = true, want false")
	}
	if got.SubscriptionFen != 0 {
		t.Errorf("SubscriptionFen = %d, want 0", got.SubscriptionFen)
	}
	if got.ActualPaidFen != 1234 {
		t.Errorf("ActualPaidFen = %d, want 1234", got.ActualPaidFen)
	}
	if got.SavedFen != 97884-1234 {
		t.Errorf("SavedFen = %d, want %d", got.SavedFen, 97884-1234)
	}
	if !got.PeriodStart.Equal(fallbackStart) || !got.PeriodEnd.Equal(fallbackEnd) {
		t.Errorf("fallback period not used")
	}
}

func TestComputeCostBreakdown_LowUsageClampsSavedToZero(t *testing.T) {
	cat := newTestCatalog()
	sub := &types.Subscription{Status: types.SubscriptionStatusActive,
		StartsAt: time.Now(), ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	plan := &types.Plan{PricePerPeriod: 19900}

	// Tiny usage: 100 input tokens → credits = 300, fen = ceil(300*5438/1e6)=2
	sums := []store.PerModelTokenSums{{Model: "claude-sonnet-4-6", InputTokens: 100}}

	got := ComputeCostBreakdown(sums, 0, cat, 5438, sub, plan, time.Time{}, time.Time{})

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
	got := ComputeCostBreakdown(sums, 0, cat, 5438, nil, nil, time.Time{}, time.Time{})

	// Only claude row contributes: 1e6 input * 3 = 3e6 credits → ceil(3e6*5438/1e6)=16314
	if got.APIStandardFen != 16314 {
		t.Errorf("APIStandardFen = %d, want 16314 (unknown rows skipped)", got.APIStandardFen)
	}
}

func TestComputeCostBreakdown_ExtraUsageOnlyCountedThroughExtraField(t *testing.T) {
	cat := newTestCatalog()
	sub := &types.Subscription{Status: types.SubscriptionStatusActive,
		StartsAt: time.Now(), ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	plan := &types.Plan{PricePerPeriod: 0} // free plan

	sums := []store.PerModelTokenSums{{Model: "claude-sonnet-4-6",
		InputTokens: 1_000_000, OutputTokens: 1_000_000}}
	extra := int64(50_000) // ¥500.00

	got := ComputeCostBreakdown(sums, extra, cat, 5438, sub, plan, time.Time{}, time.Time{})

	if got.ExtraUsageFen != 50_000 {
		t.Errorf("ExtraUsageFen = %d, want 50000", got.ExtraUsageFen)
	}
	if got.ActualPaidFen != 50_000 {
		t.Errorf("ActualPaidFen = %d, want 50000", got.ActualPaidFen)
	}
	// API standard 97884 − actual 50000 = 47884
	if got.SavedFen != 47884 {
		t.Errorf("SavedFen = %d, want 47884", got.SavedFen)
	}
}
