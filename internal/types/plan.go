package types

import "time"

// Plan represents a subscription plan stored in the database.
type Plan struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Slug             string                `json:"slug"`
	DisplayName      string                `json:"display_name"`
	Description      string                `json:"description,omitempty"`
	TierLevel        int                   `json:"tier_level"`
	GroupTag         string                `json:"group_tag,omitempty"`
	PricePerPeriod   int64                 `json:"price_per_period"`
	PeriodMonths     int                   `json:"period_months"`
	CreditRules      []CreditRule          `json:"credit_rules,omitempty"`
	ModelCreditRates map[string]CreditRate `json:"model_credit_rates,omitempty"`
	ClassicRules     []ClassicRule         `json:"classic_rules,omitempty"`
	IsActive         bool                  `json:"is_active"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

// ToPolicy constructs an in-memory RateLimitPolicy from the plan's rules.
// The returned policy has no StartsAt/ExpiresAt — the subscription's time
// window is already validated by GetActiveSubscription.
func (p *Plan) ToPolicy(projectID string) *RateLimitPolicy {
	return &RateLimitPolicy{
		ID:               "plan:" + p.ID,
		ProjectID:        projectID,
		Name:             p.Name,
		CreditRules:      p.CreditRules,
		ModelCreditRates: p.ModelCreditRates,
		ClassicRules:     p.ClassicRules,
	}
}
