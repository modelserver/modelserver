# OpenAI Images API Support (Generations + Edits)

**Status:** Approved (design)
**Date:** 2026-04-23
**Owner:** mryao

## Problem

The proxy exposes OpenAI-format `/v1/responses` and `/v1/chat/completions`
today but not `/v1/images/generations` or `/v1/images/edits`. Clients that
want `gpt-image-2` image generation through this gateway have no path.

Adding the two endpoints is more than "one more handler": they differ from
every existing proxied endpoint in ways that cross the ingress, executor,
response-parsing, and billing layers simultaneously.

1. **`/v1/images/edits` accepts both JSON references and multipart file
   uploads.** JSON edit requests can carry `images`/`mask` references such as
   `file_id` or `image_url`; multipart edit requests carry uploaded image
   files. The existing `handleProxyRequest` + `ResolveModelMiddleware`
   pipeline handles JSON bodies but cannot peek or rewrite multipart bodies.
2. **Image request bodies can be much larger**. `images/edits` accepts up
   to 16 reference images per request; real photo inputs routinely push
   the total past today's `maxBodySize` cap.
3. **Image responses have a different usage shape**. `gpt-image-2` reports
   six token classes (text/image × input/cached/output) with distinct
   per-class prices ($5 / $1.25 / $10 / $8 / $2 / $30 per 1M tokens). The
   existing `CreditRate`'s four fields cannot represent them without
   either schema pressure or lossy blending.
4. **Streaming uses different SSE event types**. Image endpoints emit
   `image_generation.partial_image`/`completed` and
   `image_edit.partial_image`/`completed` events, not the `response.*`
   event family the existing OpenAI stream interceptor recognizes.

## Decision

Add both endpoints in one iteration, covering `gpt-image-2` streaming and
non-streaming. Integrate along the minimum-invasion path: new
`request_kind` constants, new handlers, a new response parser pair
(non-stream + stream), a new `ImageCreditRate` and cost calculator, and
targeted branches in the shared `Executor`. No new executor, no new
transformer, no changes to `Router.Match`.

## Scope

**In scope:**

- `POST /v1/images/generations` (JSON, stream + non-stream)
- `POST /v1/images/edits` (JSON references and multipart/form-data uploads,
  stream + non-stream)
- `gpt-image-2` billing via a new `ImageCreditRate` on the model catalog
- Routing via the existing `request_kinds` column (two new constants)
- Admin API / dashboard changes to edit the image rate on a model

**Out of scope (v1):**

- Image models other than `gpt-image-2`. v1 is intentionally scoped to one
  model. Operators should not create catalog/routes entries for other image
  models until they are explicitly added to scope.
- Plan-level override of image rates (`ModelImageCreditRates`).
  Configuration lives exclusively on the model catalog in v1.
- Upstream `ModelMap` rewrite for `images/edits`. Multipart re-serialization
  across retries is expensive; catalog aliases (`Model.Aliases`) cover the
  naming use case.
- Dashboard rendering of per-request six-class token breakdown. Detail is
  written to request `metadata` and can be viewed via admin API; UI work
  deferred.
- Admin `GET /admin/models/{name}/pricing-preview` endpoint. Actual cost
  lands on the request row after the call; no synthetic estimator.
- Partial-image-aware billing when a stream is cut off mid-flight. Aborted
  streams are charged zero.
- Batch pricing. No batch API path through this proxy.

## Architecture

Two new routes on `/v1`:

```
POST /v1/images/generations   Content-Type: application/json
POST /v1/images/edits         Content-Type: application/json or multipart/form-data
```

Both sit behind the same middleware chain as every other `/v1` route:
`Auth → Trace → ResolveModel → SubscriptionEligibility → RateLimit →
ExtraUsageGuard → Handler`. The only middleware that needs modification is
`ResolveModelMiddleware`, which today reads JSON to extract `model` and needs
an additional multipart branch.

Request flow for `/v1/images/edits` multipart uploads (non-stream):

```
client POST /v1/images/edits (multipart)
  → AuthMiddleware (unchanged)
  → TraceMiddleware (unchanged)
  → ResolveModelMiddleware (NEW: multipart-aware extraction)
  → SubscriptionEligibility / RateLimit / ExtraUsageGuard (unchanged)
  → HandleImagesEdits (NEW)
      ├─ read body w/ images-specific cap
      ├─ parse multipart to extract model + stream flag
      ├─ resolve canonical model via catalog
      ├─ rewrite multipart model field if alias differs
      ├─ insert pending Request row
      └─ Executor.Execute(reqCtx with RequestKind=openai_images_edits)
  → Executor (EXISTING w/ kind-branches)
      ├─ Router.Match(project, model, kind) (unchanged)
      ├─ SelectWithRetry (unchanged)
      ├─ retry loop: skip TransformBody for multipart edit bodies
      ├─ outReq Content-Type = original multipart (preserves boundary)
      ├─ parse response by kind:
      │   non-stream: ParseImageNonStreamingResponse → settleImageExtraUsage
      │   stream:     newImageStreamInterceptor → (on completed) settleImageExtraUsage
      └─ collector.Record (4 token cols + metadata)
  → response to client
```

`/v1/images/generations` is a JSON body and goes through the existing
`handleProxyRequest` verbatim (one-line wrapper), with the kind-branched
response parsing and billing. JSON-form `/v1/images/edits` also uses the
shared JSON path; only multipart edits need a bespoke body handler.

