package proxy

import (
	"io"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelserver/modelserver/internal/types"
)

// ClaudeCodeTransformer handles Claude Code (OAuth subscription) request/response
// transformations. The wire format is standard Anthropic; only the auth mechanism
// differs (OAuth Bearer token instead of x-api-key).
type ClaudeCodeTransformer struct{}

var _ ProviderTransformer = (*ClaudeCodeTransformer)(nil)

// TransformBody is a no-op for Claude Code. The body is identical to Anthropic.
func (t *ClaudeCodeTransformer) TransformBody(body []byte, _ string, _ bool, _ http.Header) ([]byte, error) {
	return body, nil
}

// SetUpstream configures the outbound request for a Claude Code upstream.
// It parses the API key as a JSON credentials blob to extract the access token,
// then delegates to directorSetClaudeCodeUpstream for URL and header setup.
//
// In the future, full OAuth token refresh will be integrated via OAuthTokenManager.
// For now, we fall back to ParseClaudeCodeAccessToken for the access token.
func (t *ClaudeCodeTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
	// Extract access token from the credentials JSON stored as the API key.
	// Full OAuth refresh integration will be wired in a future step.
	accessToken := ParseClaudeCodeAccessToken(apiKey)
	directorSetClaudeCodeUpstream(r, upstream.BaseURL, accessToken)
	return nil
}

// WrapStream wraps the response body with the Anthropic SSE stream interceptor.
// Claude Code uses the same response format as Anthropic.
func (t *ClaudeCodeTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
	return newStreamInterceptor(body, startTime, func(model, msgID string, usage anthropic.Usage, ttft int64) {
		onComplete(StreamMetrics{
			Model:               model,
			MsgID:               msgID,
			InputTokens:         usage.InputTokens,
			OutputTokens:        usage.OutputTokens,
			CacheCreationTokens: usage.CacheCreationInputTokens,
			CacheReadTokens:     usage.CacheReadInputTokens,
			TTFTMs:              ttft,
		})
	})
}

// ParseResponse extracts metrics from a non-streaming Claude Code response body.
// Claude Code uses the same response format as Anthropic.
func (t *ClaudeCodeTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
	model, msgID, usage, err := ParseNonStreamingResponse(body)
	if err != nil {
		return nil, err
	}
	return &ResponseMetrics{
		Model:               model,
		MsgID:               msgID,
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationInputTokens,
		CacheReadTokens:     usage.CacheReadInputTokens,
	}, nil
}
