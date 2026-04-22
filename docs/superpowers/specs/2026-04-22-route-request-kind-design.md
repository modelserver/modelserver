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

### Backfill: provider-inferred, not naive default

Today's `AllowedProviders` already encodes a provider→endpoint mapping
(`HandleResponses` → only `openai`; `HandleChatCompletions` → only
`vertex_openai`; `HandleCountTokens` → only `anthropic`/`claudecode`; etc.).
The migration lifts that same mapping into `request_kinds` based on the
upstream group's member providers. Result: code-and-DB go live atomically and
existing traffic continues to land on the same upstreams it landed on
yesterday — no operator intervention required for the common case, no rollout
window where production endpoints 404.

Inference table (applied per upstream group; assigned to every route pointing
at that group):

| Upstream-group member providers                    | Inferred `request_kinds`                                |
| -------------------------------------------------- | ------------------------------------------------------- |
| All ∈ `{anthropic, claudecode}`                    | `['anthropic_messages', 'anthropic_count_tokens']`      |
| Includes `bedrock` or `vertex_anthropic` (no OpenAI/Gemini members) | `['anthropic_messages']` (count_tokens not supported on those upstreams today) |
| All = `openai`                                     | `['openai_responses']` (today `HandleResponses` only allows OpenAI) |
| All = `vertex_openai`                              | `['openai_chat_completions']` (today `HandleChatCompletions` only allows VertexOpenAI) |
| All ∈ `{gemini, vertex_google}`                    | `['google_generate_content']`                           |
| Mixed across families (Anthropic-side ∪ OpenAI-side ∪ Google-side) | `['anthropic_messages']` placeholder + RAISE NOTICE — operator must split the group |

```sql
-- migrations/019_route_request_kinds.sql
BEGIN;

ALTER TABLE routes
  ADD COLUMN request_kinds TEXT[] NOT NULL DEFAULT '{}';

-- Per-group provider inference. Done in a single UPDATE keyed off
-- aggregated provider sets; each route inherits the kinds of the group it
-- points to.
WITH group_providers AS (
    SELECT
        m.upstream_group_id AS gid,
        array_agg(DISTINCT u.provider) AS providers
    FROM upstream_group_members m
    JOIN upstreams u ON u.id = m.upstream_id
    GROUP BY m.upstream_group_id
),
group_kinds AS (
    SELECT
        gid,
        providers,
        CASE
            -- Anthropic-only family
            WHEN providers <@ ARRAY['anthropic','claudecode']::TEXT[]
                THEN ARRAY['anthropic_messages','anthropic_count_tokens']
            -- Anthropic-side, includes Bedrock or Vertex-Anthropic (no count_tokens)
            WHEN providers <@ ARRAY['anthropic','claudecode','bedrock','vertex_anthropic']::TEXT[]
                THEN ARRAY['anthropic_messages']
            -- Pure OpenAI native
            WHEN providers = ARRAY['openai']::TEXT[]
                THEN ARRAY['openai_responses']
            -- Pure Vertex OpenAI-compat
            WHEN providers = ARRAY['vertex_openai']::TEXT[]
                THEN ARRAY['openai_chat_completions']
            -- Google family
            WHEN providers <@ ARRAY['gemini','vertex_google']::TEXT[]
                THEN ARRAY['google_generate_content']
            -- Mixed / unrecognised
            ELSE ARRAY['anthropic_messages']
        END AS inferred_kinds,
        CASE
            WHEN providers <@ ARRAY['anthropic','claudecode']::TEXT[] THEN FALSE
            WHEN providers <@ ARRAY['anthropic','claudecode','bedrock','vertex_anthropic']::TEXT[] THEN FALSE
            WHEN providers = ARRAY['openai']::TEXT[] THEN FALSE
            WHEN providers = ARRAY['vertex_openai']::TEXT[] THEN FALSE
            WHEN providers <@ ARRAY['gemini','vertex_google']::TEXT[] THEN FALSE
            ELSE TRUE
        END AS is_mixed
    FROM group_providers
)
UPDATE routes r
SET request_kinds = gk.inferred_kinds
FROM group_kinds gk
WHERE r.upstream_group_id = gk.gid;

-- Routes whose upstream_group has no members (or null group) fall back to
-- the safe Anthropic default; they wouldn't route anywhere usable today.
UPDATE routes SET request_kinds = ARRAY['anthropic_messages'] WHERE request_kinds = '{}';

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

-- Audit: routes pointing at upstream groups whose members span more than
-- one provider family. Today these "shared groups" worked because
-- AllowedProviders filtered per-ingress; after this change, the route's
-- request_kinds is a placeholder and the group should be split. Output is
-- informational only.
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT
            rt.id AS route_id,
            rt.model_names,
            rt.upstream_group_id,
            array_agg(DISTINCT u.provider) AS providers
        FROM routes rt
        JOIN upstream_group_members m ON m.upstream_group_id = rt.upstream_group_id
        JOIN upstreams u ON u.id = m.upstream_id
        GROUP BY rt.id, rt.model_names, rt.upstream_group_id
        HAVING
            -- Mixed across the three families
            bool_or(u.provider IN ('anthropic','claudecode','bedrock','vertex_anthropic'))
              AND bool_or(u.provider IN ('openai','vertex_openai'))
            OR
            bool_or(u.provider IN ('anthropic','claudecode','bedrock','vertex_anthropic'))
              AND bool_or(u.provider IN ('gemini','vertex_google'))
            OR
            bool_or(u.provider IN ('openai','vertex_openai'))
              AND bool_or(u.provider IN ('gemini','vertex_google'))
    LOOP
        RAISE NOTICE 'Route % (models=%, group=%) has cross-family providers % — split the upstream group and assign request_kinds explicitly',
            r.route_id, r.model_names, r.upstream_group_id, r.providers;
    END LOOP;
END $$;

COMMIT;
```