## Request kinds & routing

Two new constants in `internal/types/request_kind.go`:

```go
const (
    // ...existing...
    KindOpenAIImagesGenerations = "openai_images_generations"
    KindOpenAIImagesEdits       = "openai_images_edits"
)
```

`AllRequestKinds` appends both. `IsValidRequestKind` covers them. Table
test in `request_kind_test.go` asserts the two constants are accepted.

`internal/proxy/router.go` registers the routes inside the existing `/v1`
block, after `wire(r)`:

```go
r.Post("/images/generations", handler.HandleImagesGenerations)
r.Post("/images/edits", handler.HandleImagesEdits)
```

`Router.Match` already accepts `(projectID, model, kind)` and needs no
change. Admin API's `GET /admin/routing/request-kinds` returns the new
constants automatically (it iterates `AllRequestKinds`).

### Migration 028 — update CHECK constraint

```sql
-- internal/store/migrations/028_images_request_kinds.sql
BEGIN;

ALTER TABLE routes DROP CONSTRAINT routes_request_kinds_valid;

ALTER TABLE routes ADD CONSTRAINT routes_request_kinds_valid CHECK (
  request_kinds <@ ARRAY[
    'anthropic_messages',
    'anthropic_count_tokens',
    'openai_chat_completions',
    'openai_responses',
    'google_generate_content',
    'openai_images_generations',
    'openai_images_edits'
  ]::TEXT[]
  AND array_length(request_kinds, 1) >= 1
);

COMMIT;
```

No backfill. Images endpoints 404 until an operator explicitly configures
a route with the new kinds. Error message (via the existing
`Router.Match` error path): `no route configured for model gpt-image-2 on
endpoint openai_images_generations`.

### Rollback compatibility

No `scanRoute` code change is required for rollback compatibility. The store
currently scans `request_kinds` as strings and does not validate them during
row loading; validation happens at admin write time and in the database CHECK
constraint. An older server binary can therefore read rows containing the new
image kinds after migration 028. Those routes simply never match old request
kinds, which is equivalent to the route being absent for pre-images traffic.

Add a regression test around `ListRoutes`/`scanRoute` with an unknown
`request_kind` so this assumption stays true.

## Config

New section in `internal/config/config.go`:

```go
type ImagesConfig struct {
    MaxBodySize int64 `mapstructure:"max_body_size"`
}

type Config struct {
    // ...existing fields...
    Images ImagesConfig `mapstructure:"images"`
}

// in defaults:
v.SetDefault("images.max_body_size", 200 << 20) // 200 MiB
```

`main.go` passes `cfg.Images.MaxBodySize` to `NewHandler`, `NewExecutor`, and
`ResolveModelMiddleware`. The middleware already takes `maxBodySize` for JSON
bodies; the new argument is `multipartMaxBodySize` for the multipart branch.
The executor also needs the image limit because it re-reads the request body
for retry caching after the handler has validated it.

## Ingress

### 1. `ResolveModelMiddleware` — multipart extraction

`extractModelFromRequest` gains a multipart branch keyed off `Content-Type`:

```go
func extractModelFromRequest(r *http.Request, jsonLimit, multipartLimit int64) string {
    // Gemini URL-path branch (unchanged)
    if strings.HasPrefix(r.URL.Path, "/v1beta/models/") { ... }

    mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
    if strings.EqualFold(mediaType, "multipart/form-data") {
        return extractModelFromMultipart(r, multipartLimit)
    }
    // JSON branch (existing logic, limit = jsonLimit)
}

func extractModelFromMultipart(r *http.Request, limit int64) string {
    if r.Body == nil {
        return ""
    }
    body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
    if err != nil {
        return ""
    }
    r.Body = io.NopCloser(bytes.NewReader(body))
    if int64(len(body)) > limit {
        return ""
    }
    _, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
    if err != nil || params["boundary"] == "" {
        return ""
    }
    mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
    for {
        part, err := mr.NextPart()
        if err != nil {
            return "" // incl. io.EOF — model not found
        }
        if part.FormName() == "model" {
            val, _ := io.ReadAll(io.LimitReader(part, 256))
            return strings.TrimSpace(string(val))
        }
    }
}
```

The middleware remains permissive on failure (empty string → no catalog
match → handler renders its own error envelope), preserving today's
contract.

### 2. `HandleImagesGenerations` (JSON)

```go
// internal/proxy/handler.go
func (h *Handler) HandleImagesGenerations(w http.ResponseWriter, r *http.Request) {
    h.handleProxyRequest(w, r, IngressOpenAI, types.KindOpenAIImagesGenerations)
}
```

`handleProxyRequest` is reused as-is. The CCH/fingerprint metadata capture
is gated on `Publisher == Anthropic` and never fires for OpenAI models,
so it's a silent no-op for images.

### 3. `HandleImagesEdits` (JSON or multipart)

The handler first parses the media type:

```go
func (h *Handler) HandleImagesEdits(w http.ResponseWriter, r *http.Request) {
    mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
    switch strings.ToLower(mediaType) {
    case "multipart/form-data":
        h.handleImagesEditsMultipart(w, r)
    case "application/json", "":
        h.handleProxyRequest(w, r, IngressOpenAI, types.KindOpenAIImagesEdits)
    default:
        writeProxyError(w, http.StatusUnsupportedMediaType, "unsupported content type")
    }
}
```

