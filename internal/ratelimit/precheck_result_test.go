package ratelimit

import (
	"testing"
	"time"
)

// These tests pin the LimitType classification of PreCheckResult without
// needing a real DB / policy — they verify the constants downstream callers
// branch on and the zero-value semantics.

func TestPreCheckResult_ZeroValueAllowed(t *testing.T) {
	var r PreCheckResult
	if r.Allowed {
		t.Errorf("zero-value PreCheckResult is not Allowed by default")
	}
}

func TestPreCheckResult_AllowedHasEmptyLimit(t *testing.T) {
	r := PreCheckResult{Allowed: true}
	if r.LimitType != LimitTypeNone {
		t.Errorf("allowed result must have empty LimitType")
	}
	if r.RetryAfter != 0 {
		t.Errorf("allowed result must have zero RetryAfter")
	}
}

func TestPreCheckResult_LimitConstants(t *testing.T) {
	if LimitTypeCredit != "credit" {
		t.Errorf("LimitTypeCredit=%q, want credit (downstream proxy layer depends on the literal)", LimitTypeCredit)
	}
	if LimitTypeClassic != "classic" {
		t.Errorf("LimitTypeClassic=%q, want classic", LimitTypeClassic)
	}
}

func TestPreCheckResult_Classic429Shape(t *testing.T) {
	r := PreCheckResult{Allowed: false, RetryAfter: 60 * time.Second, LimitType: LimitTypeClassic}
	if r.Allowed {
		t.Errorf("classic hit must deny")
	}
	if r.LimitType != LimitTypeClassic {
		t.Errorf("classic-hit LimitType wrong")
	}
}
