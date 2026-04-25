package admin

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListUtilizationSnapshots(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamID := chi.URLParam(r, "upstreamID")
		windowType := r.URL.Query().Get("window")
		if windowType == "" {
			windowType = "5h"
		}
		limitStr := r.URL.Query().Get("limit")
		limit := 500
		if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
			limit = v
		}

		snaps, err := st.ListUtilizationSnapshots(upstreamID, windowType, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"data": snaps})
	}
}

// tokenType names used as feature columns in OLS.
var tokenTypes = [4]string{"input", "output", "cache_creation", "cache_read"}

var knownUtilizationLimits = map[string]float64{
	"5h": 11_000_000,
	"7d": 83_333_300,
}

var utilizationAnalysisBaseRates = map[string]types.CreditRate{
	"claude-opus-4-7":           {InputRate: 0.667, OutputRate: 3.333, CacheCreationRate: 0.667, CacheReadRate: 0},
	"claude-opus-4-6":           {InputRate: 0.667, OutputRate: 3.333, CacheCreationRate: 0.667, CacheReadRate: 0},
	"claude-sonnet-4-6":         {InputRate: 0.4, OutputRate: 2.0, CacheCreationRate: 0.4, CacheReadRate: 0},
	"claude-haiku-4-5":          {InputRate: 0.133, OutputRate: 0.667, CacheCreationRate: 0.133, CacheReadRate: 0},
	"claude-haiku-4-5-20251001": {InputRate: 0.133, OutputRate: 0.667, CacheCreationRate: 0.133, CacheReadRate: 0},
	"gpt-5.5":                   {InputRate: 0.044, OutputRate: 0.261, CacheCreationRate: 0, CacheReadRate: 0.0044},
	"gpt-5.4":                   {InputRate: 0.333, OutputRate: 2.0, CacheCreationRate: 0, CacheReadRate: 0.033},
	"gpt-5.3-codex":             {InputRate: 0.233, OutputRate: 1.867, CacheCreationRate: 0, CacheReadRate: 0.023},
	"gpt-5.2-codex":             {InputRate: 0.233, OutputRate: 1.867, CacheCreationRate: 0, CacheReadRate: 0.023},
	"gpt-5.2":                   {InputRate: 0.233, OutputRate: 1.867, CacheCreationRate: 0, CacheReadRate: 0.023},
	"gpt-5.1-codex-max":         {InputRate: 0.167, OutputRate: 1.333, CacheCreationRate: 0, CacheReadRate: 0.017},
	"gpt-5.1-codex-mini":        {InputRate: 0.033, OutputRate: 0.267, CacheCreationRate: 0, CacheReadRate: 0.003},
}

// featureKey is "model:token_type".
type featureKey struct {
	Model     string
	TokenType string
}