JSON-form edits intentionally reuse `handleProxyRequest`; model extraction,
alias rewrite, request row insertion, retries, and upstream model-map rewrites
all work the same way as generations.

Multipart edits use a bespoke helper with structure symmetric to
`handleProxyRequest`:

```go
func (h *Handler) handleImagesEditsMultipart(w http.ResponseWriter, r *http.Request) {
    apiKey := APIKeyFromContext(r.Context())
    project := ProjectFromContext(r.Context())
    if apiKey == nil || project == nil {
        writeProxyError(w, http.StatusInternalServerError, "missing auth context")
        return
    }

    bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.imagesMaxBodySize))
    if err != nil {
        writeProxyError(w, http.StatusRequestEntityTooLarge, "request body too large")
        return
    }
    r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

    _, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
    if err != nil || params["boundary"] == "" {
        writeProxyError(w, http.StatusBadRequest, "invalid multipart request")
        return
    }
    boundary := params["boundary"]

    model, isStream, err := peekMultipartFields(bodyBytes, boundary)
    if err != nil {
        writeProxyError(w, http.StatusBadRequest, "multipart parse failed")
        return
    }

    canonical, ok := h.resolveModel(w, model, IngressOpenAI)
    if !ok {
        return
    }

    if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, canonical) {
        writeProxyError(w, http.StatusForbidden, "model not allowed for this API key")
        return
    }

    if canonical != model {
        bodyBytes, err = rewriteMultipartField(bodyBytes, boundary, "model", canonical)
        if err != nil {
            writeProxyError(w, http.StatusInternalServerError, "multipart rewrite failed")
            return
        }
        r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
        r.ContentLength = int64(len(bodyBytes))
    }

    // ...pending Request row insertion (mirrors handleProxyRequest)...
    // ...RequestContext construction with RequestKind = KindOpenAIImagesEdits, IsStream = isStream...

    h.executor.Execute(w, r, reqCtx)
}
```

Helpers in a new file `internal/proxy/multipart_util.go`:

- `peekMultipartFields(body []byte, boundary string) (model string, stream bool, err error)`
  — iterates parts, extracts `model` and `stream` form values; file parts
  (e.g. `image[]`, `mask`) are skipped without being read.
- `rewriteMultipartField(body []byte, boundary, name, value string) ([]byte, error)`
  — reads all parts through a `multipart.Reader`, writes them to a
  `multipart.Writer`, substituting the named form field. Preserves
  insertion order and per-part headers.

## Execution

`Executor.Execute` framework is unchanged. Targeted changes:

### 1. Use the image body cap for multipart edits

`Executor.Execute` currently re-reads the body for retry caching and enforces
`e.maxBodySize`. That would still reject large edit uploads even after the
handler accepted them. Add `imagesMaxBodySize` to `Executor` and select the
limit from the original request content type:

```go
mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
isImagesEditMultipart := reqCtx.RequestKind == types.KindOpenAIImagesEdits &&
    strings.EqualFold(mediaType, "multipart/form-data")

bodyLimit := e.maxBodySize
if isImagesEditMultipart && e.imagesMaxBodySize > 0 {
    bodyLimit = e.imagesMaxBodySize
}

originalBody, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
if err != nil {
    writeProxyError(w, http.StatusBadRequest, "failed to read request body")
    return
}
if int64(len(originalBody)) > bodyLimit {
    writeProxyError(w, http.StatusRequestEntityTooLarge, "request body too large")
    return
}
```

### 2. Skip body transform only for multipart edit bodies

Around the existing body-cache / TransformBody block (current lines
265-294 of `executor.go`):

```go
transformedBody, ok := bodyCache[cacheKey]
if !ok {
    bodyForTransform := originalBody

    if !isImagesEditMultipart {
        // JSON endpoints: sjson-based model rewrite (unchanged)
        if actualModel != reqCtx.Model && upstream.Provider != types.ProviderBedrock && /* ... */ {
            bodyForTransform, _ = sjson.SetBytes(append([]byte{}, originalBody...), "model", actualModel)
        }
    }

    if isImagesEditMultipart {
        transformedBody = bodyForTransform // opaque passthrough
    } else {
        transformedBody, err = transformer.TransformBody(bodyForTransform, actualModel, reqCtx.IsStream, r.Header)
        if err != nil {
            logger.Error("body transform failed", "error", err)
            continue
        }
    }

    if upstream.Provider == types.ProviderClaudeCode {
        transformedBody = normalizeRequestBody(transformedBody, DeriveClaudeCodeDeviceID(upstream.ID))
    }
    bodyCache[cacheKey] = transformedBody
}
```

`KindOpenAIImagesGenerations` is not special-cased here — it's JSON and
`OpenAITransformer.TransformBody` is already a no-op for OpenAI upstreams.
JSON-form `KindOpenAIImagesEdits` is also not special-cased; it should retain
normal JSON model rewrites, including upstream `ModelMap`.

### 3. Preserve multipart Content-Type on outgoing request

Current `executor.go` hardcodes at ~line 305:

```go
outReq.Header.Set("Content-Type", "application/json")
```

Replace with:

