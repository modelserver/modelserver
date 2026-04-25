package admin

import (
	"math"
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/store"
)

func TestInferLimitFromTotalCredits(t *testing.T) {
	reset := time.Unix(1_700_000_000, 0)
	snaps := []store.UtilizationSnapshot{
		{OfficialPct: 10, TotalCredits: 1_100_000, ResetsAt: &reset},
		{OfficialPct: 25, TotalCredits: 2_750_000, ResetsAt: &reset},
		{OfficialPct: 50, TotalCredits: 5_500_000, ResetsAt: &reset},
	}

	got := inferLimitFromTotalCredits(snaps)
	if got == nil {
		t.Fatal("inferLimitFromTotalCredits returned nil")
	}
	if math.Abs(got.InferredLimit-11_000_000) > 0.5 {
		t.Fatalf("InferredLimit = %v, want 11000000", got.InferredLimit)
	}
	if got.RMSEPct != 0 {
		t.Fatalf("RMSEPct = %v, want 0", got.RMSEPct)
	}
}

func TestInferLimitFromTotalCreditsNoUsableCredits(t *testing.T) {
	got := inferLimitFromTotalCredits([]store.UtilizationSnapshot{
		{OfficialPct: 10, TotalCredits: 0},
		{OfficialPct: 20, TotalCredits: -1},
	})
	if got != nil {
		t.Fatalf("inferLimitFromTotalCredits = %+v, want nil", got)
	}
}
