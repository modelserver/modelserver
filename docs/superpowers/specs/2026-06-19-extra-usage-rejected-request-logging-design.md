# Persist UA and Request Info on Pre-Handler 4xx Rejections

Date: 2026-06-19

## Background

Today, when the middleware chain rejects a request before it ever reaches a
handler, the row inserted into `requests` is sparse:

- `RateLimitMiddleware` (`internal/proxy/ratelimit_middleware.go:103`) fires
  `logRateLimitRejectionMsg`, which writes only `project_id`, `api_key_id`,
  `created_by`, `trace_id`, `model`, `status`, `client_ip`, `error_message`.
- `ExtraUsageGuardMiddleware` (`internal/proxy/extra_usage_guard_middleware.go:329`)
  fires `emitGuardRejection`, which writes the same set plus
  `extra_usage_reason`.

Neither fills `metadata` (so `User-Agent` is lost), nor `request_kind`,
`streaming`, or `oauth_grant_id`. The successful path in `handler.go`
populates all of these. Operators investigating rejection patterns in the
dashboard cannot see which client sent the rejected request, what surface
they hit, or which OAuth grant they used.

A representative slog line that motivated this work:

    extra_usage_rejected reason=client_restriction sub_reason=not_enabled
    model=claude-opus-4-7 client_kind=unknown user_agent=Python-urllib/3.14
    user_id_shape=absent

The slog line already carries diagnostic detail; the gap is in the persisted
row that drives dashboards and analytics.

## Goals

Make `RateLimitMiddleware` and `ExtraUsageGuardMiddleware` write a
`requests` row whose shape matches the success path's `CreateRequest` row:

- `metadata.user_agent` populated from the request header.
- `request_kind` populated from a path → kind mapping.
- `oauth_grant_id` populated from the auth context.
- `streaming` populated from the request body / path.
- `provider` populated from the catalog model when available.
- All existing fields (`status`, `client_ip`, `model`, `trace_id`,
  `error_message`, `extra_usage_reason`) preserved.

## Non-Goals

- No schema change. `requests.metadata` is JSONB; new fields live inside it.
- No changes to auth / model-allowed / earlier rejection points.
- No changes to the successful path in `handler.go`.
- No new diagnostic fields (`client_kind`, `user_id_shape`, `sub_reason`).
  Those already appear in the slog WARN line and are sufficient for triage.
- No DB-level fake or end-to-end test for `RateLimitMiddleware`; its DB
  insert is now a thin wrapper around the shared helper.

## Design

### New file: `internal/proxy/rejected_request.go`

Two exported package-private helpers, used by both rejection paths.

```go
// buildRejectedRequestRow assembles a *types.Request suitable for fire-
// and-forget persistence on 4xx pre-handler rejections. It captures
// everything that's knowable from the *http.Request + context at the
// rejection point — UA, kind, streaming, oauth grant, trace id, client ip,
// model, provider — so the row matches the shape of successful rows except
// for the rejection-specific status / error_message / extra_usage_reason
// fields.
//
// Returns nil when Project or APIKey are absent from context (the same
// guard the existing rejection writers used to skip persistence on 5xx
// infra paths).
func buildRejectedRequestRow(
    r *http.Request,
    status string,
    errMsg string,
    extraUsageReason string,
) *types.Request
```

Internal behaviour:

- Pulls `Project`, `APIKey`, `Model` (catalog), `OAuthGrantID`, `TraceID`
  from `r.Context()`. Returns `nil` if Project or APIKey is missing.
- `Metadata`: `map[string]string{}`. If `User-Agent` header is non-empty,
  sets `metadata["user_agent"] = ua`. Matches `handler.go:204`.
- `Model`: prefers `ModelFromContext(r).Name`; falls back to `peekModel(r)`
  (existing helper at `ratelimit_middleware.go:121`).
- `RequestKind`: calls `requestKindFromRequest(r)` (below).
- `Provider`: from `ModelFromContext(r).Publisher`, else `""`.
- `Streaming`: calls `peekStreaming(r)` (below).
- `ClientIP`: `r.RemoteAddr`.
- `CreatedBy`: `apiKey.CreatedBy`.
- `Status`, `ErrorMessage`, `ExtraUsageReason`: passed-through args.

```go
// requestKindFromRequest maps the incoming path + method to a
// types.Kind* constant. Mirrors the per-handler constants in
// handler.go. Returns "" if the path is outside the proxy's
// request surface (admin, health, /v1/models, /v1/usage, etc.).
func requestKindFromRequest(r *http.Request) string
```