```go
if isImagesEditMultipart {
    outReq.Header.Set("Content-Type", r.Header.Get("Content-Type")) // preserves boundary
} else {
    outReq.Header.Set("Content-Type", "application/json")
}
```

### 4. Upstream `ModelMap` is ignored only for multipart `openai_images_edits`

`actualModel` is still computed for logging and request-row fields. Multipart
edit bodies are not rewritten per-upstream. If an operator needs model
aliasing for multipart uploads, they configure `Model.Aliases` in the catalog
instead. JSON-form edits keep the existing JSON `ModelMap` rewrite.

Document this multipart-only limitation on the `Upstream.ModelMap` field via
a comment.

### 5. Response parsing and settle: branch on `RequestKind`

In the executor's non-stream commit path (around line 800+), replace the
unconditional `transformer.ParseResponse(body)` + `settleExtraUsage` with:

```go
switch reqCtx.RequestKind {
case types.KindOpenAIImagesGenerations, types.KindOpenAIImagesEdits:
    imgMetrics, err := ParseImageNonStreamingResponse(body)
    if err != nil {
        e.logger.Warn("image response parse failed", "error", err)
    }
    if imgMetrics != nil && imgMetrics.UsagePresent {
        e.settleImageExtraUsage(context.Background(), reqCtx, imgMetrics.Usage)
    } else if reqCtx.HasExtraUsageCtx {
        e.logger.Warn("image response has no usage; skipping extra-usage settle",
            "project_id", reqCtx.ProjectID, "request_id", reqCtx.RequestID)
    }
    e.collector.Record(/* see "Token persistence" below */)
default:
    metrics, err := transformer.ParseResponse(body)
    // ...existing logic unchanged...
}
```

In the stream wrap path (around line 900+), the equivalent branch:

```go
switch reqCtx.RequestKind {
case types.KindOpenAIImagesGenerations, types.KindOpenAIImagesEdits:
    body = newImageStreamInterceptor(body, startTime, func(u ImageTokenUsage, usagePresent bool, ttftMs int64) {
        if usagePresent {
            e.settleImageExtraUsage(context.Background(), reqCtx, u)
        } else if reqCtx.HasExtraUsageCtx {
            e.logger.Warn("image stream completed without usage; skipping extra-usage settle",
                "project_id", reqCtx.ProjectID, "request_id", reqCtx.RequestID)
        }
        e.collector.Record(/* ... */)
    })
default:
    body = transformer.WrapStream(body, startTime, onComplete)
}
```

`OpenAITransformer` is not modified.

## Response parsing

### Non-stream: `image_parser.go`

```go
// internal/proxy/image_parser.go
package proxy

import "encoding/json"

type ImageTokenUsage struct {
    InputTokens       int64
    OutputTokens      int64
    TotalTokens       int64
    TextInputTokens   int64
    ImageInputTokens  int64
    CachedInputTokens int64
    TextOutputTokens  int64
    ImageOutputTokens int64
}

type ImageResponseMetrics struct {
    Model        string
    Usage        ImageTokenUsage
    UsagePresent bool
}

type imageResponseEnvelope struct {
    Usage *struct {
        InputTokens        int64 `json:"input_tokens"`
        OutputTokens       int64 `json:"output_tokens"`
        TotalTokens        int64 `json:"total_tokens"`
        InputTokensDetails *struct {
            TextTokens   int64 `json:"text_tokens"`
            ImageTokens  int64 `json:"image_tokens"`
            CachedTokens int64 `json:"cached_tokens"`
        } `json:"input_tokens_details"`
        OutputTokensDetails *struct {
            TextTokens  int64 `json:"text_tokens"`
            ImageTokens int64 `json:"image_tokens"`
        } `json:"output_tokens_details"`
    } `json:"usage"`
}

func ParseImageNonStreamingResponse(body []byte) (*ImageResponseMetrics, error) {
    var env imageResponseEnvelope
    if err := json.Unmarshal(body, &env); err != nil {
        return nil, err
    }
    m := &ImageResponseMetrics{}
    if env.Usage != nil {
        m.UsagePresent = true
        m.Usage.InputTokens = env.Usage.InputTokens
        m.Usage.OutputTokens = env.Usage.OutputTokens
        m.Usage.TotalTokens = env.Usage.TotalTokens
        if env.Usage.InputTokensDetails != nil {
            m.Usage.TextInputTokens = env.Usage.InputTokensDetails.TextTokens
            m.Usage.ImageInputTokens = env.Usage.InputTokensDetails.ImageTokens
            m.Usage.CachedInputTokens = env.Usage.InputTokensDetails.CachedTokens
        }
        if env.Usage.OutputTokensDetails != nil {
            m.Usage.TextOutputTokens = env.Usage.OutputTokensDetails.TextTokens
            m.Usage.ImageOutputTokens = env.Usage.OutputTokensDetails.ImageTokens
        } else if env.Usage.OutputTokens > 0 {
            // Image streaming completed events currently report output_tokens
            // without output_tokens_details; for image models that output is
            // image-token output.
            m.Usage.ImageOutputTokens = env.Usage.OutputTokens
        }
        if env.Usage.OutputTokensDetails != nil &&
            env.Usage.OutputTokensDetails.TextTokens == 0 &&
            env.Usage.OutputTokensDetails.ImageTokens == 0 &&
            env.Usage.OutputTokens > 0 {
            m.Usage.ImageOutputTokens = env.Usage.OutputTokens
        }
    }
    return m, nil
}
```

