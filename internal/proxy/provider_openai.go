package proxy

import (
	"io"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// OpenAITransformer handles OpenAI Responses API request/response transformations.
type OpenAITransformer struct{}

var _ ProviderTransformer = (*OpenAITransformer)(nil)

// TransformBody is a no-op for OpenAI. The body is sent as-is.
func (t *OpenAITransformer) TransformBody(body []byte, _ string, _ bool, _ http.Header) ([]byte, error) {
	return body, nil
}

// SetUpstream configures the outbound request for an OpenAI upstream.
// Reuses the existing directorSetOpenAIUpstream logic.
func (t *OpenAITransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
	directorSetOpenAIUpstream(r, upstream.BaseURL, apiKey)
	return nil
}

// WrapStream wraps the response body with the OpenAI SSE stream interceptor.
// The interceptor transparently passes through all bytes while extracting
// model, response ID, token usage, and TTFT from OpenAI Responses API events.
//
// The OpenAI stream interceptor needs the model as a fallback (in case it's not
// in the stream events). We read it from the request context set by the Executor.
func (t *OpenAITransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
	// The OpenAI stream interceptor needs a model fallback. Since we don't have
	// access to it directly here, we pass empty string and rely on the stream
	// events to provide it (response.created always includes the model).
	return newOpenAIStreamInterceptor(body, startTime, "", func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64) {
		onComplete(StreamMetrics{
			Model:               model,
			MsgID:               respID,
			InputTokens:         inputTokens,
			OutputTokens:        outputTokens,
			CacheCreationTokens: 0,
			CacheReadTokens:     cacheReadTokens,
			TTFTMs:              ttft,
		})
	})
}

// ParseResponse extracts metrics from a non-streaming OpenAI Responses API body.
// OpenAI uses InputTokensDetails.CachedTokens for cache read tokens. The
// effective input tokens are computed as InputTokens - CachedTokens.
func (t *OpenAITransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
	model, respID, usage, err := ParseOpenAINonStreamingResponse(body)
	if err != nil {
		return nil, err
	}

	cachedTokens := usage.InputTokensDetails.CachedTokens
	inputTokens := usage.InputTokens - cachedTokens
	if inputTokens < 0 {
		inputTokens = 0
	}

	return &ResponseMetrics{
		Model:               model,
		MsgID:               respID,
		InputTokens:         inputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: 0,
		CacheReadTokens:     cachedTokens,
	}, nil
}