func handleUtilizationAnalysis(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamID := chi.URLParam(r, "upstreamID")
		windowType := r.URL.Query().Get("window")
		if windowType == "" {
			windowType = "5h"
		}

		snaps, err := st.ListUtilizationSnapshots(upstreamID, windowType, 1000)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		if len(snaps) < 3 {
			creditRateSuggestion := suggestRatesForFixedLimit(snaps, windowType, utilizationAnalysisBaseRates)
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"error":                  "insufficient data points for OLS",
				"data_points":            len(snaps),
				"min_needed":             3,
				"window_type":            windowType,
				"credit_regression":      creditRateSuggestion,
				"credit_rate_suggestion": creditRateSuggestion,
			})
			return
		}

		// Discover all models present in the data.
		modelSet := make(map[string]bool)
		for _, s := range snaps {
			for m := range s.ModelBreakdown {
				modelSet[m] = true
			}
		}
		models := make([]string, 0, len(modelSet))
		for m := range modelSet {
			models = append(models, m)
		}
		sort.Strings(models)

		// Build feature list: model × token_type.
		var features []featureKey
		for _, m := range models {
			for _, tt := range tokenTypes {
				features = append(features, featureKey{Model: m, TokenType: tt})
			}
		}
		nFeatures := len(features)
		n := len(snaps)

		// Build X matrix (n × nFeatures) and y vector (n × 1).
		X := make([][]float64, n)
		y := make([]float64, n)
		for i, s := range snaps {
			y[i] = s.OfficialPct / 100.0
			row := make([]float64, nFeatures)
			for j, f := range features {
				b, ok := s.ModelBreakdown[f.Model]
				if !ok {
					continue
				}
				switch f.TokenType {
				case "input":
					row[j] = float64(b.InputTokens)
				case "output":
					row[j] = float64(b.OutputTokens)
				case "cache_creation":
					row[j] = float64(b.CacheCreationTokens)
				case "cache_read":
					row[j] = float64(b.CacheReadTokens)
				}
			}
			X[i] = row
		}

		// Drop all-zero columns to avoid singular matrix (common when cache tokens are zero).
		nonZeroCols := make([]int, 0, nFeatures)
		for j := 0; j < nFeatures; j++ {
			hasNonZero := false
			for i := 0; i < n; i++ {
				if X[i][j] != 0 {
					hasNonZero = true
					break
				}
			}
			if hasNonZero {
				nonZeroCols = append(nonZeroCols, j)
			}
		}
		Xreduced := make([][]float64, n)
		for i := range Xreduced {
			row := make([]float64, len(nonZeroCols))
			for k, j := range nonZeroCols {
				row[k] = X[i][j]
			}
			Xreduced[i] = row
		}

		// Solve OLS: β = (X^T X)^{-1} X^T y
		betaReduced, err := solveOLS(Xreduced, y)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"error":             "OLS failed: " + err.Error(),
				"data_points":       n,
				"features":          nFeatures,
				"non_zero_features": len(nonZeroCols),
			})
			return
		}
		// Map reduced beta back to full feature indices.
		beta := make([]float64, nFeatures)
		for k, j := range nonZeroCols {
			beta[j] = betaReduced[k]
		}

		// Build effective rates and ratio maps.
		type effRates struct {
			Input         float64 `json:"input"`
			Output        float64 `json:"output"`
			CacheCreation float64 `json:"cache_creation"`
			CacheRead     float64 `json:"cache_read"`
		}
		type ratioInfo struct {
			OutputOverInput        float64 `json:"output_over_input"`
			CacheCreationOverInput float64 `json:"cache_creation_over_input"`
			CacheReadOverInput     float64 `json:"cache_read_over_input"`
		}
		effMap := make(map[string]*effRates)
		ratioMap := make(map[string]*ratioInfo)
		for j, f := range features {
			if effMap[f.Model] == nil {
				effMap[f.Model] = &effRates{}
			}
			e := effMap[f.Model]
			switch f.TokenType {
			case "input":
				e.Input = beta[j]
			case "output":
				e.Output = beta[j]
			case "cache_creation":
				e.CacheCreation = beta[j]
			case "cache_read":
				e.CacheRead = beta[j]
			}
		}
		for m, e := range effMap {
			ri := &ratioInfo{}
			if e.Input != 0 {
				ri.OutputOverInput = e.Output / e.Input
				ri.CacheCreationOverInput = e.CacheCreation / e.Input
				ri.CacheReadOverInput = e.CacheRead / e.Input
			}
			ratioMap[m] = ri
		}

		// Compute R² and RMSE.
		var ssRes, ssTot, sumY float64
		for i := range y {
			sumY += y[i]
		}
		meanY := sumY / float64(n)
		for i := range y {
			pred := dot(X[i], beta)
			ssRes += (y[i] - pred) * (y[i] - pred)
			ssTot += (y[i] - meanY) * (y[i] - meanY)
		}
		rmsePct := math.Sqrt(ssRes/float64(n)) * 100
		rSquared := 0.0
		if ssTot > 0 {
			rSquared = 1 - ssRes/ssTot
		}
		creditRateSuggestion := suggestRatesForFixedLimit(snaps, windowType, utilizationAnalysisBaseRates)

		// Anchored configurations.
		knownInputRates := make(map[string]float64, len(utilizationAnalysisBaseRates))
		for model, rate := range utilizationAnalysisBaseRates {
			knownInputRates[model] = rate.InputRate
		}

		type inferredRates struct {
			InputRate         float64 `json:"input_rate"`
			OutputRate        float64 `json:"output_rate"`
			CacheCreationRate float64 `json:"cache_creation_rate"`
			CacheReadRate     float64 `json:"cache_read_rate"`
		}
		type anchoredConfig struct {
			Anchor        string                    `json:"anchor"`
			InferredLimit float64                   `json:"inferred_limit"`
			InferredRates map[string]*inferredRates `json:"inferred_rates"`
		}

		var anchored []anchoredConfig

		// Anchor A: for each model with a known input rate, derive limit.
		for m, knownRate := range knownInputRates {
			e, ok := effMap[m]
			if !ok || e.Input <= 0 {
				continue
			}
			limit := knownRate / e.Input
			rates := make(map[string]*inferredRates)
			for m2, e2 := range effMap {
				rates[m2] = &inferredRates{
					InputRate:         e2.Input * limit,
					OutputRate:        e2.Output * limit,
					CacheCreationRate: e2.CacheCreation * limit,
					CacheReadRate:     e2.CacheRead * limit,
				}
			}
			anchored = append(anchored, anchoredConfig{
				Anchor:        m + " input_rate = " + strconv.FormatFloat(knownRate, 'f', 3, 64),
				InferredLimit: limit,
				InferredRates: rates,
			})
		}

		// Anchor B: assume known limit.
		if knownLimit, ok := knownUtilizationLimits[windowType]; ok {
			rates := make(map[string]*inferredRates)
			for m, e := range effMap {
				rates[m] = &inferredRates{
					InputRate:         e.Input * knownLimit,
					OutputRate:        e.Output * knownLimit,
					CacheCreationRate: e.CacheCreation * knownLimit,
					CacheReadRate:     e.CacheRead * knownLimit,
				}
			}
			anchored = append(anchored, anchoredConfig{
				Anchor:        windowType + " limit = " + strconv.FormatFloat(knownLimit, 'f', 0, 64),
				InferredLimit: knownLimit,
				InferredRates: rates,
			})
		}

		// Hypotheses testing: compute RMSE for predefined rate assumptions.
		type hypothesis struct {
			Name         string  `json:"name"`
			RMSEPct      float64 `json:"rmse_pct"`
			MeanErrorPct float64 `json:"mean_error_pct"`
			RSquared     float64 `json:"r_squared"`
		}
		type hypoConfig struct {
			name          string
			ccMult        float64 // cache_creation as multiple of input rate
			crMult        float64 // cache_read as multiple of input rate
			limitOverride float64 // 0 means use known limit
		}
		hypoConfigs := []hypoConfig{
			{"current (cc=1.0x, cr=0)", 1.0, 0, 0},
			{"cc=1.0x, cr=0.1x", 1.0, 0.1, 0},
			{"cc=1.25x, cr=0", 1.25, 0, 0},
			{"cc=1.25x, cr=0.1x", 1.25, 0.1, 0},
		}

		var hypotheses []hypothesis
		for _, hc := range hypoConfigs {
			limit := hc.limitOverride
			if limit == 0 {
				limit = knownUtilizationLimits[windowType]
			}
			if limit == 0 {
				continue
			}
			var ssr, sst, sumErr float64
			for i, s := range snaps {
				var pred float64
				for m, b := range s.ModelBreakdown {
					baseInput, ok := knownInputRates[m]
					if !ok {
						baseInput = 0.4 // default to sonnet
					}
					pred += baseInput * float64(b.InputTokens)
					pred += (baseInput * 5) * float64(b.OutputTokens) // output = 5x input for all claude models
					pred += (baseInput * hc.ccMult) * float64(b.CacheCreationTokens)
					pred += (baseInput * hc.crMult) * float64(b.CacheReadTokens)
				}
				predPct := pred / limit * 100
				residual := predPct - snaps[i].OfficialPct
				sumErr += residual
				ssr += residual * residual
				sst += (snaps[i].OfficialPct - meanY*100) * (snaps[i].OfficialPct - meanY*100)
			}
			rmse := math.Sqrt(ssr / float64(n))
			rs := 0.0
			if sst > 0 {
				rs = 1 - ssr/sst
			}
			hypotheses = append(hypotheses, hypothesis{
				Name:         hc.name,
				RMSEPct:      math.Round(rmse*1000) / 1000,
				MeanErrorPct: math.Round(sumErr/float64(n)*1000) / 1000,
				RSquared:     math.Round(rs*10000) / 10000,
			})
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"data_points":            n,
			"window_type":            windowType,
			"features":               nFeatures,
			"ols_effective_rates":    effMap,
			"rate_ratios":            ratioMap,
			"ols_rmse_pct":           math.Round(rmsePct*1000) / 1000,
			"ols_r_squared":          math.Round(rSquared*10000) / 10000,
			"credit_regression":      creditRateSuggestion,
			"credit_rate_suggestion": creditRateSuggestion,
			"anchored_configs":       anchored,
			"hypotheses":             hypotheses,
		})
	}
}

