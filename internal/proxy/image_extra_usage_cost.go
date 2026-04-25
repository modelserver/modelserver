package proxy

import (
	"fmt"
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

func computeImageExtraUsageCostFen(m *types.Model, u ImageTokenUsage, creditPriceFen int64) (int64, float64, error) {
	if creditPriceFen <= 0 {
		return 0, 0, fmt.Errorf("extra usage: credit_price_fen must be > 0")
	}
	credits, err := computeImageCredits(m, u)
	if err != nil {
		return 0, 0, err
	}
	if credits <= 0 {
		return 0, credits, nil
	}
	cost := int64(math.Ceil(credits * float64(creditPriceFen) / 1_000_000))
	if cost < 1 {
		cost = 1
	}
	return cost, credits, nil
}
