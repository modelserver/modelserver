# Google Vertex AI Backend (Claude Models)

## Overview

Add Google Vertex AI as a new upstream provider for Claude models. Vertex AI hosts Anthropic Claude models via `rawPredict`/`streamRawPredict` endpoints, using the same Anthropic request/response wire format. Authentication uses GCP service account JSON keys, exchanged for short-lived OAuth2 access tokens managed in-process.

This design follows the same patterns as the existing Bedrock provider: URL path manipulation, body transformation, and context-passing for per-request parameters.

## Architecture

```
Client → POST /v1/messages → AuthMiddleware → TraceMiddleware → RateLimitMiddleware
       → HandleMessages (allowedProviders += ProviderVertex)
       → Executor.Execute → Router.Match → SelectWithRetry → filter to Vertex upstream
       → VertexTransformer.TransformBody (strip model/stream, inject anthropic_version)
       → withVertexParams(ctx, model, isStream)
       → VertexTransformer.SetUpstream:
           → VertexTokenManager.GetToken(upstreamID) → cached or refresh via oauth2/google
           → Construct URL: {BaseURL}/{model}:rawPredict or :streamRawPredict
           → Set Authorization: Bearer {accessToken}
       → HTTP request to Vertex AI
       → Response: standard Anthropic format → reuse existing stream interceptor / parser
```

## Provider Constant

New constant in `internal/types/upstream.go`:

```go
ProviderVertex = "vertex"
```

The existing `ProviderGemini = "gemini"` is preserved for a future native Gemini integration. Vertex (Claude-on-GCP with service account auth) is semantically distinct.

## BaseURL Format

Admin provides the full base URL when creating a Vertex upstream:

```
https://us-east5-aiplatform.googleapis.com/v1/projects/my-project/locations/us-east5/publishers/anthropic/models
```

The transformer appends `/{model}:rawPredict` (non-streaming) or `/{model}:streamRawPredict` (streaming) at request time. This is consistent with how Bedrock encodes model in the URL path.

**API key field**: Contains the full GCP service account JSON key (the entire JSON file content). Stored encrypted at rest using the existing AES-GCM encryption, same as all other upstream credentials.

## VertexTokenManager

### File: `internal/proxy/vertex_auth.go`

Manages OAuth2 access token lifecycle for Vertex AI upstreams. Each upstream has its own service account and independently cached token.

```go
type VertexTokenManager struct {
    mu     sync.RWMutex
    tokens map[string]*vertexToken  // upstreamID → cached token
}

type vertexToken struct {
    source oauth2.TokenSource  // from google.CredentialsFromJSON
    mu     sync.Mutex          // per-token refresh lock (prevents thundering herd)
}
```

### Methods

- **`NewVertexTokenManager() *VertexTokenManager`** — constructor.

- **`Register(upstreamID string, serviceAccountJSON []byte) error`** — parses the service account JSON, creates a `google.CredentialsFromJSON` with scope `https://www.googleapis.com/auth/cloud-platform`, wraps it in `oauth2.ReuseTokenSource` for automatic caching/refresh, stores in map. Called during Router's `buildMaps()` for each Vertex upstream.

- **`GetToken(upstreamID string) (string, error)`** — returns a valid access token. The underlying `oauth2.ReuseTokenSource` handles caching and automatic refresh when the token is within its expiry window. Per-upstream mutex prevents concurrent refresh storms.

- **`Deregister(upstreamID string)`** — removes from map. Called when an upstream is deleted or disabled.

### Token Refresh Strategy

`oauth2.ReuseTokenSource` (from the standard library) handles token caching and refresh automatically. It returns the cached token if still valid, and transparently calls the underlying `TokenSource` (backed by the service account credentials) when refresh is needed. Google's token endpoint returns tokens with ~1 hour expiry; `ReuseTokenSource` refreshes when the token is within a few minutes of expiry.

No background goroutine is needed — refresh is lazy (triggered by `GetToken` when the cached token is near expiry).

### Dependency

- `golang.org/x/oauth2` — already in `go.mod` (used by OIDC auth)
- `golang.org/x/oauth2/google` — may need explicit import; transitively available via existing deps
- `google.golang.org/api/option` — NOT needed; we only use the token, not a Google API client

## VertexTransformer

### File: `internal/proxy/provider_vertex.go`

Implements `ProviderTransformer`. Unlike other transformers which are stateless, this one holds a pointer to `VertexTokenManager` for token retrieval during `SetUpstream`.

```go
type VertexTransformer struct {
    tokenManager *VertexTokenManager
}
```

### TransformBody

Similar to Bedrock:
- Strips `model` field (encoded in URL path)
- Strips `stream` field (determined by endpoint: rawPredict vs streamRawPredict)
- Sets `anthropic_version: "vertex-2023-10-16"` if not already present
- Moves supported `anthropic-beta` header values into the body (same filter as Bedrock)

### SetUpstream

- Reads `vertexParams{Model, IsStream}` from request context (set by Executor via `withVertexParams`)
- Calls `tokenManager.GetToken(upstream.ID)` to get a valid access token
- Constructs the target URL:
  - Non-streaming: `{BaseURL}/{model}:rawPredict`
  - Streaming: `{BaseURL}/{model}:streamRawPredict`
- Sets `Authorization: Bearer {accessToken}`
- Sets request URL scheme, host, and path

### WrapStream

