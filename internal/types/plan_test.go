package types

import (
	"testing"
	"time"
)

func TestToPolicy_InjectsAnchorTimeForFixedRules(t *testing.T) {
	anchor := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	plan := &Plan{
		ID:   "plan-1",
		Name: "test",
		CreditRules: []CreditRule{
			{Window: "7d", WindowType: WindowTypeFixed, MaxCredits: 500000},
			{Window: "5h", WindowType: WindowTypeSliding, MaxCredits: 50000},
		},
	}

	policy := plan.ToPolicy("proj-1", &anchor)

	if policy.CreditRules[0].AnchorTime == nil {
		t.Fatal("fixed rule AnchorTime should not be nil")
	}
	if !policy.CreditRules[0].AnchorTime.Equal(anchor) {
		t.Errorf("fixed rule AnchorTime = %v, want %v", *policy.CreditRules[0].AnchorTime, anchor)
	}
	if policy.CreditRules[1].AnchorTime != nil {
		t.Errorf("sliding rule AnchorTime should be nil, got %v", *policy.CreditRules[1].AnchorTime)
	}
	if plan.CreditRules[0].AnchorTime != nil {
		t.Error("original plan CreditRules was mutated — ToPolicy must copy the slice")
	}
}

func TestToPolicy_NilStartsAt(t *testing.T) {
	plan := &Plan{
		ID:   "plan-2",
		Name: "test",
		CreditRules: []CreditRule{
			{Window: "7d", WindowType: WindowTypeFixed, MaxCredits: 500000},
		},
	}

	policy := plan.ToPolicy("proj-1", nil)

	if policy.CreditRules[0].AnchorTime != nil {
		t.Errorf("AnchorTime should be nil when subscriptionStartsAt is nil")
	}
}