// --- OLS linear algebra helpers (pure Go, no external dependencies) ---

type creditRateSuggestion struct {
	DataPoints              int                         `json:"data_points"`
	WindowType              string                      `json:"window_type"`
	KnownLimit              float64                     `json:"known_limit"`
	SuggestedRateMultiplier float64                     `json:"suggested_rate_multiplier"`
	CurrentCredits          float64                     `json:"current_credits"`
	TargetCredits           float64                     `json:"target_credits"`
	CurrentCreditsMean      float64                     `json:"current_credits_mean"`
	TargetCreditsMean       float64                     `json:"target_credits_mean"`
	RMSEPct                 float64                     `json:"rmse_pct"`
	MeanErrorPct            float64                     `json:"mean_error_pct"`
	RSquared                float64                     `json:"r_squared"`
	SuggestedRates          map[string]types.CreditRate `json:"suggested_rates,omitempty"`
}

func suggestRatesForFixedLimit(snaps []store.UtilizationSnapshot, windowType string, baseRates map[string]types.CreditRate) *creditRateSuggestion {
	knownLimit, ok := knownUtilizationLimits[windowType]
	if !ok || knownLimit <= 0 {
		return nil
	}

	var sumCurrent, sumTarget float64
	var usable int
	modelSet := make(map[string]struct{})
	for _, s := range snaps {
		if s.TotalCredits <= 0 || s.OfficialPct < 0 {
			continue
		}
		sumCurrent += s.TotalCredits
		sumTarget += knownLimit * s.OfficialPct / 100.0
		usable++
		for model := range s.ModelBreakdown {
			modelSet[model] = struct{}{}
		}
	}
	if usable == 0 || sumCurrent <= 0 || sumTarget <= 0 {
		return nil
	}

	multiplier := sumTarget / sumCurrent

	var ssRes, ssTot, sumPct, sumErrPct float64
	for _, s := range snaps {
		if s.TotalCredits <= 0 || s.OfficialPct < 0 {
			continue
		}
		sumPct += s.OfficialPct
	}
	meanPct := sumPct / float64(usable)
	for _, s := range snaps {
		if s.TotalCredits <= 0 || s.OfficialPct < 0 {
			continue
		}
		predPct := s.TotalCredits * multiplier / knownLimit * 100
		residual := predPct - s.OfficialPct
		sumErrPct += residual
		ssRes += residual * residual
		ssTot += (s.OfficialPct - meanPct) * (s.OfficialPct - meanPct)
	}
	rSquared := 0.0
	if ssTot > 0 {
		rSquared = 1 - ssRes/ssTot
	}

	suggestedRates := make(map[string]types.CreditRate)
	for model := range modelSet {
		rate, ok := baseRates[model]
		if !ok {
			continue
		}
		suggestedRates[model] = scaleCreditRate(rate, multiplier)
	}

	return &creditRateSuggestion{
		DataPoints:              usable,
		WindowType:              windowType,
		KnownLimit:              knownLimit,
		SuggestedRateMultiplier: roundFloat(multiplier, 6),
		CurrentCredits:          roundFloat(sumCurrent, 3),
		TargetCredits:           roundFloat(sumTarget, 3),
		CurrentCreditsMean:      roundFloat(sumCurrent/float64(usable), 3),
		TargetCreditsMean:       roundFloat(sumTarget/float64(usable), 3),
		RMSEPct:                 roundFloat(math.Sqrt(ssRes/float64(usable)), 3),
		MeanErrorPct:            roundFloat(sumErrPct/float64(usable), 3),
		RSquared:                roundFloat(rSquared, 4),
		SuggestedRates:          suggestedRates,
	}
}

