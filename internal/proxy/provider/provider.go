package provider

import "net/http"

// Provider defines the interface for an LLM API provider.
type Provider interface {
	// Name returns the provider identifier (e.g., "anthropic").
	Name() string

	// Director modifies the outgoing request to target the upstream API.
	Director(req *http.Request, baseURL, apiKey string)
}
