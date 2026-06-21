package proxy

import (
	"math"

	"github.com/modelserver/modelserver/internal/types"
)

func computeImageCredits(m *types.Model, u ImageTokenUsage) (float64, error) {
	if m == nil || m.DefaultImageCreditRate == nil {
		return 0, ErrMissingDefaultCreditRate
	}
	r := m.DefaultImageCreditRate

	totalInput := u.TextInputTokens + u.ImageInputTokens
	var cachedText, cachedImage int64
	if totalInput > 0 && u.CachedInputTokens > 0 {
		cachedText = u.CachedInputTokens * u.TextInputTokens / totalInput
		cachedImage = u.CachedInputTokens - cachedText
	}
	billedText := u.TextInputTokens - cachedText
	if billedText < 0 {
		billedText = 0
	}
	billedImage := u.ImageInputTokens - cachedImage
	if billedImage < 0 {
		billedImage = 0
	}

	return r.TextInputRate*float64(billedText) +
		r.ImageInputRate*float64(billedImage) +
		r.TextCachedInputRate*float64(cachedText) +
		r.ImageCachedInputRate*float64(cachedImage) +
		r.TextOutputRate*float64(u.TextOutputTokens) +
		r.ImageOutputRate*float64(u.ImageOutputTokens), nil
}

// computeImageExtraUsageCostCredits converts ImageTokenUsage to credits using
// the catalog's DefaultImageCreditRate. Returns (credits, err). Credits round
// UP so sub-credit fractions don't undercharge. The creditPriceFen conversion
// step has been removed: credits is the natural output of tokens × rate.
func computeImageExtraUsageCostCredits(m *types.Model, u ImageTokenUsage) (int64, error) {
	credits, err := computeImageCredits(m, u)
	if err != nil {
		return 0, err
	}
	if credits <= 0 {
		return 0, nil
	}
	cost := int64(math.Ceil(credits))
	if cost < 1 {
		cost = 1
	}
	return cost, nil
}