Mapping table (covers every POST mounted in `router.go:49-66`):

| Method + Path                          | Kind                          |
|----------------------------------------|-------------------------------|
| `POST /v1/messages`                    | `KindAnthropicMessages`       |
| `POST /v1/messages/count_tokens`       | `KindAnthropicCountTokens`    |
| `POST /v1/responses`                   | `KindOpenAIResponses`         |
| `POST /v1/responses/compact`           | `KindOpenAIResponsesCompact`  |
| `POST /v1/chat/completions`            | `KindOpenAIChatCompletions`   |
| `POST /v1/images/generations`          | `KindOpenAIImagesGenerations` |
| `POST /v1/images/edits`                | `KindOpenAIImagesEdits`       |
| `POST /v1beta/models/*` (any `:generateContent` / `:streamGenerateContent` / `:streamRawPredict` suffix) | `KindGoogleGenerateContent` |
| Anything else                          | `""`                          |

```go
// peekStreaming reads whether the request is streaming without consuming
// the body. For Gemini's /v1beta/models/{model}:streamGenerateContent
// (or :streamRawPredict / similar `:stream*` suffix), returns true based
// on the path. For Anthropic / OpenAI POSTs, parses {"stream": bool} from
// the body using the same read-and-restore pattern as peekModel. Returns
// false for paths that have no streaming variant (count_tokens, images/*)
// or when the body cannot be parsed.
func peekStreaming(r *http.Request) bool
```

### Call site: `extra_usage_guard_middleware.go:emitGuardRejection`

Replace the request-construction tail of the function. The slog block at
the top stays unchanged — it carries diagnostic fields (`client_kind`,
`user_id_shape`, `opencode_header`, etc.) that are deliberately not
persisted to the row.

Before (`extra_usage_guard_middleware.go:381`):

```go
if st == nil || project == nil || apiKey == nil {
    return
}
req := &types.Request{
    ProjectID:        project.ID,
    APIKeyID:         apiKey.ID,
    CreatedBy:        apiKey.CreatedBy,
    TraceID:          traceID,
    Model:            modelName,
    Status:           types.RequestStatusRateLimited,
    ClientIP:         r.RemoteAddr,
    ErrorMessage:     message,
    ExtraUsageReason: reason,
}
go st.CreateRequest(req)
```

After:

```go
if st == nil {
    return
}
req := buildRejectedRequestRow(r, types.RequestStatusRateLimited, message, reason)
if req == nil {
    return
}
go st.CreateRequest(req)
```

The locals consumed by the slog block (`project`, `apiKey`, `model`,
`bodyModel`, `modelName`, `publisher`, `projectID`, `apiKeyID`,
`createdBy`, `traceID`, `kind`, `ua`, `userIDShape`, etc.) are kept; only
the row construction is replaced.

### Call site: `ratelimit_middleware.go:logRateLimitRejectionMsg`

Before (`ratelimit_middleware.go:103`):

```go
func logRateLimitRejectionMsg(st *store.Store, r *http.Request, project *types.Project, apiKey *types.APIKey, msg string) {
    model := peekModel(r)
    traceID := TraceIDFromContext(r.Context())
    req := &types.Request{
        ProjectID:    project.ID,
        APIKeyID:     apiKey.ID,
        CreatedBy:    apiKey.CreatedBy,
        TraceID:      traceID,
        Provider:     "",
        Model:        model,
        Status:       types.RequestStatusRateLimited,
        ClientIP:     r.RemoteAddr,
        ErrorMessage: msg,
    }
    go st.CreateRequest(req)
}
```

After:

```go
func logRateLimitRejectionMsg(st *store.Store, r *http.Request, _ *types.Project, _ *types.APIKey, msg string) {
    req := buildRejectedRequestRow(r, types.RequestStatusRateLimited, msg, "")
    if req == nil {
        return
    }
    go st.CreateRequest(req)
}
```

The `project` / `apiKey` parameters are kept for signature stability (call
sites do not change); the helper reads them from context.

## Data Flow

