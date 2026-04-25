package proxy

import (
	"errors"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestComputeImageExtraUsageCostFen_ImageOutputOnly(t *testing.T) {
	m := &types.Model{DefaultImageCreditRate: &types.ImageCreditRate{ImageOutputRate: 30}}
	cost, credits, err := computeImageExtraUsageCostFen(m, ImageTokenUsage{ImageOutputTokens: 1000, OutputTokens: 1000}, 100)
	if err != nil {
		t.Fatalf("computeImageExtraUsageCostFen: %v", err)
	}
	if credits != 30000 {
		t.Fatalf("credits = %v, want 30000", credits)
	}
	if cost != 3 {
		t.Fatalf("cost = %d, want 3", cost)
	}
}

func TestComputeImageExtraUsageCostFen_ProportionalCachedSplit(t *testing.T) {
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
	_, credits, err := computeImageExtraUsageCostFen(m, u, 100)
	if err != nil {
		t.Fatalf("computeImageExtraUsageCostFen: %v", err)
	}
	// cachedText=10, cachedImage=15, billedText=30, billedImage=45
	want := float64(10*30 + 20*45 + 1*10 + 2*15 + 30*2 + 40*3)
	if credits != want {
		t.Fatalf("credits = %v, want %v", credits, want)
	}
}

func TestComputeImageExtraUsageCostFen_MissingRate(t *testing.T) {
	_, _, err := computeImageExtraUsageCostFen(&types.Model{}, ImageTokenUsage{OutputTokens: 1}, 100)
	if !errors.Is(err, ErrMissingDefaultCreditRate) {
		t.Fatalf("err = %v, want ErrMissingDefaultCreditRate", err)
	}
}

func TestComputeImageExtraUsageCostFen_Floor(t *testing.T) {
	m := &types.Model{DefaultImageCreditRate: &types.ImageCreditRate{ImageOutputRate: 1}}
	cost, _, err := computeImageExtraUsageCostFen(m, ImageTokenUsage{ImageOutputTokens: 1, OutputTokens: 1}, 1)
	if err != nil {
		t.Fatalf("computeImageExtraUsageCostFen: %v", err)
	}
	if cost != 1 {
		t.Fatalf("cost = %d, want 1", cost)
	}
}
