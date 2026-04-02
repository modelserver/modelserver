package proxy

import (
	"io"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// GeminiTransformer handles Google Gemini API request/response transformations.
// The proxy is transparent: requests in Gemini native format are forwarded as-is.
// Only usage metrics are extracted from responses for rate limiting and billing.
type GeminiTransformer struct{}

var _ ProviderTransformer = (*GeminiTransformer)(nil)

// TransformBody is a no-op for Gemini. The body is already in Gemini native
// format and is forwarded as-is.
func (t *GeminiTransformer) TransformBody(body []byte, _ string, _ bool, _ http.Header) ([]byte, error) {
	return body, nil
}

// SetUpstream configures the outbound request for a Gemini API upstream.
// It reads the resolved model and streaming flag from the request context
// (set by the Executor via withGeminiParams) and constructs the endpoint URL.
func (t *GeminiTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
	params, _ := r.Context().Value(geminiContextKey{}).(geminiParams)
	directorSetGeminiUpstream(r, upstream.BaseURL, apiKey, params.Model, params.IsStream)
	return nil
}

// WrapStream wraps the response body with the Gemini SSE stream interceptor.
// The interceptor transparently passes through all bytes while extracting
// model, response ID, token usage, and TTFT from Gemini streaming events.
func (t *GeminiTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
	return newGeminiStreamInterceptor(body, startTime, onComplete)
}

// ParseResponse extracts metrics from a non-streaming Gemini API response body.
func (t *GeminiTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
	return ParseGeminiResponse(body)
}
