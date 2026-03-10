package types

import "time"

// Provider name constants.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	ProviderGemini    = "gemini"
)

// Channel status constants.
const (
	ChannelStatusActive   = "active"
	ChannelStatusDisabled = "disabled"
)

// Channel represents an upstream AI provider endpoint that the proxy routes requests to.
type Channel struct {
	ID                string    `json:"id"`
	Provider          string    `json:"provider"`
	Name              string    `json:"name"`
	BaseURL           string    `json:"base_url"`
	APIKeyEncrypted   []byte    `json:"-"`
	SupportedModels   []string  `json:"supported_models"`
	Weight            int       `json:"weight"`
	SelectionPriority int       `json:"selection_priority"`
	Status            string    `json:"status"`
	MaxConcurrent     int       `json:"max_concurrent"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ChannelRoute maps a model name pattern to specific channels.
type ChannelRoute struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id,omitempty"`
	ModelPattern  string    `json:"model_pattern"`
	ChannelIDs    []string  `json:"channel_ids"`
	MatchPriority int       `json:"match_priority"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