func scaleCreditRate(rate types.CreditRate, multiplier float64) types.CreditRate {
	return types.CreditRate{
		InputRate:         roundFloat(rate.InputRate*multiplier, 6),
		OutputRate:        roundFloat(rate.OutputRate*multiplier, 6),
		CacheCreationRate: roundFloat(rate.CacheCreationRate*multiplier, 6),
		CacheReadRate:     roundFloat(rate.CacheReadRate*multiplier, 6),
	}
}

func roundFloat(v float64, places int) float64 {
	scale := math.Pow10(places)
	return math.Round(v*scale) / scale
}

func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// solveOLS solves y = X*beta using normal equations: beta = (X^T X)^{-1} X^T y.
func solveOLS(X [][]float64, y []float64) ([]float64, error) {
	n := len(X)
	if n == 0 {
		return nil, fmt.Errorf("no data")
	}
	p := len(X[0])

	// Compute X^T X (p × p).
	XtX := make([][]float64, p)
	for i := range XtX {
		XtX[i] = make([]float64, p)
		for j := range XtX[i] {
			var s float64
			for k := 0; k < n; k++ {
				s += X[k][i] * X[k][j]
			}
			XtX[i][j] = s
		}
	}

	// Compute X^T y (p × 1).
	Xty := make([]float64, p)
	for i := 0; i < p; i++ {
		var s float64
		for k := 0; k < n; k++ {
			s += X[k][i] * y[k]
		}
		Xty[i] = s
	}

	// Invert X^T X using Gauss-Jordan elimination.
	inv, err := invertMatrix(XtX)
	if err != nil {
		return nil, err
	}

	// beta = inv * Xty.
	beta := make([]float64, p)
	for i := 0; i < p; i++ {
		var s float64
		for j := 0; j < p; j++ {
			s += inv[i][j] * Xty[j]
		}
		beta[i] = s
	}
	return beta, nil
}

