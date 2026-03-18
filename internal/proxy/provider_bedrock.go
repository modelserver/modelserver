package proxy

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelserver/modelserver/internal/types"
)

// bedrockContextKey is used to pass Bedrock-specific parameters through the
// request context from the Executor to SetUpstream.
type bedrockContextKey struct{}

// bedrockParams holds the resolved model and streaming flag that SetUpstream
// needs to construct the Bedrock invoke URL. The Executor stores these in the
// request context before calling SetUpstream.
type bedrockParams struct {
	Model    string
	IsStream bool
}

// withBedrockParams returns a new request with Bedrock routing parameters
// stored in its context. The Executor calls this before SetUpstream so that
// the Bedrock transformer can read the resolved model and streaming flag.
func withBedrockParams(r *http.Request, model string, isStream bool) *http.Request {
	ctx := context.WithValue(r.Context(), bedrockContextKey{}, bedrockParams{
		Model:    model,
		IsStream: isStream,
	})
	return r.WithContext(ctx)
}

// BedrockTransformer handles AWS Bedrock request/response transformations.
// Bedrock uses the Anthropic response format but requires structural body
// modifications and a different URL path format.
type BedrockTransformer struct{}

var _ ProviderTransformer = (*BedrockTransformer)(nil)

// TransformBody applies Bedrock-specific body modifications:
//   - Sets anthropic_version to "bedrock-2023-05-31" if not present
//   - Moves supported anthropic-beta header values into the body
//   - Removes model and stream fields (Bedrock encodes these in the URL)
func (t *BedrockTransformer) TransformBody(body []byte, _ string, _ bool, headers http.Header) ([]byte, error) {
	allBetas := splitBetaHeaders(headers.Values("anthropic-beta"))
	betas, _ := filterBedrockBetas(allBetas)
	return transformBedrockBody(body, betas)
}

// SetUpstream configures the outbound request for a Bedrock upstream.
// It reads the resolved model and streaming flag from the request context
// (set by the Executor via withBedrockParams).
func (t *BedrockTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
	// Extract Bedrock params from context. These are set by the Executor
	// before calling SetUpstream.
	params, _ := r.Context().Value(bedrockContextKey{}).(bedrockParams)
	directorSetBedrockUpstream(r, upstream.BaseURL, apiKey, params.Model, params.IsStream)
	return nil
}

// WrapStream wraps the response body with the Bedrock stream adapter (which
// converts AWS EventStream binary format to SSE text) followed by the
// Anthropic stream interceptor for metrics extraction.
func (t *BedrockTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
	adapted := newBedrockStreamAdapter(body)
	return newStreamInterceptor(adapted, startTime, func(model, msgID string, usage anthropic.Usage, ttft int64) {
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

// ParseResponse extracts metrics from a non-streaming Bedrock response body.
// Bedrock returns Anthropic-format JSON, so the Anthropic parser is used.
func (t *BedrockTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
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