Notes:

- `Usage` is pointer-valued in the envelope so we can tell "no usage" from
  "zero usage". Missing `usage` skips extra-usage settlement; it does not go
  through the `no_usage` deduction warning path.
- `cached_tokens` is optional. If OpenAI omits it, it remains zero and the
  cost calculator treats all input tokens as non-cached.
- `output_tokens_details` is optional. If it is absent, or present but both
  detail fields are zero while `output_tokens > 0`, parser treats
  `output_tokens` as image output tokens. This matches image streaming
  completed events, which report final image output as aggregate
  `output_tokens`.
- No top-level `id` or `model` field is parsed. The request row falls
  back to `reqCtx.ActualModel` for the model column.

### Stream: `image_stream.go`

Parses SSE by buffering `data:` lines per event (blank-line delimited),
unmarshalling each complete event's JSON payload, and watching the `type`
field. Events of interest:

- `image_generation.partial_image` / `image_edit.partial_image` — first
  occurrence sets TTFTMs.
- `image_generation.completed` / `image_edit.completed` — contains final
  `usage` object when available. Triggers `onComplete` callback with
  `usagePresent=true`.

Every upstream byte is tee'd to the output pipe unchanged; the client sees
the exact upstream stream, including error events.

```go
// Sketch — see implementation plan for full listing.
type imageStreamInterceptor struct {
    upstream   io.ReadCloser
    pr         *io.PipeReader
    pw         *io.PipeWriter
    onComplete func(ImageTokenUsage, bool, int64)
    startTime  time.Time
    done       chan struct{}
    fired      atomic.Bool // guards onComplete against double-fire
}

func newImageStreamInterceptor(
    upstream io.ReadCloser,
    startTime time.Time,
    onComplete func(ImageTokenUsage, bool, int64),
) io.ReadCloser { /* ... */ }
```

Buffer cap per event: 10 MiB (covers a 1024×1024 high-quality base64 image
with headroom).

**Failure handling:**

- Client disconnect before `completed` → pump exits on Pipe write error →
  `onComplete` is invoked once, with zero `ImageTokenUsage` and
  `usagePresent=false`, via a deferred fallback guarded by `fired`.
