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
	// InterruptErr is set by the executor when copyWithFlush returned
	// an error (downstream write failed, upstream EOF mid-stream, etc).
	// nil on clean completion. Read by completeStreamingRequest to flip
	// the request row from success→error and record the cause.
	InterruptErr error
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
	// For Anthropic this is a no-op; for ClaudeCode it injects eager_input_streaming
	// per tool when the FGTS beta header is present; for Bedrock it strips model/stream
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

// openaiChatCompletionsTransformer handles the openai provider when the
// request kind is openai_chat_completions. It's stored separately rather than
// in providerTransformers because it shares its provider key with
// OpenAITransformer (Responses API); the per-kind branch in
// GetProviderTransformer chooses between them.
var openaiChatCompletionsTransformer ProviderTransformer = &OpenAIChatCompletionsTransformer{}

func init() {
	providerTransformers[types.ProviderAnthropic] = &AnthropicTransformer{}
	providerTransformers[types.ProviderBedrockAnthropic] = &BedrockTransformer{}
	providerTransformers[types.ProviderBedrockOpenAI] = &BedrockOpenAITransformer{}
	providerTransformers[types.ProviderOpenAI] = &OpenAITransformer{}
	providerTransformers[types.ProviderClaudeCode] = &ClaudeCodeTransformer{}
	providerTransformers[types.ProviderVertexAnthropic] = &VertexAnthropicTransformer{} // tokenManager set by Router init via SetVertexAnthropicTokenManager
	providerTransformers[types.ProviderVertexGoogle] = &VertexGoogleTransformer{} // tokenManager set by Router init via SetVertexGoogleTokenManager
	providerTransformers[types.ProviderGemini] = &GeminiTransformer{}
	providerTransformers[types.ProviderVertexOpenAI] = &VertexOpenAITransformer{}
	providerTransformers[types.ProviderCodex] = &CodexTransformer{}
}

// GetProviderTransformer returns the transformer for the given (provider, kind)
// pair. Falls back to the Anthropic transformer if the provider is unknown.
//
// The kind parameter exists because a single provider may serve multiple
// wire formats that need different stream interceptors. Currently only the
// openai provider needs this split (Responses API vs Chat Completions, see
// issue #57); other providers ignore kind.
func GetProviderTransformer(provider, kind string) ProviderTransformer {
	if provider == types.ProviderOpenAI && kind == types.KindOpenAIChatCompletions {
		return openaiChatCompletionsTransformer
	}
	if t, ok := providerTransformers[provider]; ok {
		return t
	}
	return providerTransformers[types.ProviderAnthropic]
}

// SetVertexAnthropicTokenManager sets the token manager on the already-registered
// VertexAnthropicTransformer. Called by Router init after creating the token manager.
// This avoids mutating the global providerTransformers map at runtime.
func SetVertexAnthropicTokenManager(tm *VertexTokenManager) {
	if vt, ok := providerTransformers[types.ProviderVertexAnthropic].(*VertexAnthropicTransformer); ok {
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

// SetVertexOpenAITokenManager sets the token manager on the already-registered
// VertexOpenAITransformer. Called by Router init after creating the token manager.
func SetVertexOpenAITokenManager(tm *VertexTokenManager) {
	if vt, ok := providerTransformers[types.ProviderVertexOpenAI].(*VertexOpenAITransformer); ok {
		vt.SetTokenManager(tm)
	}
}
