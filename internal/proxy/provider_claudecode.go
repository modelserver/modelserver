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

// TransformBody applies FGTS (fine-grained tool streaming) when the client
// sends the corresponding anthropic-beta header. This adds eager_input_streaming: true
// to each tool, matching Claude Code's behavior (see /root/cc/source/src/utils/api.ts:194-206).
// The beta header itself is stripped later by mergeClaudeCodeBetaHeaders in SetUpstream.
func (t *ClaudeCodeTransformer) TransformBody(body []byte, _ string, _ bool, headers http.Header) ([]byte, error) {
	return applyFGTS(body, headers)
}

// SetUpstream configures the outbound request for a Claude Code upstream.
// The apiKey parameter is expected to be either a raw access token (when resolved
// by the OAuthTokenManager via the executor) or a JSON credentials blob (fallback).
func (t *ClaudeCodeTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
	accessToken := apiKey
	// If the apiKey looks like JSON (starts with '{'), extract the access_token field.
	if len(apiKey) > 0 && apiKey[0] == '{' {
		if parsed := ParseClaudeCodeAccessToken(apiKey); parsed != "" {
			accessToken = parsed
		}
	}
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
