# Route Request-Kind Dimension

**Status:** Approved (design)
**Date:** 2026-04-22
**Owner:** mryao

## Problem

`Router.Match` keys routes on `(project_id, model)`. Today the per-endpoint
distinction is enforced after-the-fact by the `AllowedProviders` field on
`RequestContext`: each handler hardcodes which providers may serve its
endpoint, and `Executor.Execute` filters the load-balanced candidate list to
those providers.

Two consequences are now uncomfortable:

1. The same model name (e.g. `claude-sonnet-4-6`) on the same project is
   forced to share one upstream group across `/v1/messages` and
   `/v1/messages/count_tokens`. Operators cannot, for example, send
   count_tokens to a free console key while sending real inference to a paid
   tier. `HandleCountTokens` works around this by re-implementing routing
   inside the handler — duplicating Match/SelectWithRetry, hard-coding a
   provider whitelist, skipping retries, the request log, and circuit
   integration.
2. Anthropic and Google both expose two wire formats: their native API
   (`/v1/messages`, `/v1beta/.../generateContent`) and an OpenAI-compatible
   surface (`/v1/chat/completions`, `/v1/responses`). A single model on a
   single provider may need to land on a *different* upstream depending on
   which wire format the client used. `AllowedProviders` cannot express
   that — the provider is the same.

## Decision

Add a third dimension to routes: **request kind** — the wire-level endpoint
the client called. Routes declare which kinds they serve; `Router.Match`
becomes `(project_id, model, request_kind)`; the per-handler
`AllowedProviders` filter is removed.

## Request kinds (this iteration)

| Constant | Trigger | Handler |
| --- | --- | --- |
| `anthropic_messages` | `POST /v1/messages` (stream and non-stream) | `HandleMessages` |
| `anthropic_count_tokens` | `POST /v1/messages/count_tokens` | `HandleCountTokens` |
| `openai_responses` | `POST /v1/responses` | `HandleResponses` |
| `openai_chat_completions` | `POST /v1/chat/completions` | `HandleChatCompletions` |
| `google_generate_content` | `POST /v1beta/models/*:generateContent` and `:streamGenerateContent` | `HandleGemini` |

Stream and non-stream variants of the same endpoint share one kind — they
typically belong on the same upstream group.

Classification is by **wire format**, not provider. Vertex's OpenAI-compatible
endpoint is `openai_chat_completions`. Bedrock and VertexAnthropic serve
`anthropic_messages` (and could in principle also serve other kinds, but
neither natively supports count_tokens — sending traffic there will surface
as an upstream 4xx, not a routing-layer concern).

## Schema

New column on `routes`: `request_kinds TEXT[] NOT NULL`. Multi-valued, mirroring
`model_names`. A single route can serve multiple kinds (e.g. one route mapping
`['anthropic_messages', 'anthropic_count_tokens']` to one group when no
differentiation is wanted).

```sql
-- migrations/019_route_request_kinds.sql
BEGIN;

ALTER TABLE routes
  ADD COLUMN request_kinds TEXT[] NOT NULL DEFAULT '{}';

UPDATE routes
SET request_kinds = ARRAY['anthropic_messages']
WHERE request_kinds = '{}';

ALTER TABLE routes ADD CONSTRAINT routes_request_kinds_valid CHECK (
  request_kinds <@ ARRAY[
    'anthropic_messages',
    'anthropic_count_tokens',
    'openai_chat_completions',
    'openai_responses',
    'google_generate_content'
  ]::TEXT[]
  AND array_length(request_kinds, 1) >= 1
);

CREATE INDEX idx_routes_request_kinds ON routes USING GIN (request_kinds);

-- Audit notice: surface routes whose upstream group contains non-Anthropic
-- providers — these will need their request_kinds corrected by hand
-- post-migration. Output is informational; the migration does not modify them.
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT r.id, r.model_names, array_agg(DISTINCT u.provider) AS providers
        FROM routes r
        JOIN upstream_group_members m ON m.upstream_group_id = r.upstream_group_id
        JOIN upstreams u ON u.id = m.upstream_id
        WHERE r.request_kinds = ARRAY['anthropic_messages']
        GROUP BY r.id, r.model_names
        HAVING bool_or(u.provider NOT IN ('anthropic', 'bedrock', 'claudecode', 'vertex_anthropic'))
    LOOP
        RAISE NOTICE 'Route % (models=%) has non-Anthropic providers % — verify request_kinds',
            r.id, r.model_names, r.providers;
    END LOOP;
END $$;

COMMIT;
```

