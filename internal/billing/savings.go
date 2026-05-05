// Package billing — savings.go computes the per-project "API standard vs
// actual paid" breakdown surfaced in the project Overview page. Pure
// function so it stays cheaply unit-testable; the SQL aggregation lives
// in store.GetPerModelTokenSums.
package billing

import (
	"log/slog"
	"math"
	"time"

	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// CostBreakdown is the JSON-serializable shape consumed by the dashboard.
// All money fields are CNY fen.
type CostBreakdown struct {
	APIStandardFen  int64     `json:"api_standard_fen"`
	SubscriptionFen int64     `json:"subscription_fen"`
	ExtraUsageFen   int64     `json:"extra_usage_fen"`
	ActualPaidFen   int64     `json:"actual_paid_fen"`
	SavedFen        int64     `json:"saved_fen"`
	PeriodStart     time.Time `json:"period_start"`
	PeriodEnd       time.Time `json:"period_end"`
	HasActiveSub    bool      `json:"has_active_subscription"`
}

// ComputeCostBreakdown folds per-model token sums against the catalog's
// default credit rates to produce the equivalent API standard cost, then
// combines that with the active plan's PricePerPeriod and accumulated
// extra-usage spend.
//
// sub/plan may be nil (no active subscription); fallbackStart/fallbackEnd
// are used as the period in that case. Long-context multipliers are NOT
// applied in this v1; see spec §VII for the known limitation.
func ComputeCostBreakdown(
	sums []store.PerModelTokenSums,
	extraUsageFen int64,
	catalog modelcatalog.Catalog,
	creditPriceFen int64,
	sub *types.Subscription,
	plan *types.Plan,
	fallbackStart, fallbackEnd time.Time,
) CostBreakdown {
	var apiFen int64
	for _, s := range sums {
		m, ok := catalog.Lookup(s.Model)
		if !ok || m.DefaultCreditRate == nil {
			slog.Warn("savings: missing default credit rate, skipping model",
				"model", s.Model, "rows", s.RequestCount)
			continue
		}
		r := m.DefaultCreditRate
		credits := r.InputRate*float64(s.InputTokens) +
			r.OutputRate*float64(s.OutputTokens) +
			r.CacheCreationRate*float64(s.CacheCreationTokens) +
			r.CacheReadRate*float64(s.CacheReadTokens)
		// Per-model ceil so rounding never under-states the API standard cost.
		apiFen += int64(math.Ceil(credits * float64(creditPriceFen) / 1_000_000))
	}

	out := CostBreakdown{
		APIStandardFen: apiFen,
		ExtraUsageFen:  extraUsageFen,
	}
	if sub != nil && plan != nil {
		out.HasActiveSub = true
		out.SubscriptionFen = plan.PricePerPeriod
		out.PeriodStart = sub.StartsAt
		out.PeriodEnd = sub.ExpiresAt
	} else {
		out.PeriodStart = fallbackStart
		out.PeriodEnd = fallbackEnd
	}
	out.ActualPaidFen = out.SubscriptionFen + out.ExtraUsageFen
	if diff := out.APIStandardFen - out.ActualPaidFen; diff > 0 {
		out.SavedFen = diff
	}
	return out
}
