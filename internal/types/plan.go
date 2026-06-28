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
	PriceCNYFen      int64                 `json:"price_cny_fen"`
	PriceUSDCents    int64                 `json:"price_usd_cents"`
	PeriodMonths     int                   `json:"period_months"`
	CreditRules            []CreditRule                     `json:"credit_rules,omitempty"`
	ModelCreditRates       map[string]CreditRate            `json:"model_credit_rates,omitempty"`
	ClientModelCreditRates map[string]map[string]CreditRate `json:"client_model_credit_rates,omitempty"`
	ClassicRules           []ClassicRule                    `json:"classic_rules,omitempty"`
	IsActive         bool                  `json:"is_active"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

// IsFree reports whether the plan carries no price in any currency. Used by
// handle_orders.go to detect the "free → first paid purchase" branch; checking
// price_cny_fen alone would misclassify USD-only plans as free.
func (p *Plan) IsFree() bool {
	return p.PriceCNYFen == 0 && p.PriceUSDCents == 0
}

// ToPolicy constructs an in-memory RateLimitPolicy from the plan's rules.
// The returned policy has no StartsAt/ExpiresAt — the subscription's time
// window is already validated by GetActiveSubscription.
// If subscriptionStartsAt is provided, fixed-window rules get their AnchorTime set.
func (p *Plan) ToPolicy(projectID string, subscriptionStartsAt *time.Time) *RateLimitPolicy {
	rules := make([]CreditRule, len(p.CreditRules))
	copy(rules, p.CreditRules)
	if subscriptionStartsAt != nil {
		for i := range rules {
			if rules[i].WindowType == WindowTypeFixed {
				t := *subscriptionStartsAt
				rules[i].AnchorTime = &t
			}
		}
	}
	return &RateLimitPolicy{
		ID:                     "plan:" + p.ID,
		ProjectID:              projectID,
		Name:                   p.Name,
		CreditRules:            rules,
		ModelCreditRates:       p.ModelCreditRates,
		ClientModelCreditRates: p.ClientModelCreditRates,
		ClassicRules:           p.ClassicRules,
	}
}