Vertex AI's `streamRawPredict` returns **standard Anthropic SSE format**. Reuses `newStreamInterceptor()` directly — identical to the Anthropic and ClaudeCode implementations.

### ParseResponse

Vertex AI's `rawPredict` returns **standard Anthropic JSON format**. Reuses `ParseNonStreamingResponse()` directly — identical to the Anthropic and ClaudeCode implementations.

## Context Passing (Bedrock Pattern)

Same pattern as Bedrock for passing resolved model and streaming flag to `SetUpstream`:

```go
// in provider_vertex.go
type vertexContextKey struct{}
type vertexParams struct {
    Model    string
    IsStream bool
}

func withVertexParams(r *http.Request, model string, isStream bool) *http.Request {
    ctx := context.WithValue(r.Context(), vertexContextKey{}, vertexParams{Model: model, IsStream: isStream})
    return r.WithContext(ctx)
}
```

## Integration Points (Modified Files)

### `internal/types/upstream.go`
- Add `ProviderVertex = "vertex"` to provider constants

### `internal/proxy/provider_transform.go`
- Add `RegisterVertexTransformer(tm *VertexTokenManager)` function that adds the Vertex entry to `providerTransformers` map
- Called after token manager is initialized (cannot be in static `init()` since it needs the token manager)

### `internal/proxy/executor.go`
- Add Vertex context injection alongside the existing Bedrock block:
  ```go
  if upstream.Provider == types.ProviderVertex {
      outReq = withVertexParams(outReq, actualModel, reqCtx.IsStream)
  }
  ```

### `internal/proxy/handler.go`
- Add `types.ProviderVertex` to `HandleMessages` allowed providers:
  ```go
  func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
      h.handleProxyRequest(w, r, []string{
          types.ProviderAnthropic,
          types.ProviderBedrock,
          types.ProviderClaudeCode,
          types.ProviderVertex,  // NEW
      })
  }
  ```

### `internal/proxy/lb/health_checker.go`
- Add `case "vertex":` in `buildProbeRequest()`:
  - Uses `rawPredict` endpoint with access token from token manager
  - Health checker needs a reference to `VertexTokenManager` (add to `HealthChecker` struct or pass via closure)

### `internal/admin/handle_upstreams.go`
- Add `case types.ProviderVertex:` in `handleTestUpstream`:
  - Parse service account JSON from decrypted API key
  - Generate access token via `google.CredentialsFromJSON`
  - Build rawPredict request to `{BaseURL}/{testModel}:rawPredict`
  - Set `Authorization: Bearer {token}`

### `internal/admin/handle_channels.go`
- Add `case types.ProviderVertex:` in `handleTestChannel` (same logic as above)

### `internal/proxy/executor.go` (header whitelist)
- `sanitizeOutboundHeaders` already allows `Authorization` and `Content-Type` — no change needed

### Router initialization
- During `buildMaps()` / upstream loading, for Vertex upstreams: call `tokenManager.Register(upstreamID, decryptedKey)`
- `VertexTokenManager` is created once and passed to both the `VertexTransformer` and the Router

### `go.mod`
- Ensure `golang.org/x/oauth2/google` is importable (likely already transitively available)

## New Files

| File | Purpose |
|------|---------|
| `internal/proxy/vertex_auth.go` | `VertexTokenManager` — OAuth2 token lifecycle management |
| `internal/proxy/vertex_auth_test.go` | Unit tests for token manager (mock token source) |
| `internal/proxy/provider_vertex.go` | `VertexTransformer` — `ProviderTransformer` implementation |
| `internal/proxy/provider_vertex_test.go` | Unit tests for body transform, URL construction |

## Modified Files

| File | Change |
|------|--------|
| `internal/types/upstream.go` | Add `ProviderVertex` constant |
| `internal/proxy/provider_transform.go` | Add `RegisterVertexTransformer()` function |
| `internal/proxy/executor.go` | Add Vertex context injection block |
| `internal/proxy/handler.go` | Add `ProviderVertex` to HandleMessages allowed list |
| `internal/proxy/lb/health_checker.go` | Add Vertex health probe builder |
| `internal/admin/handle_upstreams.go` | Add Vertex test case |
| `internal/admin/handle_channels.go` | Add Vertex test case |
| `go.mod` / `go.sum` | Ensure google oauth2 dependency |

## Unchanged Components

- `internal/types/upstream_group.go` — group structure unchanged
- `internal/types/route.go` — route matching unchanged
- `internal/store/` — no schema changes; Vertex upstreams use existing columns
- `internal/proxy/stream.go` — existing `streamInterceptor` reused as-is
- `internal/proxy/parser.go` — existing `ParseNonStreamingResponse` reused as-is
- Dashboard — admin can create `provider=vertex` upstreams via existing CRUD UI
- `internal/ratelimit/` — rate limiting unchanged
- `internal/config/` — no new config fields

## Testing Strategy

1. **Unit tests for `VertexTokenManager`**: Mock `oauth2.TokenSource` to verify caching behavior, refresh timing, and per-upstream isolation
2. **Unit tests for `TransformBody`**: Verify model/stream field stripping, `anthropic_version` injection, beta header migration
3. **Unit tests for URL construction**: Verify `rawPredict` vs `streamRawPredict` endpoint selection, BaseURL parsing
4. **Unit tests for `SetUpstream`**: Verify Authorization header, URL, and host are set correctly
5. **Integration test** (optional/manual): Full request flow with real GCP service account credentials against Vertex AI