### Backfill rationale

All existing rows are defaulted to `['anthropic_messages']`. This is correct for
the dominant case (the bulk of production routes serve `/v1/messages`) but will
mis-tag any existing OpenAI/Gemini routes. The migration emits NOTICE lines for
each suspect route so operators can run targeted UPDATEs after deploy. The
migration intentionally does not auto-detect kind from upstream provider — that
heuristic is wrong often enough (e.g. an OpenAI provider can serve either
`openai_chat_completions` or `openai_responses`) that operators should confirm.

## Code changes

### `internal/types/`

New file `internal/types/request_kind.go`:

```go
package types

const (
    KindAnthropicMessages     = "anthropic_messages"
    KindAnthropicCountTokens  = "anthropic_count_tokens"
    KindOpenAIChatCompletions = "openai_chat_completions"
    KindOpenAIResponses       = "openai_responses"
    KindGoogleGenerateContent = "google_generate_content"
)

var AllRequestKinds = []string{
    KindAnthropicMessages,
    KindAnthropicCountTokens,
    KindOpenAIChatCompletions,
    KindOpenAIResponses,
    KindGoogleGenerateContent,
}

func IsValidRequestKind(s string) bool {
    for _, k := range AllRequestKinds {
        if k == s { return true }
    }
    return false
}
```

`Route` struct gains:

```go
RequestKinds []string `json:"request_kinds"`
```

### `internal/store/routes.go`

`scanRoute`, `CreateRoute`, `UpdateRoute`, `ListRoutes*` updated to
read/write `request_kinds`. `UpdateRoute` accepts `request_kinds` in the
updates map. `Router.Reload` reads routes through these store methods, so
no additional reload-path change is needed.

### `internal/proxy/router_engine.go`

`Router.Match` signature:

```go
func (r *Router) Match(projectID, model, kind string) (*resolvedGroup, error)
```

Both project-specific and global passes add a kind check:

```go
if route.ProjectID == projectID &&
   slices.Contains(route.ModelNames, model) &&
   slices.Contains(route.RequestKinds, kind) {
    if g, ok := r.groups[route.UpstreamGroupID]; ok {
        return g, nil
    }
}
```

Error string changes to: `no route configured for model <m> on endpoint <kind>`.

### `internal/proxy/executor.go`

- `RequestContext.AllowedProviders` field: **removed**.
- `RequestContext.RequestKind string` field: **added**.
- `Executor.Execute` calls `e.router.Match(reqCtx.ProjectID, reqCtx.Model, reqCtx.RequestKind)`.
- The `AllowedProviders` filtering block (current lines 192-207) is **removed**.
  The routing table is now the single source of truth. Misconfiguration
  (e.g. an OpenAI upstream wired into an `anthropic_messages` group) becomes a
  visible upstream error rather than being silently masked.

### `internal/proxy/handler.go`

- Each handler sets `reqCtx.RequestKind` to the appropriate constant; the
  hardcoded `[]string{...}` provider lists are removed.
- `ingressForProviders` (lines ~464-475) is deleted; each handler passes the
  ingress string to `resolveModel` directly (it already does).