### Why provider inference is safe here

Concern raised during review: "an OpenAI upstream might serve either
`openai_chat_completions` or `openai_responses` — heuristic is wrong." That
concern is valid in general but doesn't apply *to the migration*: today
`HandleResponses` is hard-wired to `openai` and `HandleChatCompletions` to
`vertex_openai`. So an OpenAI upstream group has only ever served
`/v1/responses` in production; tagging it `['openai_responses']` exactly
preserves observed behavior. After the new model is in place, operators are
free to add an additional `request_kinds=['openai_chat_completions']` to
that route (or split into two routes) if they want OpenAI to serve both
endpoints — that's a forward-going decision, not a migration concern.

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
    `IsStream: false`, **`HttpLogEnabled: false`** (see below), and the same
    auth/policy/trace plumbing as `handleProxyRequest`.
  - Calls `h.executor.Execute(w, r, reqCtx)`.

This means count_tokens automatically gains: retry, circuit breaker,
session-affinity (irrelevant in practice but harmless), and request-table
observability — at the cost of one extra DB insert per request.

**HTTP body logging is explicitly disabled for count_tokens.** In
`handleProxyRequest`, `HttpLogEnabled` is gated only on
`Publisher == Anthropic`, which count_tokens models satisfy. Since
count_tokens is invoked at high frequency by IDE clients (live token-count
estimation in the editor), inheriting full-body logging would balloon the
httplog store with low-value records. `HandleCountTokens` therefore sets the
flag to false unconditionally, regardless of publisher.

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
4. **Rate limit and extra-usage quotas remain model-keyed (not kind-keyed).**
   count_tokens consumes the same `(project, api_key, user, model)` quota
   bucket as `/v1/messages`. Splitting per-kind would require schema work in
   the rate limiter and extra-usage tables and is out of scope. Operators
   wanting count_tokens to be free-of-quota should route it to a free-tier
   upstream group via the new routing — quota itself stays unified.
5. **`/v1/models` (`HandleListModels`) is unchanged.** It returns the union
   of models with at least one active route, regardless of which kinds those
   routes serve. A model that is only configured for `openai_chat_completions`
   still appears in the list when the client calls `/v1/models` from any
   ingress; this is consistent with `/v1/models` being a coarse discovery
   endpoint, not a per-endpoint capability probe.

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

