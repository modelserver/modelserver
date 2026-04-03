package proxy

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// VertexGoogleTransformer handles Vertex AI Gemini request/response transformations.
// It uses OAuth2 Bearer token auth (via VertexTokenManager) like the Anthropic Vertex
// transformer, but forwards Gemini native format requests and uses generateContent
// endpoints instead of rawPredict.
type VertexGoogleTransformer struct {
	tokenManager atomic.Pointer[VertexTokenManager]
}

var _ ProviderTransformer = (*VertexGoogleTransformer)(nil)

// TransformBody is a no-op for Vertex Google. The body is already in Gemini native
// format and is forwarded as-is.
func (t *VertexGoogleTransformer) TransformBody(body []byte, _ string, _ bool, _ http.Header) ([]byte, error) {
	return body, nil
}

// SetTokenManager atomically sets the token manager. Called by Router init.
func (t *VertexGoogleTransformer) SetTokenManager(tm *VertexTokenManager) {
	t.tokenManager.Store(tm)
}

// SetUpstream configures the outbound request for a Vertex AI Gemini upstream.
// Gets an OAuth2 token from the shared VertexTokenManager and constructs the
// generateContent / streamGenerateContent endpoint URL.
func (t *VertexGoogleTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, _ string) error {
	params, _ := r.Context().Value(vertexGoogleContextKey{}).(vertexGoogleParams)

	tm := t.tokenManager.Load()
	if tm == nil {
		return fmt.Errorf("vertex google token manager not initialized")
	}
	accessToken, err := tm.GetToken(upstream.ID)
	if err != nil {
		return err
	}

	directorSetVertexGoogleUpstream(r, upstream.BaseURL, accessToken, params.Model, params.IsStream)
	return nil
}

// WrapStream wraps the response body with the Gemini SSE stream interceptor.
// Vertex AI Gemini streaming returns the same SSE format as the native Gemini API.
func (t *VertexGoogleTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
	return newGeminiStreamInterceptor(body, startTime, onComplete)
}

// ParseResponse extracts metrics from a non-streaming Vertex AI Gemini response body.
func (t *VertexGoogleTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
	return ParseGeminiResponse(body)
}
