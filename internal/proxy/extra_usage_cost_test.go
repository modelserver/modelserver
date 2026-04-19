package proxy

import (
	"errors"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestComputeExtraUsageCostFen_ZeroUsage(t *testing.T) {
	m := &types.Model{
		Name: "claude-opus-4-7",
		DefaultCreditRate: &types.CreditRate{
			InputRate:  0.667,
			OutputRate: 3.333,
		},
	}
	cost, credits, err := computeExtraUsageCostFen(m, types.TokenUsage{}, 5438)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cost != 0 || credits != 0 {
		t.Errorf("zero usage → cost=%d credits=%v, want (0, 0)", cost, credits)
	}
}

func TestComputeExtraUsageCostFen_OnlyCacheRead(t *testing.T) {
	m := &types.Model{
		Name: "claude-opus-4-7",
		DefaultCreditRate: &types.CreditRate{
			CacheReadRate: 0.1,
		},
	}
	cost, credits, err := computeExtraUsageCostFen(m,
		types.TokenUsage{CacheReadTokens: 10000}, 5438)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// credits = 0.1 * 10000 = 1000
	// cost_fen = ceil(1000 * 5438 / 1e6) = ceil(5.438) = 6
	if cost != 6 {
		t.Errorf("cost=%d, want 6", cost)
	}
	if credits != 1000 {
		t.Errorf("credits=%v, want 1000", credits)
	}
}

func TestComputeExtraUsageCostFen_CeilRoundsUp(t *testing.T) {
	m := &types.Model{
		Name: "x",
		DefaultCreditRate: &types.CreditRate{
			InputRate: 1,
		},
	}
	// 0.0001 credits → cost would be 0.00054 fen → must ceil to 1.
	cost, _, err := computeExtraUsageCostFen(m,
		types.TokenUsage{InputTokens: 1}, 1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cost != 1 {
		t.Errorf("sub-fen charge must round up, got cost=%d", cost)
	}
}

func TestComputeExtraUsageCostFen_MissingRate(t *testing.T) {
	_, _, err := computeExtraUsageCostFen(&types.Model{Name: "x"},
		types.TokenUsage{InputTokens: 1}, 5438)
	if !errors.Is(err, ErrMissingDefaultCreditRate) {
		t.Errorf("want ErrMissingDefaultCreditRate, got %v", err)
	}
}

func TestComputeExtraUsageCostFen_MixedTokens(t *testing.T) {
	m := &types.Model{
		Name: "claude-opus-4-7",
		DefaultCreditRate: &types.CreditRate{
			InputRate:         0.667,
			OutputRate:        3.333,
			CacheCreationRate: 0.667,
			CacheReadRate:     0,
		},
	}
	u := types.TokenUsage{
		InputTokens:         1000,
		OutputTokens:        500,
		CacheCreationTokens: 0,
		CacheReadTokens:     2000,
	}
	// credits = 0.667*1000 + 3.333*500 + 0 + 0 = 667 + 1666.5 = 2333.5
	// cost_fen = ceil(2333.5 * 5438 / 1e6) = ceil(12.69) = 13
	cost, _, err := computeExtraUsageCostFen(m, u, 5438)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cost != 13 {
		t.Errorf("cost=%d, want 13", cost)
	}
}
