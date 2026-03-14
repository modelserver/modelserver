# OpenAI Responses API Channel Support

## Overview

Add OpenAI model support to the proxy by introducing a new `/v1/responses` route group. Requests in OpenAI Responses API format are transparently forwarded to OpenAI upstream via `httputil.ReverseProxy`, with response interception to extract usage data for billing and rate limiting.

## Architecture

```
Client → POST /v1/responses → AuthMiddleware → TraceMiddleware → RateLimitMiddleware
       → HandleResponses → ReverseProxy(Director) → api.openai.com/v1/responses
       → ModifyResponse → interceptOpenAINonStreaming / interceptOpenAIStreaming
       → Extract usage → normalize → ComputeCredits → store.CompleteRequest
```

The design mirrors the existing Anthropic `/v1/messages` flow. Both routes share the same middleware chain, channel routing, credit computation, and request recording infrastructure. However, `HandleResponses` uses its own intercept methods (not the Anthropic-typed ones) to avoid coupling to `anthropic.Usage`.

## Usage Field Mapping

Anthropic and OpenAI have different usage semantics that must be normalized before storage.

### Anthropic Usage

```json
{
  "input_tokens": 100,
  "output_tokens": 50,
  "cache_creation_input_tokens": 30,
  "cache_read_input_tokens": 20
}
```

In Anthropic's model, `input_tokens` excludes cache tokens. Cache creation and cache read are separate additive fields.

### OpenAI Usage (Responses API)

```json
{
  "input_tokens": 120,
  "output_tokens": 50,
  "total_tokens": 170,
  "input_tokens_details": {
    "cached_tokens": 80
  },
  "output_tokens_details": {
    "reasoning_tokens": 0
  }
}
```

In OpenAI's model, `input_tokens` is the **total** input count **including** cached tokens. `input_tokens_details.cached_tokens` is a subset of `input_tokens`.

The openai-go SDK (v3) represents this as:

```go
// From github.com/openai/openai-go/v3/responses
type ResponseUsage struct {
    InputTokens         int64                            `json:"input_tokens"`
    InputTokensDetails  ResponseUsageInputTokensDetails  `json:"input_tokens_details"`
    OutputTokens        int64                            `json:"output_tokens"`
    OutputTokensDetails ResponseUsageOutputTokensDetails `json:"output_tokens_details"`
    TotalTokens         int64                            `json:"total_tokens"`
}

type ResponseUsageInputTokensDetails struct {
    CachedTokens int64 `json:"cached_tokens"`
}

type ResponseUsageOutputTokensDetails struct {
    ReasoningTokens int64 `json:"reasoning_tokens"`
}
```

### Normalization (OpenAI → Internal Model)

| Internal Field | OpenAI Source | Formula |
|---|---|---|
| `InputTokens` | `InputTokens`, `InputTokensDetails.CachedTokens` | `InputTokens - InputTokensDetails.CachedTokens` |
| `OutputTokens` | `OutputTokens` | `OutputTokens` |
| `CacheCreationTokens` | N/A | `0` |
| `CacheReadTokens` | `InputTokensDetails.CachedTokens` | `InputTokensDetails.CachedTokens` |

This normalization ensures `ComputeCredits()` works identically for both providers without formula changes.

## SSE Streaming Format

### Anthropic SSE

```
data: {"type":"message_start","message":{"id":"msg_xxx","model":"claude-sonnet-4-6","usage":{...}}}
data: {"type":"content_block_delta","delta":{"text":"Hello"}}
data: {"type":"message_delta","usage":{"output_tokens":50}}
```

- Usage split across `message_start` (input) and `message_delta` (output)
- Event type inside `data` JSON `type` field only

### OpenAI Responses SSE

```
event: response.created
data: {"type":"response.created","response":{"id":"resp_xxx","model":"gpt-5.2"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_xxx","model":"gpt-5.2","usage":{"input_tokens":120,"output_tokens":50,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":0},"total_tokens":170}}}
```

- Usage available in terminal event (`response.completed`, `response.incomplete`, or `response.failed`)
- Event type in both SSE `event:` field AND `data` JSON `type` field
- Model and ID from `response.created` event, confirmed in terminal event

### Terminal Events

The stream can end with one of three terminal events, all of which may contain usage data:

| Event | Meaning | Usage present? |
|---|---|---|
| `response.completed` | Normal completion | Yes |
| `response.incomplete` | Partial result (e.g., `max_output_tokens` hit) | Yes (partial) |
| `response.failed` | Error during generation | Possibly |

The stream interceptor must handle all three to avoid unbilled requests.

### TTFT Measurement

- Anthropic: first `content_block_delta` event
- OpenAI: first `response.output_text.delta` event

## New Files

### `internal/proxy/openai_handler.go`

Contains `HandleResponses` method on the existing `Handler` struct, plus its own `interceptOpenAINonStreaming` and `interceptOpenAIStreaming` methods.

Key differences from `HandleMessages`:

