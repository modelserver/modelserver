# Bedrock OpenAI-Compatible Channel — Design

Date: 2026-05-01
Status: Approved (pending implementation plan)

## 1. Background

modelserver currently exposes a `bedrock` upstream provider that proxies to
Amazon Bedrock's *Anthropic-format* invoke endpoint
(`POST /model/{modelId}/invoke[-with-response-stream]`). The body wire format is
Anthropic Messages, the streaming format is AWS EventStream binary frames, and
the model id is encoded in the URL path.

AWS has since shipped an OpenAI-compatible endpoint
(`POST /openai/v1/chat/completions`) that accepts standard OpenAI Chat
Completions JSON, returns standard OpenAI responses, and uses Bearer-token
auth (`Authorization: Bearer $AWS_BEARER_TOKEN_BEDROCK`). It supports the
"openai.*" model family hosted on Bedrock (e.g. `openai.gpt-oss-20b-1:0`).

This design adds a second Bedrock channel alongside the existing one, mirroring
the way `vertex-anthropic` and `vertex-openai` coexist. To keep the naming
parallel and unambiguous, the existing `bedrock` provider is renamed to
`bedrock-anthropic`, and a new `bedrock-openai` provider is added.

## 2. Goals / Non-goals

**Goals**
- Support routing OpenAI Chat Completions requests through Amazon Bedrock's
  OpenAI-compatible endpoint as a first-class upstream provider.
- Reuse the existing OpenAI Chat Completions stream interceptor and
  non-streaming response parser; do not duplicate parsing logic.
- Preserve health checks, admin "test connection", and dashboard upstream
  management for the new provider with no special-cases beyond what the
  existing per-provider switch already does.
- Rename the existing `bedrock` provider to `bedrock-anthropic` so the two
  channels have symmetric, descriptive names matching the `vertex-*` family.

**Non-goals**
- SigV4 signing: out of scope. Both Bedrock channels use Bearer-token auth
  (Bedrock API key), matching how the existing `bedrock` provider already
  works.
- OpenAI Responses API support on Bedrock: AWS only exposes Chat Completions
  on this endpoint today. Routes that select `bedrock-openai` upstreams must
  serve `openai_chat_completions` request kind only.
- Cross-channel automatic fallback (Anthropic → OpenAI on the same model).
  Channels are independent upstreams selected by the existing routing engine.

## 3. User-facing configuration

Operators register `bedrock-openai` upstreams the same way they register any
other provider:

| Field | Value |
|-------|-------|
| Provider | `bedrock-openai` |
| Base URL | Region root, e.g. `https://bedrock-runtime.us-west-2.amazonaws.com` |
| API Key  | The Bedrock API key (`AWS_BEARER_TOKEN_BEDROCK`) |
| Supported Models | e.g. `openai.gpt-oss-20b-1:0`, `openai.gpt-oss-120b-1:0` |
| Model Map | Optional canonical → Bedrock model id rewrites |
| Test Model | A supported model id used for the admin connectivity test |

Code appends `/openai/v1/chat/completions` to the base URL when issuing the
request. Storing only the region root keeps the upstream entry consistent with
the existing `bedrock-anthropic` upstream entry (same base URL, different
provider value).

## 4. Architecture

### 4.1 Provider registration

`internal/types/upstream.go`:

```go
const (
    ProviderBedrockAnthropic = "bedrock-anthropic"  // value renamed from "bedrock"
    ProviderBedrockOpenAI    = "bedrock-openai"     // new
    // ... others unchanged
)
```

The old `ProviderBedrock` constant is **removed** so the compiler flags every
call site that still references it. Each call site is updated explicitly to
`ProviderBedrockAnthropic`.

### 4.2 New transformer

Following the established per-provider double-file split (cf. `vertex_openai.go`
+ `provider_vertex_openai.go`):

**`internal/proxy/bedrock_openai.go`** — pure URL/header wiring.

