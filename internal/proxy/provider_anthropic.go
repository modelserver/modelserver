package proxy

import (
	"io"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelserver/modelserver/internal/types"
)

// AnthropicTransformer handles Anthropic API request/response transformations.
// The Anthropic wire format is used as-is; model rewriting is handled by the
// Executor before calling TransformBody.
type AnthropicTransformer struct{}

var _ ProviderTransformer = (*AnthropicTransformer)(nil)

// TransformBody is a no-op for Anthropic. The body is sent as-is; model
// rewriting (via ModelMap) is performed by the Executor before this call.
func (t *AnthropicTransformer) TransformBody(body []byte, _ string, _ bool, _ http.Header) ([]byte, error) {
	return body, nil
}

// SetUpstream configures the outbound request for an Anthropic upstream.
// Reuses the existing directorSetUpstream logic: sets URL, x-api-key,
// anthropic-version, and removes client auth headers.
func (t *AnthropicTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
	directorSetUpstream(r, upstream.BaseURL, apiKey)
	return nil
}

// WrapStream wraps the response body with the Anthropic SSE stream interceptor.
// The interceptor transparently passes through all bytes while extracting
// model, message ID, token usage, and TTFT from SSE events.
func (t *AnthropicTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
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

// ParseResponse extracts metrics from a non-streaming Anthropic response body.
func (t *AnthropicTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
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