1. **Provider guard**: Validates the selected channel's provider is `ProviderOpenAI`. Returns 503 if a misconfigured route selects a non-OpenAI channel.
2. **Director function**: Uses `directorSetOpenAIUpstream` — sets `Authorization: Bearer <key>` (not `x-api-key`), only changes host/scheme from `baseURL` (path `/v1/responses` is preserved as-is from the client request). Removes `Accept-Encoding` to prevent compressed SSE (same rationale as commit `4e34f44`). Removes Anthropic-specific headers (`x-api-key`, `anthropic-version`).
3. **Stream detection**: Same pattern — reads `"stream": true` from request JSON body.
4. **Own intercept methods**: `interceptOpenAINonStreaming` and `interceptOpenAIStreaming` use OpenAI types (not `anthropic.Usage`) and perform usage normalization before calling `ComputeCredits` and `store.CompleteRequest`.
5. **Model fallback**: Uses model from request body as fallback if the stream interceptor fails to extract it from SSE events (e.g., connection drops before `response.created`).

### `internal/proxy/openai_parser.go`

Two functions mirroring `parser.go`:

- `ParseOpenAINonStreamingResponse(body []byte) (model, respID string, usage responses.ResponseUsage, err error)` — parses full JSON response, extracts `model`, `id`, and `usage` from the top-level response object.
- `ParseOpenAIStreamEvent(data []byte) (eventType, model, respID string, usage responses.ResponseUsage, hasUsage bool)` — parses a single SSE `data:` payload. Returns usage for terminal events (`response.completed`, `response.incomplete`, `response.failed`). Returns model/ID for `response.created`.

Uses `github.com/openai/openai-go/v3/responses` package's `ResponseUsage` type for deserialization.

### `internal/proxy/openai_stream.go`

`openaiStreamInterceptor` — similar to the existing `streamInterceptor` but handles OpenAI SSE format:

- Parses both `event:` prefix lines (to detect event type) and `data:` lines (for JSON payload)
- Extracts model/ID from `response.created` event
- Extracts usage from terminal events (`response.completed`, `response.incomplete`, `response.failed`)
- TTFT on first `response.output_text.delta`
- `onComplete` callback signature uses normalized values directly: `func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64)` — normalization (subtracting cached from input) happens inside the interceptor
- `finish()` guard: fires callback if usage was received, even if model is empty (uses model from request body fallback via closure)

### `internal/proxy/provider/openai.go`

Implements the `Provider` interface:

```go
type OpenAI struct{}

func (o *OpenAI) Name() string { return "openai" }

func (o *OpenAI) Director(req *http.Request, baseURL, apiKey string) {
    // Parse baseURL, set scheme/host (path left unchanged)
    // Set Authorization: Bearer <apiKey>
    // Remove x-api-key, anthropic-version headers
    // Remove Accept-Encoding for stream interceptor compatibility
}
```

## Modified Files

### `internal/proxy/router.go`

Add the new route inside the existing `/v1` group:

```go
r.Route("/v1", func(r chi.Router) {
    r.Use(AuthMiddleware(st, encKey))
    r.Use(TraceMiddleware(traceCfg))
    if limiter != nil {
        r.Use(RateLimitMiddleware(limiter, st, logger))
    }

    // Existing Anthropic routes
    r.Post("/messages", handler.HandleMessages)
    r.Post("/messages/count_tokens", handler.HandleCountTokens)

    // New OpenAI routes
    r.Post("/responses", handler.HandleResponses)

    // Shared routes
    r.Get("/models", handler.HandleListModels)
    r.Get("/usage", handler.HandleUsage)
})
```

### `go.mod`

Add dependency:

```
github.com/openai/openai-go/v3
```

Only the type definitions from the `responses` sub-package are used (`github.com/openai/openai-go/v3/responses`), not the HTTP client.

## Unchanged Components

- `types/channel.go` — already has `ProviderOpenAI = "openai"`
- `types/policy.go` — `ComputeCredits()` formula unchanged
- `types/request.go` — `Request` struct unchanged
- `store/` — database schema unchanged
- `channel_router.go` — channel matching/selection unchanged
- `ratelimit/` — rate limiting unchanged
- Dashboard — already supports creating `provider=openai` channels

## Credit Rate Configuration

For OpenAI models, plan administrators configure `model_credit_rates` as:

```json
{
  "gpt-5.2": {
    "input_rate": 0.35,
    "output_rate": 2.8,
    "cache_creation_rate": 0,
    "cache_read_rate": 0.035
  }
}
```

- `cache_creation_rate`: always 0 for OpenAI (no cache creation charge)
- `cache_read_rate`: typically 1/10 of input_rate (OpenAI's 90% cache discount)

## Testing Strategy

1. Unit tests for `openai_parser.go` — non-streaming and streaming event parsing with real response samples
2. Unit tests for usage normalization (InputTokens - CachedTokens)
3. Unit tests for all three terminal events (completed, incomplete, failed)
4. Integration test with mock upstream returning OpenAI format responses
5. Manual test with real OpenAI API key configured as a channel
