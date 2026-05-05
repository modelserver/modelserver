package admin

import (
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

func TestResolveOverviewPeriod(t *testing.T) {
	defStart := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	defEnd := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	subStart := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	subEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	sub := &types.Subscription{StartsAt: subStart, ExpiresAt: subEnd, PlanID: "p1"}
	plan := &types.Plan{ID: "p1", PricePerPeriod: 19900}

	cases := []struct {
		name         string
		userWindow   bool
		sub          *types.Subscription
		plan         *types.Plan
		wantStart    time.Time
		wantEnd      time.Time
		wantSource   string
		wantSubSet   bool // true if returned sub/plan should be non-nil
	}{
		{
			name:       "user-supplied window beats everything",
			userWindow: true, sub: sub, plan: plan,
			wantStart: defStart, wantEnd: defEnd,
			wantSource: PeriodSourceUser, wantSubSet: false,
		},
		{
			name:       "active sub + plan → subscription period",
			userWindow: false, sub: sub, plan: plan,
			wantStart: subStart, wantEnd: subEnd,
			wantSource: PeriodSourceSubscription, wantSubSet: true,
		},
		{
			name:       "active sub but plan missing → default fallback",
			userWindow: false, sub: sub, plan: nil,
			wantStart: defStart, wantEnd: defEnd,
			wantSource: PeriodSourceDefault30d, wantSubSet: false,
		},
		{
			name:       "no sub at all → default fallback",
			userWindow: false, sub: nil, plan: nil,
			wantStart: defStart, wantEnd: defEnd,
			wantSource: PeriodSourceDefault30d, wantSubSet: false,
		},
		{
			name:       "user window forces default even without sub",
			userWindow: true, sub: nil, plan: nil,
			wantStart: defStart, wantEnd: defEnd,
			wantSource: PeriodSourceUser, wantSubSet: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStart, gotEnd, gotSrc, gotSub, gotPlan := resolveOverviewPeriod(
				tc.userWindow, defStart, defEnd, tc.sub, tc.plan)
			if !gotStart.Equal(tc.wantStart) || !gotEnd.Equal(tc.wantEnd) {
				t.Errorf("window = [%v, %v], want [%v, %v]",
					gotStart, gotEnd, tc.wantStart, tc.wantEnd)
			}
			if gotSrc != tc.wantSource {
				t.Errorf("source = %q, want %q", gotSrc, tc.wantSource)
			}
			if (gotSub != nil) != tc.wantSubSet {
				t.Errorf("sub returned = %v, want non-nil=%v", gotSub, tc.wantSubSet)
			}
			if (gotPlan != nil) != tc.wantSubSet {
				t.Errorf("plan returned = %v, want non-nil=%v", gotPlan, tc.wantSubSet)
			}
		})
	}
}
