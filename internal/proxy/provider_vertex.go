package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelserver/modelserver/internal/types"
)

// vertexContextKey is used to pass Vertex-specific parameters through the
// request context from the Executor to SetUpstream.
type vertexContextKey struct{}

// vertexParams holds the resolved model and streaming flag that SetUpstream
// needs to construct the Vertex endpoint URL.
type vertexParams struct {
	Model    string
	IsStream bool
}

// withVertexParams returns a new request with Vertex routing parameters
// stored in its context.
func withVertexParams(r *http.Request, model string, isStream bool) *http.Request {
	ctx := context.WithValue(r.Context(), vertexContextKey{}, vertexParams{
		Model:    model,
		IsStream: isStream,
	})
	return r.WithContext(ctx)
}

// VertexTransformer handles Google Vertex AI request/response transformations.
// The tokenManager is stored as an atomic pointer so it can be set after init()
// without mutating the global providerTransformers map.
type VertexTransformer struct {
	tokenManager atomic.Pointer[VertexTokenManager]
}

var _ ProviderTransformer = (*VertexTransformer)(nil)

// TransformBody applies Vertex-specific body modifications.
func (t *VertexTransformer) TransformBody(body []byte, _ string, _ bool, headers http.Header) ([]byte, error) {
	betas := splitBetaHeaders(headers.Values("anthropic-beta"))
	return transformVertexBody(body, betas)
}

// SetTokenManager atomically sets the token manager. Called by Router init.
func (t *VertexTransformer) SetTokenManager(tm *VertexTokenManager) {
	t.tokenManager.Store(tm)
}

// SetUpstream configures the outbound request for a Vertex AI upstream.
func (t *VertexTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, _ string) error {
	params, _ := r.Context().Value(vertexContextKey{}).(vertexParams)

	tm := t.tokenManager.Load()
	if tm == nil {
		return fmt.Errorf("vertex token manager not initialized")
	}
	accessToken, err := tm.GetToken(upstream.ID)
	if err != nil {
		return err
	}

	directorSetVertexUpstream(r, upstream.BaseURL, accessToken, params.Model, params.IsStream)
	return nil
}

// WrapStream wraps the response body with the Anthropic SSE stream interceptor.
// Vertex AI's streamRawPredict returns standard Anthropic SSE format.
func (t *VertexTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
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

// ParseResponse extracts metrics from a non-streaming Vertex AI response body.
func (t *VertexTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
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
