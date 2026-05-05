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
