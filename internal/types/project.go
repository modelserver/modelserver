package types

import (
	"encoding/json"
	"time"
)

// Project status constants.
const (
	ProjectStatusActive    = "active"
	ProjectStatusSuspended = "suspended"
	ProjectStatusArchived  = "archived"
)

// Project represents a logical grouping of API keys, upstreams, and policies.
type Project struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	CreatedBy   string          `json:"created_by"`
	Status      string          `json:"status"`
	Settings    json.RawMessage `json:"settings,omitempty"`
	BillingTags []string        `json:"billing_tags,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}
