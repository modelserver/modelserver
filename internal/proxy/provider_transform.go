package proxy

import (
	"io"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// StreamMetrics is the unified metric output from all stream interceptors.
type StreamMetrics struct {
	Model               string
	MsgID               string
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	TTFTMs              int64
}

// ResponseMetrics is the unified metric output from non-streaming response parsing.
type ResponseMetrics struct {
	Model               string
	MsgID               string
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
}

// ProviderTransformer encapsulates all provider-specific request/response transformations.
// Each provider (Anthropic, Bedrock, OpenAI, ClaudeCode) implements this interface to
// handle its unique wire format, auth mechanism, and response parsing.
type ProviderTransformer interface {
	// TransformBody applies provider-specific body modifications.
	// For Anthropic/ClaudeCode this is a no-op; for Bedrock it strips model/stream
	// fields and injects anthropic_version/anthropic_beta; for OpenAI it's a no-op.
	TransformBody(originalBody []byte, model string, isStream bool, headers http.Header) ([]byte, error)

	// SetUpstream configures the outbound request for a specific upstream endpoint.
	// This sets the URL, host, auth headers, and removes client credentials.
	SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error

	// WrapStream wraps the response body with a provider-specific stream interceptor
	// that transparently parses SSE events for usage metrics while forwarding bytes.
	WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser

	// ParseResponse extracts metrics from a non-streaming response body.
	ParseResponse(body []byte) (*ResponseMetrics, error)
}

// providerTransformers maps provider name to its transformer implementation.
var providerTransformers = map[string]ProviderTransformer{}

func init() {
	providerTransformers[types.ProviderAnthropic] = &AnthropicTransformer{}
	providerTransformers[types.ProviderBedrock] = &BedrockTransformer{}
	providerTransformers[types.ProviderOpenAI] = &OpenAITransformer{}
	providerTransformers[types.ProviderClaudeCode] = &ClaudeCodeTransformer{}
	providerTransformers[types.ProviderVertex] = &VertexTransformer{}             // tokenManager set by Router init via SetTokenManager
	providerTransformers[types.ProviderVertexGoogle] = &VertexGoogleTransformer{} // tokenManager set by Router init via SetVertexGoogleTokenManager
	providerTransformers[types.ProviderGemini] = &GeminiTransformer{}
}

// GetProviderTransformer returns the transformer for the given provider name.
// Falls back to the Anthropic transformer if the provider is unknown.
func GetProviderTransformer(provider string) ProviderTransformer {
	if t, ok := providerTransformers[provider]; ok {
		return t
	}
	return providerTransformers[types.ProviderAnthropic]
}

// SetVertexTokenManager sets the token manager on the already-registered
// VertexTransformer. Called by Router init after creating the token manager.
// This avoids mutating the global providerTransformers map at runtime.
func SetVertexTokenManager(tm *VertexTokenManager) {
	if vt, ok := providerTransformers[types.ProviderVertex].(*VertexTransformer); ok {
		vt.SetTokenManager(tm)
	}
}

// SetVertexGoogleTokenManager sets the token manager on the already-registered
// VertexGoogleTransformer. Called by Router init after creating the token manager.
func SetVertexGoogleTokenManager(tm *VertexTokenManager) {
	if vt, ok := providerTransformers[types.ProviderVertexGoogle].(*VertexGoogleTransformer); ok {
		vt.SetTokenManager(tm)
	}
}
