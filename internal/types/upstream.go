package types

import "time"

// Provider constants identify supported AI provider backends.
const (
	ProviderAnthropic  = "anthropic"
	ProviderOpenAI     = "openai"
	ProviderGemini     = "gemini"
	ProviderBedrock    = "bedrock"
	ProviderClaudeCode = "claudecode"
	ProviderVertexAnthropic = "vertex-anthropic"
	ProviderVertexGoogle = "vertex-google"
)

// Upstream status constants.
const (
	UpstreamStatusActive   = "active"
	UpstreamStatusDraining = "draining" // No new requests; in-flight streams finish naturally
	UpstreamStatusDisabled = "disabled"
)

// Upstream represents a single backend AI provider endpoint (nginx: server directive).
type Upstream struct {
	ID              string             `json:"id"`
	Provider        string             `json:"provider"`
	Name            string             `json:"name"`
	BaseURL         string             `json:"base_url"`
	APIKeyEncrypted []byte             `json:"-"`
	SupportedModels []string           `json:"supported_models"`
	ModelMap        map[string]string  `json:"model_map"`
	Weight          int                `json:"weight"`                  // Default LB weight (can be overridden per-group)
	MaxConcurrent   int                `json:"max_concurrent"`          // 0 = unlimited
	DialTimeout     time.Duration      `json:"dial_timeout,omitempty"`  // Per-upstream TCP dial timeout (default: 10s)
	ReadTimeout     time.Duration      `json:"read_timeout,omitempty"`  // Per-upstream response timeout (default: 300s for streaming)
	TestModel       string             `json:"test_model,omitempty"`
	HealthCheck     *HealthCheckConfig `json:"health_check,omitempty"`  // Per-upstream health check config
	Status          string             `json:"status"`                  // active / draining / disabled
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

// HealthCheckConfig configures active health probes for this upstream.
type HealthCheckConfig struct {
	Enabled  bool          `json:"enabled"`            // Default: true
	Interval time.Duration `json:"interval,omitempty"` // Default: 30s
	Timeout  time.Duration `json:"timeout,omitempty"`  // Default: 5s
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
