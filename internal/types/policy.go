package types

import "time"

// RateLimitPolicy defines the rate-limiting and credit rules applied to an API key or project.
type RateLimitPolicy struct {
	ID               string                `json:"id"`
	ProjectID        string                `json:"project_id"`
	Name             string                `json:"name"`
	IsDefault        bool                  `json:"is_default"`
	CreditRules      []CreditRule          `json:"credit_rules,omitempty"`
	ModelCreditRates map[string]CreditRate `json:"model_credit_rates,omitempty"`
	ClassicRules     []ClassicRule         `json:"classic_rules,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

// CreditRule defines a credit budget for a time window.
type CreditRule struct {
	Window     string `json:"window"`
	WindowType string `json:"window_type"`
	MaxCredits int64  `json:"max_credits"`
}

// CreditRate defines per-model credit rates.
type CreditRate struct {
	InputRate         float64 `json:"input_rate"`
	OutputRate        float64 `json:"output_rate"`
	CacheCreationRate float64 `json:"cache_creation_rate"`
	CacheReadRate     float64 `json:"cache_read_rate"`
}

// ClassicRule defines a traditional rate limit.
type ClassicRule struct {
	Metric   string `json:"metric"`
	Limit    int64  `json:"limit"`
	PerModel bool   `json:"per_model"`
}
