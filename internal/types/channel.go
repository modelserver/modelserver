package types

import "time"

// Deprecated: Use UpstreamStatus* constants instead.
// These aliases are kept for backward compatibility with existing code.
const (
	ChannelStatusActive   = UpstreamStatusActive
	ChannelStatusDisabled = UpstreamStatusDisabled
)

// Deprecated: Channel is superseded by Upstream.
// This type is kept for backward compatibility; new code should use Upstream.
type Channel struct {
	ID                string            `json:"id"`
	Provider          string            `json:"provider"`
	Name              string            `json:"name"`
	BaseURL           string            `json:"base_url"`
	APIKeyEncrypted   []byte            `json:"-"`
	SupportedModels   []string          `json:"supported_models"`
	ModelMap          map[string]string `json:"model_map"`
	Weight            int               `json:"weight"`
	SelectionPriority int               `json:"selection_priority"`
	Status            string            `json:"status"`
	MaxConcurrent     int               `json:"max_concurrent"`
	TestModel         string            `json:"test_model,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// ResolveModel returns the upstream model name for the given request model.
// If a mapping exists in ModelMap, the mapped value is returned; otherwise
// the original model name is returned unchanged.
//
// Deprecated: Use Upstream.ResolveModel instead.
func (c *Channel) ResolveModel(requestModel string) string {
	if c.ModelMap != nil {
		if mapped, ok := c.ModelMap[requestModel]; ok {
			return mapped
		}
	}
	return requestModel
}

// Deprecated: ChannelRoute is superseded by Route.
// This type is kept for backward compatibility; new code should use Route.
type ChannelRoute struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id,omitempty"`
	ModelPattern  string    `json:"model_pattern"`
	ChannelIDs    []string  `json:"channel_ids"`
	MatchPriority int       `json:"match_priority"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