```go
const bedrockOpenAIPath = "/openai/v1/chat/completions"

func directorSetBedrockOpenAIUpstream(req *http.Request, baseURL, apiKey string) {
    endpoint := strings.TrimRight(baseURL, "/") + bedrockOpenAIPath
    target, _ := url.Parse(endpoint)
    req.URL.Scheme, req.URL.Host, req.URL.Path = target.Scheme, target.Host, target.Path
    req.URL.RawPath = target.RawPath
    req.Host = target.Host

    req.Header.Set("Authorization", "Bearer "+apiKey)
    req.Header.Del("x-api-key")
    req.Header.Del("anthropic-version")
    req.Header.Del("anthropic-beta")
    req.Header.Del("x-goog-api-key")
}
```

**`internal/proxy/provider_bedrock_openai.go`** — `ProviderTransformer` impl.

```go
type BedrockOpenAITransformer struct{}

var _ ProviderTransformer = (*BedrockOpenAITransformer)(nil)

func (t *BedrockOpenAITransformer) TransformBody(body []byte, _ string, isStream bool, _ http.Header) ([]byte, error) {
    if isStream && !gjson.GetBytes(body, "stream_options.include_usage").Bool() {
        body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)
    }
    return body, nil
}

func (t *BedrockOpenAITransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
    directorSetBedrockOpenAIUpstream(r, upstream.BaseURL, apiKey)
    return nil
}

func (t *BedrockOpenAITransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
    return newChatCompletionsStreamInterceptor(body, startTime, onComplete)
}

func (t *BedrockOpenAITransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
    return ParseChatCompletionsResponse(body)
}
```

### 4.3 Registration

`internal/proxy/provider_transform.go`:

```go
providerTransformers[types.ProviderBedrockAnthropic] = &BedrockTransformer{}
providerTransformers[types.ProviderBedrockOpenAI]    = &BedrockOpenAITransformer{}
```

### 4.4 Executor adjustments

`internal/proxy/executor.go`:

- The `actualModel != reqCtx.Model` body-rewrite exclusion list (currently
  `Bedrock | VertexAnthropic | Gemini | VertexGoogle`) keeps `BedrockAnthropic`
  but does **not** include `BedrockOpenAI` — the new channel needs the model
  field in the body and benefits from the standard ModelMap rewrite path.
- The `withBedrockParams` injection (which passes resolved model + isStream
  via context for URL construction) is renamed to apply to
  `ProviderBedrockAnthropic` only.
- The streaming `Content-Type: text/event-stream` override
  (`commitStreamingResponse`) stays scoped to `ProviderBedrockAnthropic` only;
  `bedrock-openai` upstream already returns SSE and its `Content-Type` is
  forwarded as-is.

### 4.5 Health check

`internal/proxy/lb/health_checker.go`:

- `case "bedrock"` becomes `case "bedrock-anthropic"`.
- New `case "bedrock-openai": return hc.buildBedrockOpenAIProbe(entry)`.
- `buildBedrockOpenAIProbe` mirrors `buildVertexOpenAIProbe`: POST
  `{baseURL}/openai/v1/chat/completions` with a 1-token Chat Completions body
  and `Authorization: Bearer entry.apiKey`. No token fetcher needed.

### 4.6 Admin connectivity test

`internal/admin/handle_upstreams.go`:

- The two `case types.ProviderBedrock` arms (endpoint construction and header
  setup) become `ProviderBedrockAnthropic`.
- New cases for `ProviderBedrockOpenAI`:
  - endpoint = `baseURL + "/openai/v1/chat/completions"`
  - body = `{"model": testModel, "max_tokens": 10, "messages":[{"role":"user","content":"Hi"}]}`
  - header = `Authorization: Bearer apiKey`

### 4.7 Dashboard

`dashboard/src/pages/admin/UpstreamsPage.tsx`:

- The `<SelectItem value="bedrock">AWS Bedrock</SelectItem>` becomes
  `<SelectItem value="bedrock-anthropic">AWS Bedrock (Anthropic)</SelectItem>`.
- A new `<SelectItem value="bedrock-openai">AWS Bedrock (OpenAI)</SelectItem>`
  is added immediately after.
- Any `provider === "bedrock"` checks in the page are updated to
  `"bedrock-anthropic"`.
- `bedrock-openai` reuses the same form fields as the existing `bedrock` /
  `openai` flows (base URL + API key text input, model list).

### 4.8 DB migration

`internal/store/migrations/034_rename_bedrock_provider.sql`:

