package admin

import (
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// PeriodSource enumerates the time-window provenance returned by
// resolveOverviewPeriod and surfaced via the `period_source` field.
const (
	PeriodSourceUser         = "user"
	PeriodSourceSubscription = "subscription"
	PeriodSourceDefault30d   = "default_30d"
)

// resolveOverviewPeriod decides what time window the project usage
// overview should reflect, and (for the active-sub case) which sub/plan
// drive the savings breakdown.
//
// Inputs:
//   - userProvidedWindow: true when the caller passed since or until on
//     the query string. We honor whatever they sent verbatim.
//   - defaultSince, defaultUntil: the fallback window if neither user nor
//     subscription supplies one (today: trailing 30 days).
//   - activeSub, activeSubPlan: resolved upstream by store calls. May be
//     nil independently. Both must be non-nil for the subscription period
//     to take effect (otherwise HasActiveSub would lie about the window).
//
// Outputs:
//   - since, until: window the caller should use for all queries.
//   - source: one of PeriodSource* — drives the JSON response field.
//   - sub, plan: pass-through, but cleared to nil when source is not
//     "subscription" so ComputeCostBreakdown sees the same provenance.
func resolveOverviewPeriod(
	userProvidedWindow bool,
	defaultSince, defaultUntil time.Time,
	activeSub *types.Subscription,
	activeSubPlan *types.Plan,
) (since, until time.Time, source string, sub *types.Subscription, plan *types.Plan) {
	if userProvidedWindow {
		return defaultSince, defaultUntil, PeriodSourceUser, nil, nil
	}
	if activeSub != nil && activeSubPlan != nil {
		return activeSub.StartsAt, activeSub.ExpiresAt, PeriodSourceSubscription, activeSub, activeSubPlan
	}
	return defaultSince, defaultUntil, PeriodSourceDefault30d, nil, nil
}
