package types

import "time"

// Predefined plan names.
const (
	PlanPro    = "pro"
	PlanMax5x  = "max_5x"
	PlanMax20x = "max_20x"
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
	Window     string     `json:"window"`                // "5h", "1w", "1M"
	WindowType string     `json:"window_type"`           // "sliding", "calendar", or "fixed"
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
	InputRate         float64 `json:"input_rate"`
	OutputRate        float64 `json:"output_rate"`
	CacheCreationRate float64 `json:"cache_creation_rate"`
	CacheReadRate     float64 `json:"cache_read_rate"`
}

// ComputeCredits calculates the credits consumed by a request using the policy's model rates.
// Returns 0 if no rates are configured.
func (p *RateLimitPolicy) ComputeCredits(model string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) float64 {
	if len(p.ModelCreditRates) == 0 {
		return 0
	}
	rate, ok := p.ModelCreditRates[model]
	if !ok {
		rate, ok = p.ModelCreditRates["_default"]
		if !ok {
			return 0
		}
	}
	credits := rate.InputRate*float64(inputTokens) +
		rate.OutputRate*float64(outputTokens) +
		rate.CacheCreationRate*float64(cacheCreationTokens) +
		rate.CacheReadRate*float64(cacheReadTokens)
	return credits
}

// ClassicRule defines a traditional rate limit.
type ClassicRule struct {
	Metric   string `json:"metric"`    // "rpm", "rpd", "tpm", "tpd"
	Limit    int64  `json:"limit"`
	PerModel bool   `json:"per_model"`
}

// Subscription binds a project to a plan with a time range.
type Subscription struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	PlanID    string    `json:"plan_id,omitempty"`
	PlanName  string    `json:"plan_name"` // "pro", "max_5x", "max_20x", or custom
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
