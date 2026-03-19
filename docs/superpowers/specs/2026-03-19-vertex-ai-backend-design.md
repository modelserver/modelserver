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
    source oauth2.TokenSource  // oauth2.ReuseTokenSource wrapping google.CredentialsFromJSON
    // No per-token mutex needed: oauth2.ReuseTokenSource serializes refresh internally.
}
```

### Methods

- **`NewVertexTokenManager() *VertexTokenManager`** — constructor.

- **`Register(upstreamID string, serviceAccountJSON []byte) error`** — parses the service account JSON, creates a `google.CredentialsFromJSON` with scope `https://www.googleapis.com/auth/cloud-platform`, wraps it in `oauth2.ReuseTokenSource` for automatic caching/refresh, stores in map. Called during Router's `buildMaps()` for each Vertex upstream.

- **`GetToken(upstreamID string) (string, error)`** — returns a valid access token. The underlying `oauth2.ReuseTokenSource` handles caching, automatic refresh, and internal serialization (no external mutex needed).

- **`Clear()`** — removes all entries from the map under write lock. Called by `Router.Reload()` before rebuilding maps.

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
- Sets `anthropic_version: "vertex-2023-10-16"` if not already present (verify against current Google Vertex AI Claude documentation at implementation time — version string may have been updated)
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
- **Model-rewrite guard**: The existing code at line ~197 skips model-field rewriting for Bedrock (since Bedrock strips it). Vertex also strips the model field, so add `ProviderVertex` to the exclusion:
  ```go
  if actualModel != reqCtx.Model && upstream.Provider != types.ProviderBedrock && upstream.Provider != types.ProviderVertex {
      bodyForTransform, _ = sjson.SetBytes(...)
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
- Add `case "vertex":` in `buildProbeRequest()`
- **Import cycle solution**: `HealthChecker` (in `lb` package) cannot import `proxy.VertexTokenManager` directly (would create `proxy` → `lb` → `proxy` cycle). Instead, define a `TokenFetcher` callback type in the `lb` package:
  ```go
  // TokenFetcher retrieves a valid access token for the given upstream.
  // Used by health checker for providers that require dynamic token refresh.
  type TokenFetcher func(upstreamID string) (string, error)
  ```
  Add an optional `TokenFetcher` field to `HealthChecker`. At construction time (in `main.go` or Router init), pass a closure that calls `tokenManager.GetToken`. The Vertex health probe uses this callback to get a fresh access token for the probe request.
- Vertex probe: POST to `{baseURL}/{testModel}:rawPredict` with `Authorization: Bearer {token}`, same minimal body as Anthropic probe

### `internal/admin/handle_upstreams.go`
- Add `case types.ProviderVertex:` in `handleTestUpstream`:
  - Parse service account JSON from decrypted API key
  - Generate access token inline via `google.CredentialsFromJSON` (one-shot, not reusing `VertexTokenManager` — test handlers are independent of the proxy pipeline and run infrequently, so duplication is acceptable)
  - Build rawPredict request to `{BaseURL}/{testModel}:rawPredict`
  - Set `Authorization: Bearer {token}`
  - Body: Anthropic format with `anthropic_version`, `max_tokens: 10`, minimal message

### `internal/admin/handle_channels.go`
- Add `case types.ProviderVertex:` in `handleTestChannel` (same logic as above)

### `internal/proxy/executor.go` (header whitelist)
- `sanitizeOutboundHeaders` already allows `Authorization` and `Content-Type` — no change needed

### Router initialization
- During `buildMaps()` / upstream loading, for Vertex upstreams: call `tokenManager.Register(upstreamID, decryptedKey)`
- `VertexTokenManager` is created once and passed to both the `VertexTransformer` and the Router
- **On `Reload()`**: Before rebuilding maps, call `tokenManager.Clear()` to remove all entries, then re-register during the new `buildMaps()` pass. This ensures key rotations take effect without a server restart. `Clear()` is a simple method that resets the internal map under the write lock.

### `internal/proxy/handler.go` (count_tokens)
- Add `types.ProviderVertex` to the `HandleCountTokens` provider filter alongside Anthropic and ClaudeCode. Vertex's `rawPredict` supports the full Anthropic API surface including count_tokens.

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
| `dashboard/src/pages/admin/UpstreamsPage.tsx` | Add `vertex` to provider dropdown |
| `dashboard/src/pages/admin/ChannelsPage.tsx` | Add `vertex` to provider dropdowns (create + edit) |
| Router init (e.g. `router_engine.go`) | Pass `TokenFetcher` closure to `NewHealthChecker` |

## Unchanged Components

- `internal/types/upstream_group.go` — group structure unchanged
- `internal/types/route.go` — route matching unchanged
- `internal/store/` — no schema changes; Vertex upstreams use existing columns
- `internal/proxy/stream.go` — existing `streamInterceptor` reused as-is
- `internal/proxy/parser.go` — existing `ParseNonStreamingResponse` reused as-is
- Dashboard — see Dashboard Changes section below
- `internal/ratelimit/` — rate limiting unchanged
- `internal/config/` — no new config fields

## Dashboard Changes

Both `UpstreamsPage.tsx` and `ChannelsPage.tsx` need:

1. **Provider dropdown**: Add `<SelectItem value="vertex">Google Vertex AI</SelectItem>` to all provider `<Select>` components (create and edit dialogs).

2. **API Key input**: When `provider === "vertex"`, replace the single-line `<Input type="password">` with a multi-line `<textarea>` for pasting the service account JSON. This follows the same conditional rendering pattern already used for `claudecode` (which shows an OAuth flow instead of a text input). Example:
   ```tsx
   {form.provider === "vertex" ? (
     <Textarea
       value={form.api_key}
       onChange={(e) => updateForm("api_key", e.target.value)}
       placeholder='Paste service account JSON key here...'
       rows={6}
       className="font-mono text-xs"
     />
   ) : (
     <Input type="password" ... />
   )}
   ```

3. **Base URL placeholder**: When `provider === "vertex"`, update the placeholder to guide the user:
   ```
   https://REGION-aiplatform.googleapis.com/v1/projects/PROJECT/locations/REGION/publishers/anthropic/models
   ```

## Notes

- **Error response format**: Vertex AI error responses may use Google-style error envelopes (`{"error": {"code": 400, "message": "...", "status": "INVALID_ARGUMENT"}}`) rather than Anthropic's format. The proxy's `commitErrorResponse` reads and forwards the raw body to the client, so this is transparent. No special error handling needed.
- **Proxy support for token refresh**: The `oauth2` HTTP client used for token refresh does not automatically use `HTTP_PROXY`/`HTTPS_PROXY` environment variables. If the deployment requires a proxy to reach `oauth2.googleapis.com`, pass a custom `http.Client` with proxy support via `option.WithHTTPClient()` when constructing credentials. This is an edge case — most deployments have direct internet access for the token endpoint.

## Testing Strategy

1. **Unit tests for `VertexTokenManager`**: Mock `oauth2.TokenSource` to verify caching behavior, refresh timing, and per-upstream isolation
2. **Unit tests for `TransformBody`**: Verify model/stream field stripping, `anthropic_version` injection, beta header migration
3. **Unit tests for URL construction**: Verify `rawPredict` vs `streamRawPredict` endpoint selection, BaseURL parsing
4. **Unit tests for `SetUpstream`**: Verify Authorization header, URL, and host are set correctly
5. **Integration test** (optional/manual): Full request flow with real GCP service account credentials against Vertex AI