Migration test (run 003 → 019 against a fixture DB seeded with one route per
provider family):

- Anthropic-only group → `['anthropic_messages','anthropic_count_tokens']`.
- Group containing a Bedrock or VertexAnthropic upstream → `['anthropic_messages']` only.
- Pure-OpenAI group → `['openai_responses']`.
- Pure-VertexOpenAI group → `['openai_chat_completions']`.
- Gemini/VertexGoogle group → `['google_generate_content']`.
- Cross-family group (e.g. Anthropic + OpenAI in same group) → falls back to
  `['anthropic_messages']` placeholder AND emits a NOTICE line.
- CHECK constraint rejects an attempted update setting an unknown kind or
  an empty array.

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

This change ships as a **single atomic deploy**: migration + server + dashboard
together. Splitting them is unsafe — see "Why one PR" below.

1. Merge a single PR containing: migration 019, server changes (Match
   signature, AllowedProviders removal, HandleCountTokens delegation,
   HttpLogEnabled=false for count_tokens), admin API additions, and
   dashboard updates. CI runs the migration test from §Testing to verify
   provider inference matches the table above.
2. Deploy. The migration runs in-transaction with the schema change so
   existing routes immediately have correctly-inferred `request_kinds`
   when the new code starts serving traffic — no window where production
   endpoints 404 due to default-tagged rows.
3. Post-deploy, operators review the NOTICE output for cross-family
   shared groups (if any) and split them — those rows have placeholder
   `['anthropic_messages']` and need correction. Most deployments will
   have zero such groups.
4. Going forward, operators add new differentiated routes where desired
   (e.g. count_tokens to a free console key; OpenAI-compat to a dedicated
   proxy upstream).

### Why one PR

- Backend-only deploy without dashboard: `POST /admin/routing/routes` now
  requires `request_kinds`; the old dashboard would 400 on every route
  create.
- Dashboard-only deploy without backend: dashboard would send
  `request_kinds` to a backend that doesn't know the field, silently
  dropping it on persistence.
- Migration-only without backend: harmless; old code ignores the column.
- Backend-only without migration: `Router.Reload` would scan a missing
  column → DB error → server crash on startup.

The only safe order is migration → backend → dashboard within one
deployment unit. We bundle them.

## Risks

- **Cross-family shared upstream groups break.** If any deployment runs an
  upstream group whose members span Anthropic + OpenAI (or any cross-family
  combination), today `AllowedProviders` per-ingress filtered the candidate
  list correctly; after this change the route is migrated with a placeholder
  `['anthropic_messages']` and the original cross-wire routing stops working.
  Migration emits a NOTICE for each such route. Mitigation: operators split
  the shared group into per-family groups and assign explicit `request_kinds`.
  Pre-deploy, audit production with the migration's NOTICE query (can be
  run read-only in advance).
- **Removing `AllowedProviders` removes a defense-in-depth.** Once routes
  are kind-correct, a misconfigured route (kind says `anthropic_messages`
  but the upstream group's members are OpenAI) will send Anthropic-format
  bodies to an OpenAI upstream, which 4xx's. Previously this would have
  been silently filtered to "no upstreams available" (503). Visibility >
  silent masking, but operators should expect louder failure on
  misconfiguration.
- **count_tokens request-log volume.** count_tokens is typically invoked
  more frequently than `/v1/messages` (clients use it for live token-count
  estimation in the editor). Inserting a request row per call may double
  request-table write volume in some deployments. HTTP-body logging is
  already disabled for count_tokens (see §Code changes); the request row
  itself is small. If write volume still concerns operators, add a
  `log_requests=false` toggle on the route; out of scope for this iteration.
- **No external admin-API consumers assumed.** The admin API contract change
  (required `request_kinds` on POST/PUT) breaks any external automation
  that creates routes. We assume the dashboard is the only consumer; if
  any internal scripts exist they need updating in the same window.