// invertMatrix inverts a square matrix using Gauss-Jordan elimination.
func invertMatrix(a [][]float64) ([][]float64, error) {
	n := len(a)
	// Build augmented matrix [A | I].
	aug := make([][]float64, n)
	for i := range aug {
		aug[i] = make([]float64, 2*n)
		copy(aug[i], a[i])
		aug[i][n+i] = 1
	}

	for col := 0; col < n; col++ {
		// Find pivot.
		pivotRow := -1
		pivotVal := 0.0
		for row := col; row < n; row++ {
			if math.Abs(aug[row][col]) > pivotVal {
				pivotVal = math.Abs(aug[row][col])
				pivotRow = row
			}
		}
		if pivotVal < 1e-15 {
			return nil, fmt.Errorf("singular matrix (column %d)", col)
		}
		aug[col], aug[pivotRow] = aug[pivotRow], aug[col]

		// Scale pivot row.
		scale := aug[col][col]
		for j := range aug[col] {
			aug[col][j] /= scale
		}

		// Eliminate column.
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			factor := aug[row][col]
			for j := range aug[row] {
				aug[row][j] -= factor * aug[col][j]
			}
		}
	}

	// Extract inverse.
	inv := make([][]float64, n)
	for i := range inv {
		inv[i] = make([]float64, n)
		copy(inv[i], aug[i][n:])
	}
	return inv, nil
}
