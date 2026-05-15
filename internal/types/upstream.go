package types

import "time"

// Provider constants identify supported AI provider backends.
const (
	ProviderAnthropic        = "anthropic"
	ProviderOpenAI           = "openai"
	ProviderGemini           = "gemini"
	ProviderBedrockAnthropic = "bedrock-anthropic"
	ProviderBedrockOpenAI    = "bedrock-openai"
	ProviderClaudeCode       = "claudecode"
	ProviderVertexAnthropic  = "vertex-anthropic"
	ProviderVertexGoogle     = "vertex-google"
	ProviderVertexOpenAI     = "vertex-openai"
	ProviderCodex            = "codex"
)

// AllProviders enumerates every Provider* constant. Used for input validation
// (admin handlers reject unknown values to prevent silently broken upstreams
// from clients that have a stale provider list).
var AllProviders = []string{
	ProviderAnthropic,
	ProviderOpenAI,
	ProviderGemini,
	ProviderBedrockAnthropic,
	ProviderBedrockOpenAI,
	ProviderClaudeCode,
	ProviderVertexAnthropic,
	ProviderVertexGoogle,
	ProviderVertexOpenAI,
	ProviderCodex,
}

// IsValidProvider reports whether s is a recognized upstream provider.
// Use this to reject unknown values at API boundaries — internal code that
// has already loaded an upstream from the database may assume the value is
// valid and switch on it without falling through to a generic default.
func IsValidProvider(s string) bool {
	for _, p := range AllProviders {
		if p == s {
			return true
		}
	}
	return false
}

// Upstream status constants.
const (
	UpstreamStatusActive   = "active"
	UpstreamStatusDraining = "draining" // No new requests; in-flight streams finish naturally
	UpstreamStatusDisabled = "disabled"
)

// Upstream represents a single backend AI provider endpoint (nginx: server directive).
type Upstream struct {
	ID              string   `json:"id"`
	Provider        string   `json:"provider"`
	Name            string   `json:"name"`
	BaseURL         string   `json:"base_url"`
	APIKeyEncrypted []byte   `json:"-"`
	SupportedModels []string `json:"supported_models"`
	// ModelMap rewrites JSON request bodies per upstream. Multipart image edit
	// uploads are opaque and use catalog aliases instead.
	ModelMap      map[string]string  `json:"model_map"`
	Weight        int                `json:"weight"`                 // Default LB weight (can be overridden per-group)
	MaxConcurrent int                `json:"max_concurrent"`         // 0 = unlimited
	ReadTimeout   time.Duration      `json:"read_timeout,omitempty"` // Per-upstream response timeout (default: 300s for streaming)
	TestModel     string             `json:"test_model,omitempty"`
	Status        string             `json:"status"`                 // active / draining / disabled
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

// ResolveModel returns the upstream model name for the given request model.
// If a mapping exists in ModelMap, the mapped value is returned; otherwise
// the original model name is returned unchanged.
func (u *Upstream) ResolveModel(requestModel string) string {
	if u.ModelMap != nil {
		if mapped, ok := u.ModelMap[requestModel]; ok {
			return mapped
		}
	}
	return requestModel
}