- Upstream error SSE frame → passed through to client, not parsed for
  usage (type doesn't match). `onComplete` fires with `usagePresent=false`
  if no `completed` event with usage is seen.
- Event size > 10 MiB → scanner errors, pump exits,
  `usagePresent=false`. Metric `image_stream_event_too_large_total`
  increments (new).

**TTFT** is measured from `startTime` (passed in by executor = time of
first upstream request send) to the first `partial_image` event.

## Billing

### Structs

New type in `internal/types/policy.go` (parallel to `CreditRate`):

```go
type ImageCreditRate struct {
    TextInputRate        float64 `json:"text_input_rate"`
    TextCachedInputRate  float64 `json:"text_cached_input_rate"`
    TextOutputRate       float64 `json:"text_output_rate"`
    ImageInputRate       float64 `json:"image_input_rate"`
    ImageCachedInputRate float64 `json:"image_cached_input_rate"`
    ImageOutputRate      float64 `json:"image_output_rate"`
}
```

New field on `Model` (`internal/types/model.go`):

```go
type Model struct {
    // ...existing...
    DefaultCreditRate      *CreditRate      `json:"default_credit_rate,omitempty"`
    DefaultImageCreditRate *ImageCreditRate `json:"default_image_credit_rate,omitempty"`
}
```

The two fields are conceptually mutually exclusive per-model but not
enforced: operators configure whichever applies. `gpt-image-2` has only
`DefaultImageCreditRate` set.

### Migration 029 — add model column

```sql
-- internal/store/migrations/029_models_image_credit_rate.sql
BEGIN;
ALTER TABLE models ADD COLUMN default_image_credit_rate JSONB;
COMMIT;
```

No backfill. Existing models see `NULL`.

### Cost calculation

Formula with proportional cached-token split:

```
total_input   = text_tokens + image_tokens
cached_text   = floor(cached_tokens × text_tokens / total_input)   when total_input > 0
cached_image  = cached_tokens - cached_text
billed_text   = max(0, text_tokens - cached_text)
billed_image  = max(0, image_tokens - cached_image)

credits =
    TextInputRate        × billed_text
  + ImageInputRate       × billed_image
  + TextCachedInputRate  × cached_text
  + ImageCachedInputRate × cached_image
  + TextOutputRate       × output_text_tokens
  + ImageOutputRate      × output_image_tokens

cost_fen = max(1, ceil(credits × CreditPriceFen / 1_000_000))   when credits > 0
         = 0                                                    when credits == 0
```

Proportional split is preferred over "all cached ↦ text" because
`/images/edits` cache hits on photo inputs would otherwise underbill
(image cached at $2/M vs. text cached at $1.25/M). The formula matches
`/images/generations` exactly (where `image_tokens = 0` makes the split
degenerate).

`cached_tokens` is optional in the Images response usage object. When absent,
it is treated as zero. The six rates are the operator-configured
`gpt-image-2` billing schedule; they are not inferred from the generate/edit
method reference payloads.

The 1-fen floor and `CreditPriceFen / 1_000_000` match the existing
`computeExtraUsageCostFen` contract.

New file `internal/proxy/image_extra_usage_cost.go`:

```go
func computeImageExtraUsageCostFen(m *types.Model, u ImageTokenUsage, creditPriceFen int64) (int64, float64, error) {
    if m == nil || m.DefaultImageCreditRate == nil {
        return 0, 0, ErrMissingDefaultCreditRate
    }
    if creditPriceFen <= 0 {
        return 0, 0, fmt.Errorf("extra usage: credit_price_fen must be > 0")
    }
    r := m.DefaultImageCreditRate

    totalInput := u.TextInputTokens + u.ImageInputTokens
    var cachedText, cachedImage int64
    if totalInput > 0 && u.CachedInputTokens > 0 {
        cachedText = u.CachedInputTokens * u.TextInputTokens / totalInput
        cachedImage = u.CachedInputTokens - cachedText
    }
    billedText := u.TextInputTokens - cachedText
    if billedText < 0 { billedText = 0 }
    billedImage := u.ImageInputTokens - cachedImage
    if billedImage < 0 { billedImage = 0 }

    credits := r.TextInputRate*float64(billedText) +
        r.ImageInputRate*float64(billedImage) +
        r.TextCachedInputRate*float64(cachedText) +
        r.ImageCachedInputRate*float64(cachedImage) +
        r.TextOutputRate*float64(u.TextOutputTokens) +
        r.ImageOutputRate*float64(u.ImageOutputTokens)

    if credits <= 0 {
        return 0, credits, nil
    }
    cost := int64(math.Ceil(credits * float64(creditPriceFen) / 1_000_000))
    if cost < 1 { cost = 1 }
    return cost, credits, nil
}
```

Sentinel `ErrMissingDefaultCreditRate` is reused — callers only need to
distinguish "unbillable, no charge" from "error". The warn-log key changes
to `extra_usage_missing_default_image_rate` to preserve observability.

### Settle path

New `Executor.settleImageExtraUsage` — structurally identical to
`settleExtraUsage`:

```go
func (e *Executor) settleImageExtraUsage(ctx context.Context, rc *RequestContext, usage ImageTokenUsage) {
    if !rc.HasExtraUsageCtx {
        return
    }
    euCtx := rc.ExtraUsageCtx
    rc.IsExtraUsage = true
    rc.ExtraUsageReason = euCtx.Reason

    if usage.InputTokens+usage.OutputTokens == 0 {
        e.logger.Warn("extra_usage_settle_no_usage",
            "project_id", rc.ProjectID, "request_id", rc.RequestID)
        metrics.IncExtraUsageDeduction("no_usage")
        return
    }

    costFen, credits, err := computeImageExtraUsageCostFen(rc.ModelRef, usage, e.extraUsageCfg.CreditPriceFen)
    if err != nil {
        if errors.Is(err, ErrMissingDefaultCreditRate) {
            e.logger.Error("extra_usage_missing_default_image_rate",
                "project_id", rc.ProjectID, "model", rc.Model)
            metrics.IncExtraUsageMissingRate(rc.Model)
            return
        }
        e.logger.Error("compute_image_extra_usage_cost_failed", "error", err)
        return
    }
    rc.ExtraUsageCostFen = costFen

    newBal, err := e.store.DeductExtraUsage(store.DeductExtraUsageReq{
        ProjectID:   rc.ProjectID,
        AmountFen:   costFen,
        RequestID:   rc.RequestID,
        Reason:      euCtx.Reason,
        Description: fmt.Sprintf("%s | credits=%.2f | model=%s | kind=%s",
            euCtx.Reason, credits, rc.Model, rc.RequestKind),
    })
    // ...ok / ErrInsufficientBalance / ErrMonthlyLimitReached /
    //    ErrExtraUsageNotEnabled switch — identical to settleExtraUsage...
}
```

`DeductExtraUsage` makes no assumption about token shape; reusing it
yields monthly-limit enforcement, underdraft detection, and metrics for
free. The only new thing is the `cost_fen` input.

### Token persistence

The `requests` table keeps its four token columns. Images usage maps:

```go
pendingReq.InputTokens         = u.InputTokens
pendingReq.OutputTokens        = u.OutputTokens
pendingReq.CacheCreationTokens = 0
pendingReq.CacheReadTokens     = u.CachedInputTokens
pendingReq.Metadata["image_text_input_tokens"]   = strconv.FormatInt(u.TextInputTokens, 10)
pendingReq.Metadata["image_image_input_tokens"]  = strconv.FormatInt(u.ImageInputTokens, 10)
pendingReq.Metadata["image_text_output_tokens"]  = strconv.FormatInt(u.TextOutputTokens, 10)
pendingReq.Metadata["image_image_output_tokens"] = strconv.FormatInt(u.ImageOutputTokens, 10)
```

Dashboard totals stay consistent with existing queries. Detail is
available via `GET /admin/requests/{id}`.

### ExtraUsageGuardMiddleware

No change. The guard operates on project balance/monthly-limit state, not
on token-class detail. Images requests that hit a balance-depleted
project are refused by the existing guard just like chat requests.

## Admin API

`POST /admin/models` / `PUT /admin/models/{name}` bodies accept a new
optional field `default_image_credit_rate: ImageCreditRate`. The store
layer persists it to the new JSONB column. `GET /admin/models` and
`GET /admin/models/{name}` return the field when set.

No structural validation between `default_credit_rate` and
`default_image_credit_rate` is enforced. An advisory `lint_warnings`
field in the GET response flags configurations where both or neither are
set; deferred to a follow-up.

`POST /admin/routing/routes` with `request_kinds` containing the new
constants is accepted by the existing validator (relies on
`types.IsValidRequestKind`).

`GET /admin/routing/request-kinds` — no change; returns
`AllRequestKinds`, which now includes the two new constants.

## Dashboard

**Required for this iteration:**

- Model edit page: expose a new "Image billing" section with the six
  `ImageCreditRate` fields. Collapsed by default; opened when the model
  row has a non-null `default_image_credit_rate` or when the operator
  clicks "Enable image billing".

**Automatic (no work):**

- Routes list & edit: the request-kinds dropdown is populated from
  `GET /admin/routing/request-kinds`, so the two new options appear
  without frontend changes.

**Deferred:**

- Request detail page: per-class token breakdown (text vs image, input vs
  cached vs output). The metadata is persisted but not rendered.

## Testing

### Unit

`internal/types/request_kind_test.go`:

- Table test confirms both new constants satisfy `IsValidRequestKind`.

`internal/proxy/resolve_model_middleware_test.go`:

- Multipart body with `model` field → extracted.
- Multipart without `model` field → empty string.
- Malformed boundary → empty string, no panic.
- Body > multipart limit → empty string, no panic.
- JSON body continues to work unchanged (regression).

`internal/proxy/multipart_util_test.go` (new):

- `peekMultipartFields`: extracts model + stream=true/false; tolerates
  file parts without reading them; reports error on truncated body.
- `rewriteMultipartField`: rewrites model field, preserves all other parts
  byte-for-byte and in original order. Idempotent when new value equals
  old.

`internal/proxy/image_parser_test.go`:

- Full usage payload → all six classes populated.
- Missing `usage` → `UsagePresent=false`, zero-value metrics, no error, no
  extra-usage settle.
- Partial details (only `output_tokens_details`, missing
  `input_tokens_details`) → partial fields populated.
- Missing `output_tokens_details` with `output_tokens > 0` → `ImageOutputTokens`
  falls back to aggregate `output_tokens`.
- Present-but-zero `output_tokens_details` with `output_tokens > 0` →
  `ImageOutputTokens` falls back to aggregate `output_tokens`.
- Malformed JSON → error returned.

`internal/proxy/image_stream_test.go`:

- Golden stream: 2× partial + 1× completed → client receives byte-for-byte
  upstream stream; TTFT matches first partial arrival; onComplete fires
  once with `usagePresent=true` and completed-event usage.
- Completed stream usage without `output_tokens_details` → output token count
  is billed as image output.
- Truncated stream (partial with no completed) → onComplete fires once
  with `usagePresent=false`; no panic; no double-fire.
- Upstream error SSE mid-stream → bytes passed through; onComplete fires
  with `usagePresent=false`.
- `image_edit.*` event variants → parsed identically to
  `image_generation.*`.
- Oversize event (> 10 MiB) → scanner terminates, onComplete fires
  `usagePresent=false`, metric increments.

`internal/proxy/image_extra_usage_cost_test.go`:

- Only `image_output` populated (generations happy path) → cost matches
  rate × output.
- All six classes populated with non-zero cached → proportional split
  applies; billed_text/billed_image subtract correctly.
- `cached_tokens` > `text_tokens + image_tokens` (defensive) → billed
  terms clamp to zero; cost still non-negative.
- `DefaultImageCreditRate == nil` → `ErrMissingDefaultCreditRate`.
- `CreditPriceFen <= 0` → configuration error.
- Zero usage everywhere → zero cost, zero credits, nil error.
- Non-zero credits under 1 fen → ceils to 1 fen (floor).

### Integration

`internal/proxy/handler_images_test.go` (new):

- `TestHandleImagesGenerations_JSON_HappyPath` — fake OpenAI upstream
  returns response with usage; verify request row written, settle fires
  (when project has extra_usage enabled), cost_fen matches
  `computeImageExtraUsageCostFen`.
- `TestHandleImagesGenerations_StreamSSE` — fake upstream emits SSE;
  verify client receives identical bytes; TTFT recorded; cost settled on
  completed event.
- `TestHandleImagesEdits_Multipart_HappyPath` — real multipart request
  with 2 images + prompt + model + stream=false; verify full pipeline.
- `TestHandleImagesEdits_JSON_HappyPath` — JSON request with
  `images: [{file_id: ...}]` or `images: [{image_url: ...}]`; verify shared
  JSON path, response parsing, request row, and extra-usage settle.
- `TestHandleImagesEdits_JSON_ModelMapRewrite` — JSON edit body uses
  upstream `ModelMap` rewrite like other JSON OpenAI endpoints.
- `TestHandleImagesEdits_Multipart_CanonicalModelRewrite` — client sends
  alias; verify handler rewrites multipart model field; upstream request
  has canonical name.
- `TestHandleImagesEdits_Multipart_BodyTooLarge` — body exceeds
  `images.max_body_size` → 413.
- `TestHandleImagesEdits_Multipart_ExecutorUsesImageBodyLimit` — body larger
  than global `max_body_size` but smaller than `images.max_body_size` reaches
  upstream successfully.
- `TestHandleImagesEdits_Multipart_NoRouteMatch` — known model but no
  images_edits route → 404 with the kind-named error message.
- `TestHandleImagesGenerations_UnknownModel` → 400 via existing
  `writeUnsupportedModelError`.
- `TestHandleImagesEdits_StreamTrueInMultipart` — verify `stream` form
  field flips `reqCtx.IsStream` and triggers stream path.
- `TestImagesEdits_RetryAcrossUpstreams` — first upstream 502, second
  200; verify multipart body is re-sent byte-identical (no second rewrite).

`internal/proxy/router_engine_test.go` (extend):

- `TestMatch_ImagesKindsRoutedSeparately` — same model, two routes,
  kinds `['openai_images_generations']` vs `['openai_images_edits']`,
  verify correct group selection per endpoint.

`internal/admin/handle_models_test.go` (extend):

- POST model with `default_image_credit_rate` → persisted.
- PUT updating only the image rate → persisted.
- PUT setting image rate to null → cleared.
- GET returns the field.

### Migration tests

- Migration 028 against a fixture DB with existing routes → CHECK
  constraint permits new kinds; rejects unknown kinds.
- Migration 029 against a fixture DB → column added, existing rows have
  NULL, inserts with populated JSON work.
- Store rollback-compat regression: a route row containing an unknown
  `request_kind` can be scanned/listed without crashing the server.

### Manual smoke after deploy

1. Create `gpt-image-2` model with `DefaultImageCreditRate` per §Billing
   operator worksheet.
2. Create a route: model=`gpt-image-2`, upstream_group=an existing OpenAI
   group, request_kinds=`['openai_images_generations','openai_images_edits']`.
3. `curl` generations (non-stream) → 200, b64 image returned, request
   row shows populated token columns and `cost_fen > 0`.
4. `curl` generations (stream=true) → SSE frames, client receives
   partials + completed; request row has TTFTMs and settled cost.
5. `curl` edits JSON with an `images` `file_id`/`image_url` reference → 200,
   edited image; request row populated.
6. `curl` edits multipart with a PNG + mask → 200, edited image; request row
   populated.
7. Run against an extra_usage project with depleted balance → guard
   middleware rejects before upstream.
8. Request `model=gpt-image-2` without a matching route → 404 with
   endpoint-named error.

## Rollout

Single atomic PR containing:

1. Migration 028 (routes CHECK constraint update for new kinds).
2. Migration 029 (models table adds `default_image_credit_rate` column).
3. Rollback-compat regression test proving unknown request kinds can be
   scanned without crashing.
4. All server code: types, config, middleware, handlers, executor
   branches, parsers, billing.
5. Dashboard: model edit image-billing section.

Deploy order (runs in one deployment unit):

1. CI runs migration tests and full Go test suite.
2. Deploy: migrations run in-transaction before server starts; server
   starts with new code; dashboard deploys in the same window.
3. Operators create the `gpt-image-2` model entry and the images route.
4. Clients can hit the new endpoints.

### Rollback

If a regression surfaces, roll back the server. The schema changes are
backward compatible:

- Migration 028 simply permits two additional kinds. Old code scans
  `request_kinds` as strings and does not validate them during route load, so
  image-only routes do not crash startup and do not match old endpoint kinds.
- Migration 029 adds a nullable column that old code does not read.

Rollback restores pre-images behavior. Image traffic returns 404. No data
loss.

## Risks

- **Multipart parsing cost at ingress**. 16-image requests parse the body
  twice (middleware `extractModelFromRequest`, handler
  `peekMultipartFields`). Monitor p99 latency after rollout; if elevated,
  cache parsed parts on the request context in the middleware and have
  the handler consume them.
- **SSE event-buffer growth**. Cap enforced at 10 MiB/event. Monitor heap
  and the new `image_stream_event_too_large_total` counter.
- **Approximation of `cached_tokens` split**. OpenAI reports cached
  tokens as a single aggregate field; we split it between text and image
  proportionally. The per-cached-token rate delta is $0.75/1M
  (text-cached $1.25 vs image-cached $2.00), applied only to the cached
  portion. Expected billing error per request is <0.5% of total cost.
- **Executor `if` branches on `RequestKind` / multipart content type**.
  `executor.go` gains small branches for image body limit, multipart
  transform skip, outgoing Content-Type, and response parse/stream dispatch.
  Covered by `handler_images_test.go`.
- **Operator oversight on model/rate setup**. Creating a `gpt-image-2`
  model without `DefaultImageCreditRate` logs
  `extra_usage_missing_default_image_rate` and charges zero. Runbook addition:
  the rollout checklist includes "verify model has the image rate configured".

## Open questions / future work

- Per-endpoint rate limit. Today rate limits are keyed on `(project,
  api_key, user, model)`. Images draws against the same bucket as chat
  for the same model name (n/a since `gpt-image-2` is images-only). If
  operators want separate buckets for `images` vs other kinds,
  `RequestKind` becomes another bucket dimension — deferred.
- Per-class token columns on `requests`. If dashboards commonly surface
  image-output ratios, promoting `image_*_tokens` out of metadata to
  typed columns is worth it. Revisit after one month of production data.
- Additional image models. v1 is intentionally `gpt-image-2` only; adding
  any other image model should be a separate design with its own usage and
  billing verification.
