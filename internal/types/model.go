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
	Name                   string           `json:"name"`
	DisplayName            string           `json:"display_name"`
	Description            string           `json:"description,omitempty"`
	Aliases                []string         `json:"aliases"`
	DefaultCreditRate      *CreditRate      `json:"default_credit_rate,omitempty"`
	DefaultImageCreditRate *ImageCreditRate `json:"default_image_credit_rate,omitempty"`
	Status                 string           `json:"status"`
	Publisher              string           `json:"publisher"`
	Metadata               ModelMetadata    `json:"metadata"`
	CreatedAt              time.Time        `json:"created_at"`
	UpdatedAt              time.Time        `json:"updated_at"`
}

// ModelMetadata carries optional hints about a model. Most fields are
// UI-oriented and unset fields are omitted from JSON. ExtraUsageOnly is the
// exception — it is a policy flag consumed by
// SubscriptionEligibilityMiddleware to force a model onto the extra-usage
// path regardless of client kind. Kept on ModelMetadata (rather than a
// dedicated column) so ops can flip it via the admin UI's existing
// metadata JSON editor without a schema migration; if the number of
// policy-shaped fields grows past two or three, promote them to dedicated
// columns.
type ModelMetadata struct {
	ContextWindow  int      `json:"context_window,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	ProviderHint   string   `json:"provider_hint,omitempty"`
	Icon           string   `json:"icon,omitempty"`
	Category       string   `json:"category,omitempty"`
	ReplacedBy     string   `json:"replaced_by,omitempty"`
	ExtraUsageOnly bool     `json:"extra_usage_only,omitempty"`
}
