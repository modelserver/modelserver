package proxy

import (
	"errors"
	"math"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestComputeImageExtraUsageCostCredits_ImageOutputOnly(t *testing.T) {
	m := &types.Model{DefaultImageCreditRate: &types.ImageCreditRate{ImageOutputRate: 30}}
	cost, err := computeImageExtraUsageCostCredits(m, ImageTokenUsage{ImageOutputTokens: 1000, OutputTokens: 1000})
	if err != nil {
		t.Fatalf("computeImageExtraUsageCostCredits: %v", err)
	}
	// credits = 30 * 1000 = 30000; cost_credits = ceil(30000) = 30000
	if cost != 30000 {
		t.Fatalf("cost = %d, want 30000", cost)
	}
}

func TestComputeImageExtraUsageCostCredits_ProportionalCachedSplit(t *testing.T) {
	m := &types.Model{DefaultImageCreditRate: &types.ImageCreditRate{
		TextInputRate: 10, TextCachedInputRate: 1,
		ImageInputRate: 20, ImageCachedInputRate: 2,
		TextOutputRate: 30, ImageOutputRate: 40,
	}}
	u := ImageTokenUsage{
		TextInputTokens:   40,
		ImageInputTokens:  60,
		CachedInputTokens: 25,
		TextOutputTokens:  2,
		ImageOutputTokens: 3,
		InputTokens:       100,
		OutputTokens:      5,
	}
	cost, err := computeImageExtraUsageCostCredits(m, u)
	if err != nil {
		t.Fatalf("computeImageExtraUsageCostCredits: %v", err)
	}
	// cachedText=10, cachedImage=15, billedText=30, billedImage=45
	wantCredits := float64(10*30 + 20*45 + 1*10 + 2*15 + 30*2 + 40*3)
	wantCost := int64(math.Ceil(wantCredits))
	if cost != wantCost {
		t.Fatalf("cost = %d, want %d (%.2f raw credits)", cost, wantCost, wantCredits)
	}
}

func TestComputeImageExtraUsageCostCredits_MissingRate(t *testing.T) {
	_, err := computeImageExtraUsageCostCredits(&types.Model{}, ImageTokenUsage{OutputTokens: 1})
	if !errors.Is(err, ErrMissingDefaultCreditRate) {
		t.Fatalf("err = %v, want ErrMissingDefaultCreditRate", err)
	}
}

func TestComputeImageExtraUsageCostCredits_Floor(t *testing.T) {
	m := &types.Model{DefaultImageCreditRate: &types.ImageCreditRate{ImageOutputRate: 1}}
	cost, err := computeImageExtraUsageCostCredits(m, ImageTokenUsage{ImageOutputTokens: 1, OutputTokens: 1})
	if err != nil {
		t.Fatalf("computeImageExtraUsageCostCredits: %v", err)
	}
	// credits = 1 * 1 = 1; cost = ceil(1) = 1; min is 1
	if cost != 1 {
		t.Fatalf("cost = %d, want 1", cost)
	}
}
