# Vertex AI Google (Gemini) Provider Design

**Date:** 2026-04-03
**Status:** Approved

## Problem

The Vertex AI upstream (`ProviderVertex`) currently only supports Anthropic models via the `rawPredict` / `streamRawPredict` endpoints. Users who want to access Gemini models through Vertex AI (using OAuth2 service account auth instead of API keys) have no way to do so.

## Solution

Add a new `ProviderVertexGoogle` ("vertex-google") provider type that routes Gemini-native-format requests to Vertex AI's `generateContent` / `streamGenerateContent` endpoints, authenticated via the same OAuth2 service account mechanism as the existing Vertex provider. The name "vertex-google" aligns with the Vertex AI publisher namespace (`publishers/google/models`).

## Architecture

### Provider Type

New constant `ProviderVertexGoogle = "vertex-google"` in `internal/types/upstream.go`.

Comparison of the three related providers:

| Aspect | `vertex` | `gemini` | `vertex-google` (new) |
|--------|----------|----------|-----------------------|
| Input endpoint | `/v1/messages` | `/v1beta/models/*` | `/v1beta/models/*` |
| Request format | Anthropic | Gemini native | Gemini native |
| Response format | Anthropic | Gemini native | Gemini native |
| Auth | OAuth2 Bearer (service account) | API key | OAuth2 Bearer (service account) |
| Upstream URL | `{baseURL}/{model}:rawPredict` | `{baseURL}/v1beta/models/{model}:generateContent` | `{baseURL}/{model}:generateContent` |
| baseURL example | `.../publishers/anthropic/models` | `https://generativelanguage.googleapis.com` | `.../publishers/google/models` |

### Transformer: `VertexGoogleTransformer`

Implements `ProviderTransformer` with:

- **TransformBody**: No-op. Gemini native format forwarded as-is.
- **SetUpstream**: Gets OAuth2 token from shared `VertexTokenManager`, constructs endpoint URL, sets `Authorization: Bearer` header.
- **WrapStream**: Reuses `newGeminiStreamInterceptor` (identical SSE format).
- **ParseResponse**: Reuses `ParseGeminiResponse` (identical response format).

### URL Construction

```
baseURL = "https://REGION-aiplatform.googleapis.com/v1beta1/projects/PROJECT/locations/REGION/publishers/google/models"

Non-streaming: {baseURL}/{model}:generateContent
Streaming:     {baseURL}/{model}:streamGenerateContent?alt=sse
```

### Authentication

Reuses the existing `VertexTokenManager`. Service account JSON is stored as the upstream's encrypted API key. The router registers `vertex-google` upstreams with the same token manager as `vertex` upstreams.

### Routing

The `/v1beta/models/*` handler (`HandleGemini`) adds `ProviderVertexGoogle` to its `AllowedProviders` list alongside `ProviderGemini`:

```go
AllowedProviders: []string{types.ProviderGemini, types.ProviderVertexGoogle}
```

### Health Check

New `buildVertexGoogleProbe`:
- Request body: Gemini native format (`contents` + `generationConfig`)
- URL: `{baseURL}/{testModel}:generateContent`
- Auth: Bearer token from `tokenFetcher`

### Dashboard

- New provider option: "Google Vertex AI (Gemini)"
- BaseURL placeholder: `https://REGION-aiplatform.googleapis.com/v1beta1/projects/PROJECT/locations/REGION/publishers/google/models`
- Credential field: "Service Account JSON" (same as existing Vertex)

## Files Changed

| File | Change |
|------|--------|
| `internal/types/upstream.go` | Add `ProviderVertexGoogle` constant |
| `internal/proxy/vertex_google.go` | **New** — URL construction, context key, director |
| `internal/proxy/provider_vertex_google.go` | **New** — `VertexGoogleTransformer` impl |
| `internal/proxy/provider_transform.go` | Register transformer, add `SetVertexGoogleTokenManager` |
| `internal/proxy/executor.go` | Add context params injection for `ProviderVertexGoogle` |
| `internal/proxy/handler.go` | Add `ProviderVertexGoogle` to `HandleGemini` allowed providers |
| `internal/proxy/router_engine.go` | Register vertex-google upstreams with token manager |
| `internal/proxy/lb/health_checker.go` | Add `buildVertexGoogleProbe` |
| `dashboard/src/pages/admin/UpstreamsPage.tsx` | Add provider option + UI hints |
| `internal/proxy/vertex_google_test.go` | **New** — URL & transformer tests |

## What is NOT in scope

- Format conversion between Anthropic and Gemini (no Anthropic→Gemini translation)
- Vertex AI Express Mode (API key auth for Vertex) — can be added later if needed
