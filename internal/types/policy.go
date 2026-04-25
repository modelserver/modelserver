package types

import "time"

// Predefined plan names.
const (
	PlanPro     = "pro"
	PlanMax5x   = "max_5x"
	PlanMax20x  = "max_20x"
	PlanMax40x  = "max_40x"
	PlanMax80x  = "max_80x"
	PlanMax120x = "max_120x"
	PlanMax200x = "max_200x"
)

// Subscription status constants.
const (
	SubscriptionStatusActive  = "active"
	SubscriptionStatusExpired = "expired"
	SubscriptionStatusRevoked = "revoked"
)

// Window type constants.
const (
	WindowTypeSliding  = "sliding"
	WindowTypeCalendar = "calendar"
	WindowTypeFixed    = "fixed"
)

// CreditScope determines how credit is counted.
const (
	CreditScopeProject = "project" // shared across all keys in the project
	CreditScopeKey     = "key"     // per individual API key
)

// RateLimitPolicy defines the rate-limiting and credit rules applied to an API key or project.
type RateLimitPolicy struct {
	ID               string                `json:"id"`
	ProjectID        string                `json:"project_id"`
	Name             string                `json:"name"`
	IsDefault        bool                  `json:"is_default"`
	CreditRules      []CreditRule          `json:"credit_rules,omitempty"`
	ModelCreditRates map[string]CreditRate `json:"model_credit_rates,omitempty"`
	ClassicRules     []ClassicRule         `json:"classic_rules,omitempty"`
	StartsAt         *time.Time            `json:"starts_at,omitempty"`
	ExpiresAt        *time.Time            `json:"expires_at,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

// IsActive returns true if the policy is currently within its validity window.
func (p *RateLimitPolicy) IsActive() bool {
	now := time.Now()
	if p.StartsAt != nil && now.Before(*p.StartsAt) {
		return false
	}
	if p.ExpiresAt != nil && now.After(*p.ExpiresAt) {
		return false
	}
	return true
}

// CreditRule defines a credit budget for a time window.
type CreditRule struct {
	Window     string     `json:"window"`      // "5h", "1w", "1M"
	WindowType string     `json:"window_type"` // "sliding", "calendar", or "fixed"
	MaxCredits int64      `json:"max_credits"`
	Scope      string     `json:"scope,omitempty"`       // "project" or "key"; defaults to "project"
	AnchorTime *time.Time `json:"anchor_time,omitempty"` // runtime-injected for fixed windows
}

// EffectiveScope returns the scope, defaulting to "project" if empty.
func (r CreditRule) EffectiveScope() string {
	if r.Scope == "" {
		return CreditScopeProject
	}
	return r.Scope
}

// CreditRate defines per-model credit rates.
type CreditRate struct {
	InputRate         float64                `json:"input_rate"`
	OutputRate        float64                `json:"output_rate"`
	CacheCreationRate float64                `json:"cache_creation_rate"`
	CacheReadRate     float64                `json:"cache_read_rate"`
	LongContext       *LongContextCreditRate `json:"long_context,omitempty"`
}

// LongContextCreditRate describes pricing that applies when a request's total
// input context exceeds a token threshold. OpenAI's long-context pricing
// applies the multiplier to the whole request, not only the tokens above the
// threshold.
type LongContextCreditRate struct {
	ThresholdInputTokens int64   `json:"threshold_input_tokens"`
	InputMultiplier      float64 `json:"input_multiplier"`
	OutputMultiplier     float64 `json:"output_multiplier"`
}

// ImageCreditRate defines per-token credit rates for image models whose
// usage reports text/image token classes separately.
type ImageCreditRate struct {
	TextInputRate        float64 `json:"text_input_rate"`
	TextCachedInputRate  float64 `json:"text_cached_input_rate"`
	TextOutputRate       float64 `json:"text_output_rate"`
	ImageInputRate       float64 `json:"image_input_rate"`
	ImageCachedInputRate float64 `json:"image_cached_input_rate"`
	ImageOutputRate      float64 `json:"image_output_rate"`
}

// ComputeCredits calculates credits using only the policy's own rate map.
// Prefer ComputeCreditsWithDefault so the catalog-level default can act as a
// fallback between the plan override and the plan's "_default".
func (p *RateLimitPolicy) ComputeCredits(model string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) float64 {
	return p.ComputeCreditsWithDefault(model, nil, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens)
}

// ComputeCreditsWithDefault resolves a credit rate in the order:
//  1. policy.ModelCreditRates[model]  (explicit plan override)
//  2. catalogDefault                  (per-model truth from the catalog)
//  3. policy.ModelCreditRates["_default"]  (plan-wide safety net)
//  4. zero rate                       (no billing)
//
// This mirrors the billing-fallback contract described in the model catalog
// spec: plan overrides are most intentional; catalog defaults are the
// per-model source of truth; the plan "_default" remains as a legacy safety
// net for plans that rely on it.
func (p *RateLimitPolicy) ComputeCreditsWithDefault(model string, catalogDefault *CreditRate, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) float64 {
	rate, ok := p.ModelCreditRates[model]
	if !ok {
		if catalogDefault != nil {
			rate = *catalogDefault
			ok = true
		}
	}
	if !ok {
		rate, ok = p.ModelCreditRates["_default"]
	}
	if !ok {
		return 0
	}
	rate = ApplyLongContextCreditRate(rate, inputTokens+cacheCreationTokens+cacheReadTokens)
	return rate.InputRate*float64(inputTokens) +
		rate.OutputRate*float64(outputTokens) +
		rate.CacheCreationRate*float64(cacheCreationTokens) +
		rate.CacheReadRate*float64(cacheReadTokens)
}

// ApplyLongContextCreditRate returns an effective copy of rate after applying
// any configured long-context multipliers. It intentionally treats malformed
// long_context values as disabled so old persisted JSON remains harmless.
func ApplyLongContextCreditRate(rate CreditRate, totalInputTokens int64) CreditRate {
	lc := rate.LongContext
	if lc == nil ||
		lc.ThresholdInputTokens <= 0 ||
		totalInputTokens <= lc.ThresholdInputTokens ||
		lc.InputMultiplier <= 0 ||
		lc.OutputMultiplier <= 0 {
		return rate
	}
	rate.InputRate *= lc.InputMultiplier
	rate.CacheCreationRate *= lc.InputMultiplier
	rate.CacheReadRate *= lc.InputMultiplier
	rate.OutputRate *= lc.OutputMultiplier
	return rate
}

// ClassicRule defines a traditional rate limit.
type ClassicRule struct {
	Metric   string `json:"metric"` // "rpm", "rpd", "tpm", "tpd"
	Limit    int64  `json:"limit"`
	PerModel bool   `json:"per_model"`
}

// Subscription binds a project to a plan with a time range.
type Subscription struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	PlanID    string    `json:"plan_id,omitempty"`
	PlanName  string    `json:"plan_name"` // "pro", "max_5x", "max_20x", "max_40x", "max_80x", "max_120x", "max_200x", or custom
	Status    string    `json:"status"`
	StartsAt  time.Time `json:"starts_at"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsActive returns true if the subscription is currently active.
func (s *Subscription) IsActive() bool {
	now := time.Now()
	return s.Status == SubscriptionStatusActive && !now.Before(s.StartsAt) && !now.After(s.ExpiresAt)
}
