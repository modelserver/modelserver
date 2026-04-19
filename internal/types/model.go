package types

import "time"

// Model status constants.
const (
	ModelStatusActive   = "active"
	ModelStatusDisabled = "disabled"
)

// Model is a catalog entry for a single LLM model. Every model name that
// flows through the proxy must be registered here. The canonical name is
// the primary key; aliases resolve to the same row.
type Model struct {
	Name              string        `json:"name"`
	DisplayName       string        `json:"display_name"`
	Description       string        `json:"description,omitempty"`
	Aliases           []string      `json:"aliases"`
	DefaultCreditRate *CreditRate   `json:"default_credit_rate,omitempty"`
	Status            string        `json:"status"`
	Publisher         string        `json:"publisher"`
	Metadata          ModelMetadata `json:"metadata"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
}

// ModelMetadata carries optional, UI-oriented hints about a model.
// Unset fields are omitted from JSON.
type ModelMetadata struct {
	ContextWindow int      `json:"context_window,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
	ProviderHint  string   `json:"provider_hint,omitempty"`
	Icon          string   `json:"icon,omitempty"`
	Category      string   `json:"category,omitempty"`
	ReplacedBy    string   `json:"replaced_by,omitempty"`
}