- `HandleCountTokens` is rewritten to delegate to `executor.Execute`, matching
  the structure of `handleProxyRequest`. It loses its self-managed
  `httputil.ReverseProxy`, the inline `Match`/`SelectWithRetry`, and the
  hard-coded provider filter. Specifically:
  - Reads body, resolves model, enforces `AllowedModels` (unchanged).
  - Inserts a pending request row with `Streaming: false`.
  - Builds `RequestContext` with `RequestKind: KindAnthropicCountTokens`,
    `IsStream: false`, and the same auth/policy/trace plumbing as
    `handleProxyRequest`.
  - Calls `h.executor.Execute(w, r, reqCtx)`.

This means count_tokens automatically gains: retry, circuit breaker,
session-affinity (irrelevant in practice but harmless), HTTP logging,
request-table observability — at the cost of one extra DB insert per request.

### Response parsing

`Executor.handleNonStream` calls `transformer.ParseResponse(body)` on every
2xx response. The Anthropic transformer expects a `Message` shape with a
`usage` field; count_tokens responses are `{"input_tokens": N}` and will
trigger a `failed to parse response` warning per request — log noise, but
not behavioral. Fix: in the executor, skip `ParseResponse` (and the
extra-usage settle path, which is irrelevant to count_tokens) when
`reqCtx.RequestKind == types.KindAnthropicCountTokens`. The request row is
then finalized with `Status = success` and zero token counts. No collector
change required.

### `internal/admin/handle_routing_routes.go`

`POST` and `PUT` body schemas gain `request_kinds: []string`:

- `POST /admin/routing/routes`: `request_kinds` is required and non-empty;
  every value must satisfy `types.IsValidRequestKind`. Validation failures
  return 400 with an explanatory message.
- `PUT /admin/routing/routes/{id}`: `request_kinds` is in the allowlist of
  updatable fields; same validation.
- `GET /admin/routing/routes` returns the field unchanged from storage.

New endpoint `GET /admin/routing/request-kinds`:

```json
{ "data": ["anthropic_messages", "anthropic_count_tokens",
           "openai_chat_completions", "openai_responses",
           "google_generate_content"] }
```

Lets the dashboard render a dropdown without compiling the enum into the
frontend bundle.

### Dashboard

- Routes list page: add an "Endpoint(s)" column rendering `request_kinds`
  as comma-joined chips.
- Route create/edit modal: add a multi-select "Request kinds" dropdown,
  options sourced from `GET /admin/routing/request-kinds`. Default to
  `[anthropic_messages]` for new rows.
- Soft warning (non-blocking): if the chosen upstream group's members all
  have providers that don't typically serve the chosen kinds, show a
  yellow inline note. (e.g. selecting only `anthropic_messages` with a
  group of OpenAI upstreams.) Operators can override.

## Behavior changes visible to users

1. **Strict matching, no fallback.** A request whose `(project, model, kind)`
   doesn't match any active route returns 404 with
   `no route configured for model <m> on endpoint <kind>`. Previously, the
   route would be found by `(project, model)` and only the provider filter
   would block it (returning 503 `no upstreams available`).
2. **count_tokens now writes to the request log** with zero token usage. This
   shows up in the dashboard's request list under the `anthropic` ingress
   filter. If this is undesirable for cost-reporting reasons, gate it behind a
   metadata field (`metadata["endpoint"] = "count_tokens"`) so the dashboard
   can hide them by default.
3. **count_tokens now retries on upstream failure.** Previously a single
   upstream error returned 502. Now, if the matched group has
   `RetryPolicy.MaxRetries > 0`, count_tokens benefits from the same retry
   behavior as `/v1/messages`. This is intended.

## Out of scope

- Activating the existing `routes.conditions` JSONB column. It remains a
  declared-but-unused field.
- Bedrock / Vertex count_tokens upstream support. These providers don't
  expose count_tokens natively; if operators route count_tokens to them they
  will see upstream 4xx, by design.
- Per-endpoint metrics on the routing-health dashboard. Worth a follow-up
  once data accumulates.
- A `request_kinds` filter on the `GET /admin/routing/routes` query
  (paginated list). Add only if the routes table grows large enough to need
  it.