```
incoming POST /v1/messages
  │  User-Agent: Python-urllib/3.14, body: {"model":"claude-opus-4-7","stream":true,...}
  ▼
Auth → Trace → ResolveModel → SubscriptionEligibility
  │  ctx populated: Project, APIKey, Model, OAuthGrantID, TraceID, Eligibility
  ▼
RateLimitMiddleware
  ├─ ineligible branch, classic-only denied → logRateLimitRejectionMsg
  │     └─ buildRejectedRequestRow → go st.CreateRequest
  └─ pass → withExtraUsageIntent → next
  ▼
ExtraUsageGuardMiddleware
  └─ settings.Enabled == false → emitGuardRejection
        ├─ slog.Warn("extra_usage_rejected", ...)   (unchanged)
        └─ buildRejectedRequestRow → go st.CreateRequest
```

Row comparison (this example: `client_restriction` / `not_enabled`):

| Column              | Before                       | After                                            |
|---------------------|------------------------------|--------------------------------------------------|
| `status`            | `rate_limited`               | `rate_limited`                                   |
| `model`             | `claude-opus-4-7`            | `claude-opus-4-7`                                |
| `request_kind`      | `""`                         | `anthropic_messages`                             |
| `streaming`         | `false`                      | `true`                                           |
| `oauth_grant_id`    | NULL                         | OAuth grant id (when one is attached)            |
| `client_ip`         | `58.240.x.x`                 | `58.240.x.x`                                     |
| `trace_id`          | `""`                         | `""` (absent in this example)                    |
| `error_message`     | `this client cannot use...`  | (same)                                           |
| `extra_usage_reason`| `client_restriction`         | `client_restriction`                             |
| `metadata`          | `{}`                         | `{"user_agent":"Python-urllib/3.14"}`            |

## Error Handling

- Helper returns `nil` when Project or APIKey is missing from context. This
  is the same skip-on-missing-attribution policy `emitGuardRejection`
  already had.
- `peekModel` / `peekStreaming` silently return zero values on bad bodies
  or IO errors. The successful path's metadata population follows the same
  best-effort style.
- DB write stays `go st.CreateRequest(req)` (fire-and-forget). An insert
  failure drops one row; it does not affect the rejection response.

## Testing

New file `internal/proxy/rejected_request_test.go`:

1. `TestBuildRejectedRequestRow_FullContext` — context populated with
   project + api key + catalog model + oauth grant + trace; POST
   `/v1/messages` with `User-Agent: foo/1.0` and body
   `{"model":"claude-opus-4-7","stream":true}`. Asserts every field and
   `metadata["user_agent"] == "foo/1.0"`.
2. `TestBuildRejectedRequestRow_MissingProjectOrAPIKey` — context missing
   either → helper returns `nil`.
3. `TestBuildRejectedRequestRow_NoUserAgent` — header absent →
   `metadata` has no `user_agent` key.
4. `TestBuildRejectedRequestRow_ModelFallsBackToBodyPeek` — no ModelRef
   in context, body has `"model":"x"` → `req.Model == "x"`.
5. `TestRequestKindFromRequest_AllRoutes` — table-driven coverage of
   every row in the mapping table plus `GET /v1/models` → `""` and a
   random `/admin/*` → `""`.
6. `TestPeekStreaming` — `/v1beta/models/foo:streamGenerateContent` true,
   `/v1/messages` with `{"stream":true}` true and without `stream` false,
   `/v1/messages/count_tokens` always false, malformed body → false.

Modify `internal/proxy/extra_usage_guard_middleware_test.go`'s existing
`TestEmitGuardRejection_PersistsRequestRow` (line 305+):

- Set `User-Agent: foo/1.0` on the test request, attach a ModelRef and
  OAuthGrantID via context.
- Add assertions for the new fields:
  `got.Metadata["user_agent"] == "foo/1.0"`,
  `got.RequestKind == types.KindAnthropicMessages`,
  `got.Streaming == true`,
  `got.OAuthGrantID == "<seeded oauth grant>"`,
  `got.Provider == types.PublisherAnthropic`.

No DB integration test for `RateLimitMiddleware`. Its insert is a thin
wrapper around `buildRejectedRequestRow`; the helper's units cover the
shared logic.

## Risks & Rollback

- **Double body peek.** `peekModel` and `peekStreaming` each
  read-and-restore the body. On the rejection path the request is about
  to terminate, so the extra small read is irrelevant. Consolidating
  them into one `peekAnthropicShape(r)` is a follow-up cleanup; out of
  scope here to keep the PR small.
- **Behaviour change.** Rows from `status=rate_limited` rejections now
  have non-empty `request_kind` and `metadata`. Dashboard queries that
  pivot on these will start showing the new distribution — this is the
  intended outcome.
- **Rollback.** Delete `rejected_request.go` and revert the two small
  middleware diffs. No schema change to undo.
