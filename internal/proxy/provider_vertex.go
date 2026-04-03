package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelserver/modelserver/internal/types"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// vertexAnthropicContextKey is used to pass Vertex-Anthropic-specific parameters through the
// request context from the Executor to SetUpstream.
type vertexAnthropicContextKey struct{}

// vertexAnthropicParams holds the resolved model and streaming flag that SetUpstream
// needs to construct the Vertex endpoint URL.
type vertexAnthropicParams struct {
	Model    string
	IsStream bool
}

// withVertexAnthropicParams returns a new request with Vertex-Anthropic routing parameters
// stored in its context.
func withVertexAnthropicParams(r *http.Request, model string, isStream bool) *http.Request {
	ctx := context.WithValue(r.Context(), vertexAnthropicContextKey{}, vertexAnthropicParams{
		Model:    model,
		IsStream: isStream,
	})
	return r.WithContext(ctx)
}

// VertexAnthropicTransformer handles Google Vertex AI (Anthropic) request/response transformations.
// The tokenManager is stored as an atomic pointer so it can be set after init()
// without mutating the global providerTransformers map.
type VertexAnthropicTransformer struct {
	tokenManager atomic.Pointer[VertexTokenManager]
}

var _ ProviderTransformer = (*VertexAnthropicTransformer)(nil)

// TransformBody applies Vertex-specific body modifications.
func (t *VertexAnthropicTransformer) TransformBody(body []byte, _ string, _ bool, headers http.Header) ([]byte, error) {
	allBetas := splitBetaHeaders(headers.Values("anthropic-beta"))
	betas, _ := filterVertexAnthropicBetas(allBetas, body)
	return transformVertexAnthropicBody(body, betas)
}

// SetTokenManager atomically sets the token manager. Called by Router init.
func (t *VertexAnthropicTransformer) SetTokenManager(tm *VertexTokenManager) {
	t.tokenManager.Store(tm)
}

// SetUpstream configures the outbound request for a Vertex AI (Anthropic) upstream.
// After directorSetVertexAnthropicUpstream sets the URL and auth, this method moves
// the anthropic_beta array from the request body to the anthropic-beta HTTP
// header, matching the approach used by litellm for Vertex AI passthrough.
func (t *VertexAnthropicTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, _ string) error {
	params, _ := r.Context().Value(vertexAnthropicContextKey{}).(vertexAnthropicParams)

	tm := t.tokenManager.Load()
	if tm == nil {
		return fmt.Errorf("vertex token manager not initialized")
	}
	accessToken, err := tm.GetToken(upstream.ID)
	if err != nil {
		return err
	}

	directorSetVertexAnthropicUpstream(r, upstream.BaseURL, accessToken, params.Model, params.IsStream)

	// Move anthropic_beta from body to HTTP header.
	if r.Body != nil {
	if body, err := io.ReadAll(r.Body); err == nil {
		r.Body.Close()
		if betasResult := gjson.GetBytes(body, "anthropic_beta"); betasResult.IsArray() {
			var betas []string
			for _, b := range betasResult.Array() {
				betas = append(betas, b.String())
			}
			if len(betas) > 0 {
				r.Header.Set("anthropic-beta", strings.Join(betas, ","))
			}
			body, _ = sjson.DeleteBytes(body, "anthropic_beta")
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	}

	return nil
}

// WrapStream wraps the response body with the Anthropic SSE stream interceptor.
// Vertex AI's streamRawPredict returns standard Anthropic SSE format.
func (t *VertexAnthropicTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
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
func (t *VertexAnthropicTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
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
