package proxy

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/modelserver/modelserver/internal/types"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// VertexOpenAITransformer handles Vertex AI's OpenAI-compatible Chat Completions
// endpoint. It uses OAuth2 Bearer token auth (via VertexTokenManager) shared with
// vertex-anthropic and vertex-google, but proxies OpenAI Chat Completions format
// and uses the /endpoints/openapi/chat/completions URL.
type VertexOpenAITransformer struct {
	tokenManager atomic.Pointer[VertexTokenManager]
}

var _ ProviderTransformer = (*VertexOpenAITransformer)(nil)

// TransformBody ensures stream_options.include_usage is set for streaming requests.
// Without this, the Chat Completions stream won't include a usage event and token
// metrics will be recorded as zero.
func (t *VertexOpenAITransformer) TransformBody(body []byte, _ string, isStream bool, _ http.Header) ([]byte, error) {
	if isStream && !gjson.GetBytes(body, "stream_options.include_usage").Bool() {
		body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)
	}
	return body, nil
}

// SetTokenManager atomically sets the token manager. Called by Router init.
func (t *VertexOpenAITransformer) SetTokenManager(tm *VertexTokenManager) {
	t.tokenManager.Store(tm)
}

// SetUpstream configures the outbound request for a Vertex AI OpenAI-compatible upstream.
func (t *VertexOpenAITransformer) SetUpstream(r *http.Request, upstream *types.Upstream, _ string) error {
	tm := t.tokenManager.Load()
	if tm == nil {
		return fmt.Errorf("vertex openai token manager not initialized")
	}
	accessToken, err := tm.GetToken(upstream.ID)
	if err != nil {
		return err
	}

	directorSetVertexOpenAIUpstream(r, upstream.BaseURL, accessToken)
	return nil
}

// WrapStream wraps the response body with the Chat Completions SSE stream interceptor.
func (t *VertexOpenAITransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
	return newChatCompletionsStreamInterceptor(body, startTime, onComplete)
}

// ParseResponse extracts metrics from a non-streaming Chat Completions response.
func (t *VertexOpenAITransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
	return ParseChatCompletionsResponse(body)
}
