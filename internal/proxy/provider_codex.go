package proxy

import (
	"io"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// CodexTransformer handles ChatGPT-subscription codex requests. The wire
// format is OpenAI Responses API; only the auth + a few fingerprint headers
// differ. Body / non-stream parser / stream interceptor reuse OpenAI logic.
type CodexTransformer struct{}

var _ ProviderTransformer = (*CodexTransformer)(nil)

// TransformBody is a pass-through (same as OpenAITransformer).
func (t *CodexTransformer) TransformBody(body []byte, _ string, _ bool, _ http.Header) ([]byte, error) {
	return body, nil
}

// SetUpstream configures the outbound request. apiKey is either a raw access
// token (preferred — set by the Executor via CodexOAuthTokenManager) or a
// CodexCredentials JSON blob (fallback for cold-start before the manager has
// loaded).
func (t *CodexTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
	accessToken, accountID := ParseCodexAccessTokenAndAccount(apiKey)
	directorSetCodexUpstream(r, upstream.BaseURL, accessToken, accountID, upstream.ID)
	return nil
}

// WrapStream reuses the OpenAI Responses SSE interceptor.
func (t *CodexTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
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

// ParseResponse parses non-streaming OpenAI Responses bodies (same as OpenAITransformer).
func (t *CodexTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
	model, respID, usage, err := ParseOpenAINonStreamingResponse(body)
	if err != nil {
		return nil, err
	}
	cached := usage.InputTokensDetails.CachedTokens
	input := usage.InputTokens - cached
	if input < 0 {
		input = 0
	}
	return &ResponseMetrics{
		Model:               model,
		MsgID:               respID,
		InputTokens:         input,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: 0,
		CacheReadTokens:     cached,
	}, nil
}