## Testing

### Unit / integration

`internal/proxy/router_engine_test.go`:

- `TestMatch_KindIsRequired_NoMatchingKindReturnsError` — kind not in any
  active route's `RequestKinds` → error.
- `TestMatch_MultiKindRouteServesBothEndpoints` — one route with
  `['anthropic_messages','anthropic_count_tokens']` is selected by both kinds.
- `TestMatch_KindMismatchSkipsRoute_FallsThroughToGlobal` — project route
  exists but for a different kind; falls through to a global route that
  matches the kind.
- `TestMatch_ProjectKindBeatsGlobalKind` — project + kind match wins over
  global + kind match at lower priority.

`internal/proxy/handler_count_tokens_test.go` (or extend existing):

- `/v1/messages/count_tokens` with no count_tokens route configured → 404
  with the new error message; does not silently use an `anthropic_messages`
  route.
- Same model with two routes — `anthropic_messages` → group A,
  `anthropic_count_tokens` → group B — confirm each endpoint hits its
  intended group via fake upstream servers.
- `/v1/chat/completions` and `/v1/responses` against the same model with
  two routes pointing at distinct groups, confirm endpoint isolation.
- count_tokens upstream 502 with `RetryPolicy.MaxRetries=1` → second
  candidate is tried (regression-guards the executor delegation).
- count_tokens success writes a request row with zero token usage.

`internal/admin/handle_routing_routes_test.go`:

- `POST` without `request_kinds` → 400.
- `POST` with invalid kind string → 400 listing the offender.
- `PUT` updating only `request_kinds` works.
- `GET /admin/routing/request-kinds` returns all five constants.

Migration test (run 003 → 019 against a fixture DB):

- All preexisting rows end up with `request_kinds = ['anthropic_messages']`.
- CHECK constraint rejects an attempted update setting an unknown kind.

### Manual smoke

After deploy:

1. Existing `/v1/messages` traffic continues to flow (default-tagged routes
   still match).
2. Configure a new `anthropic_count_tokens` route pointing at a distinct
   upstream group; confirm `/v1/messages/count_tokens` lands there and
   `/v1/messages` still lands on the old group.
3. Hit `/v1/responses` with a model that has only an `anthropic_messages`
   route configured → 404 (previously 503). Confirm the error message names
   the endpoint.

## Rollout

1. Merge migration 019. The `DEFAULT '{}'` plus immediate `UPDATE` means
   the column is populated synchronously in the same transaction; old code
   reading the table without the column is unaffected.
2. Deploy new server code (Match signature change, AllowedProviders removal,
   HandleCountTokens delegation).
3. Operators review NOTICE output from migration 019 and run targeted
   `UPDATE routes SET request_kinds = ARRAY[<correct-kind>] WHERE id = ...`
   for OpenAI/Gemini-served routes that were defaulted to
   `anthropic_messages`.
4. Operators add new differentiated routes where desired (e.g. count_tokens
   to a free console key; OpenAI-compat to a dedicated proxy).

## Risks

- **Mis-tagged routes during step 1 → 3 window.** If the deploy happens
  before operators correct OpenAI/Gemini routes' `request_kinds`, traffic to
  those endpoints will 404 (no `openai_chat_completions` route matches).
  Mitigation: stage migration + correction UPDATEs together; for sites
  with only Anthropic upstreams, no correction needed.
- **Removing `AllowedProviders` removes a defense-in-depth.** A route
  pointing at a mismatched-provider upstream group will now send the
  request and the upstream will reject it. This is intentional (visibility
  > silent masking) but operators should be told.
- **count_tokens request-log volume.** count_tokens is typically invoked
  more frequently than `/v1/messages` (clients use it for live token-count
  estimation in the editor). Inserting a request row per call may double
  request-table write volume in some deployments. If this becomes a
  problem, add a `log_requests=false` toggle on the route; out of scope for
  this iteration.
