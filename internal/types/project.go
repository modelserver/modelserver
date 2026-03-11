package types

import (
	"encoding/json"
	"time"
)

// Project status constants.
const (
	ProjectStatusActive    = "active"
	ProjectStatusSuspended = "suspended"
)

// Project represents a logical grouping of API keys, channels, and policies.
type Project struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Slug        string          `json:"slug"`
	Description string          `json:"description,omitempty"`
	CreatedBy   string          `json:"created_by"`
	Status      string          `json:"status"`
	Settings    json.RawMessage `json:"settings,omitempty"`
	BillingTag  string          `json:"billing_tag,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}
