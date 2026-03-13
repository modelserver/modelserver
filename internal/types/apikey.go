package types

import "time"

// APIKeyPrefix is prepended to every plaintext API key issued by the server.
const APIKeyPrefix = "ms-"

// API key status constants.
const (
	APIKeyStatusActive   = "active"
	APIKeyStatusDisabled = "disabled"
	APIKeyStatusRevoked  = "revoked"
)

// APIKey represents a bearer token that grants access to the proxy on behalf of a project.
type APIKey struct {
	ID                string     `json:"id"`
	ProjectID         string     `json:"project_id"`
	CreatedBy         string     `json:"created_by"`
	KeyHash           string     `json:"-"`
	KeySuffix         string     `json:"key_suffix"`
	Name              string     `json:"name"`
	Description       string     `json:"description,omitempty"`
	Status            string     `json:"status"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	LastUsedAt        *time.Time `json:"last_used_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}