```sql
UPDATE upstreams SET provider = 'bedrock-anthropic' WHERE provider = 'bedrock';
UPDATE requests  SET provider = 'bedrock-anthropic' WHERE provider = 'bedrock';
```

This mirrors migration `012_rename_vertex_provider.sql` but extends it to the
`requests` table so historical analytics rows do not split across two values
for the same channel.

## 5. Data flow (request lifecycle)

```
client → handler (KindOpenAIChatCompletions)
       → router matches a group containing a bedrock-openai upstream
       → executor:
           • body rewrite: if ModelMap hits, sjson.SetBytes(body, "model", actualModel)
           • TransformBody: streaming → enforce stream_options.include_usage=true
           • SetUpstream: URL=baseURL+/openai/v1/chat/completions, Authorization: Bearer apiKey
           • httpClient.Do
       → streaming  : newChatCompletionsStreamInterceptor extracts model/usage/TTFT
         non-stream : ParseChatCompletionsResponse extracts usage
       → billing & extra-usage settle on the same code path as vertex-openai
```

## 6. Error handling & retries

No new code paths.

- Retry policy, circuit breaker, connection tracker, and per-upstream timeout
  apply identically to the existing OpenAI / vertex-openai providers.
- A static Bearer token cannot be refreshed in-line: a 401/403 from Bedrock is
  treated as a normal commit error, surfaced to the client. Operators rotate
  the upstream's API key out of band.

## 7. Testing

Three new test files, modelled on the corresponding vertex-openai tests:

- **`internal/proxy/bedrock_openai_test.go`**
  - `TestDirectorSetBedrockOpenAIUpstream` — basic URL composition, scheme,
    host, path.
  - `TestDirectorSetBedrockOpenAIUpstream_TrailingSlash` — base URL with
    trailing slash collapses cleanly.
  - `TestDirectorSetBedrockOpenAIUpstream_StripsConflictingHeaders` —
    `x-api-key`, `anthropic-version`, `anthropic-beta`, `x-goog-api-key` are
    deleted before the bearer header is set.

- **`internal/proxy/provider_bedrock_openai_test.go`**
  - `TestBedrockOpenAITransformerTransformBody_InjectsStreamOptionsForStreaming`
  - `TestBedrockOpenAITransformerTransformBody_NoOpForNonStreaming`
  - `TestBedrockOpenAITransformerTransformBody_PreservesExistingStreamOptions`

- **`internal/proxy/lb/health_checker_test.go`** (extend existing file)
  - `TestBuildBedrockOpenAIProbe` — URL, body shape, bearer header.

End-to-end integration coverage is supplied by the existing executor /
chat-completions tests; the new transformer flows through the same code paths
as `vertex-openai`.

## 8. Migration / rollout

1. Apply DB migration 034 — single transactional update of `upstreams.provider`
   and `requests.provider`.
2. Deploy backend with `bedrock` constant removed and both new constants in
   place. Compile fails immediately surface any missed call site.
3. Deploy dashboard build; existing Bedrock upstreams display under the new
   "AWS Bedrock (Anthropic)" label.
4. Operators add new `bedrock-openai` upstreams via the admin UI as needed.

There is no read-side compatibility shim: after migration 034, the value
`bedrock` no longer appears in the database, and all Go code references
`ProviderBedrockAnthropic` directly.

## 9. Files touched (summary)

**New**
- `internal/proxy/bedrock_openai.go`
- `internal/proxy/bedrock_openai_test.go`
- `internal/proxy/provider_bedrock_openai.go`
- `internal/proxy/provider_bedrock_openai_test.go`
- `internal/store/migrations/034_rename_bedrock_provider.sql`

**Modified**
- `internal/types/upstream.go` (constant rename + new constant)
- `internal/proxy/provider_transform.go` (registration)
- `internal/proxy/executor.go` (rename references; exclusion-list update)
- `internal/proxy/lb/health_checker.go` (rename case + new probe)
- `internal/proxy/lb/health_checker_test.go` (new probe test)
- `internal/admin/handle_upstreams.go` (rename cases + new cases)
- `dashboard/src/pages/admin/UpstreamsPage.tsx` (rename + new option)
- Any other Go file the compiler flags after removing `ProviderBedrock`.
