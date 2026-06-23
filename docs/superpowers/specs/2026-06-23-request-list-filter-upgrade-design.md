# Request-list filter upgrade — model & kind dropdowns + index

**Date:** 2026-06-23
**Status:** Approved (awaiting spec review)

## Problem

The admin dashboard's requests list (`RequestsPage.tsx`) exposes free-text inputs for the **model** filter and has **no filter at all** for `request_kind`. Free-text means users have to know the exact slug — `gpt-5.4-mini` vs `gpt-5.4`, `openai_chat_completions` vs `openai-chat-completions` — and a typo silently returns zero rows. Adding `request_kind` as a column-level filter is overdue (the kind enum has been stable for months, and the only way to look at "all my OpenAI chat-completions requests" today is to scroll).

On the storage side, `requests.model` has **no index**. The same filter that's getting a dropdown is also the slowest predicate on the project — sequential scans of `requests` for any reasonably-sized project. `requests.request_kind` already has a partial index from `025_request_kind.sql` so nothing to do there.

## Goals

1. Add a single index on `requests(model)` so the new dropdown's filter query is fast on large `requests` tables.
2. Convert the model filter from `<Input>` to `<Select>` populated from the project's visible model catalog.
3. Add a new `<Select>` for `request_kind` populated from the 8-value `types.AllRequestKinds` enum (frontend constant).

## Non-goals

- No CONCURRENTLY for the index (project migrations run inside transactions; matching the existing convention from 001/003/025). Operators should run this migration during a low-write window if their `requests` table is huge.
- No new "distinct values from requests" endpoint. We're using the catalog for models and a frontend constant for kinds.
- No `kind` column on the dropdown for cross-project admin views unless it's free — we'll only wire the per-project page in this PR.

## Design

### 1. Migration `054_add_request_indexes.sql`

```sql
-- 054_add_request_indexes.sql
--
-- Single index on requests.model to back the model dropdown's filter
-- query (`WHERE project_id = $1 AND model = $2`). request_kind already
-- has a partial index from 025_request_kind.sql (idx_requests_request_kind
-- WHERE request_kind <> ''), so no new index is needed for that column.
--
-- Runs inside the migration runner's transaction — same as all prior
-- index migrations on this table. CREATE INDEX (non-concurrent) briefly
-- locks writes; operators with large requests tables should apply during
-- a low-write window.

CREATE INDEX IF NOT EXISTS idx_requests_model ON requests(model);
```

**Test** (`internal/store/migrations_054_test.go`):

```go
func TestMigration054_ModelIndexExists(t *testing.T) {
    st := openTestStore(t)
    var exists bool
    err := st.pool.QueryRow(context.Background(), `
        SELECT EXISTS(
            SELECT 1 FROM pg_indexes
            WHERE schemaname = 'public'
              AND tablename = 'requests'
              AND indexname = 'idx_requests_model'
        )`).Scan(&exists)
    if err != nil {
        t.Fatalf("query pg_indexes: %v", err)
    }
    if !exists {
        t.Fatal("idx_requests_model not found after migration 054")
    }
}
```

Skipped without `TEST_DATABASE_URL`, same convention as other migration tests.

### 2. Backend — `RequestFilters.RequestKind`

`internal/store/requests.go`:

```go
type RequestFilters struct {
    Model       string
    RequestKind string  // ← new
    Status      string
    APIKeyID    string
    CreatedBy   string
    Since       time.Time
    Until       time.Time
}
```

Both `buildRequestFilters` and `buildGlobalRequestFilters` get one new block (mirroring `Model`):

```go
if f.RequestKind != "" {
    conditions = append(conditions, fmt.Sprintf("r.request_kind = $%d", n))
    args = append(args, f.RequestKind)
    n++
}
```

`internal/admin/handle_requests.go` — both handlers add one line:

```go
filters := store.RequestFilters{
    Model:       q.Get("model"),
    RequestKind: q.Get("request_kind"),  // ← new
    Status:      q.Get("status"),
    APIKeyID:    q.Get("api_key_id"),
}
```

**Tests** (TDD-first, `internal/store/requests_filters_test.go` — new file):
- `buildRequestFilters` with `RequestKind="openai_responses"` emits the right SQL predicate and arg.
- Empty `RequestKind` does not add a predicate.
- Filters compose correctly (Model + RequestKind + Status all set).

We do NOT validate `RequestKind` value at the API edge — invalid kinds will produce no matches, same idempotent posture as `Model`.

### 3. Frontend — `RequestsPage.tsx`

**Model**: replace the `<Input>` with a `<Select>` whose `<SelectItem>`s come from `useProjectModels(projectId)`. First item is `<SelectItem value="all">All models</SelectItem>`. Pattern is identical to the existing API-key dropdown right next to it.

**Kind**: new `<Select>` between the model and status dropdowns. Source is a frontend constant in `RequestsPage.tsx` (no separate file — 8 values, single use site):

```ts
const REQUEST_KINDS: ReadonlyArray<{ value: string; label: string }> = [
  { value: "anthropic_messages",       label: "Anthropic Messages" },
  { value: "anthropic_count_tokens",   label: "Anthropic Count Tokens" },
  { value: "openai_chat_completions",  label: "OpenAI Chat Completions" },
  { value: "openai_responses",         label: "OpenAI Responses" },
  { value: "openai_responses_compact", label: "OpenAI Responses Compact" },
  { value: "openai_images_generations",label: "OpenAI Images Generations" },
  { value: "openai_images_edits",      label: "OpenAI Images Edits" },
  { value: "google_generate_content",  label: "Google Generate Content" },
] as const;
```

State + handlers mirror `status`:

```tsx
const [requestKind, setRequestKind] = useState("");
// ...
<Select
  value={requestKind}
  onValueChange={(v) => {
    setRequestKind(!v || v === "all" ? "" : v);
    setPage(1);
  }}
>
  <SelectTrigger className="w-56"><SelectValue placeholder="All kinds" /></SelectTrigger>
  <SelectContent>
    <SelectItem value="all">All kinds</SelectItem>
    {REQUEST_KINDS.map((k) => (
      <SelectItem key={k.value} value={k.value}>{k.label}</SelectItem>
    ))}
  </SelectContent>
</Select>
```

**API client** (`dashboard/src/api/requests.ts`):

```ts
export interface RequestFilters {
  model?: string;
  request_kind?: string;  // ← new
  status?: string;
  // ...
}
```

`useRequests` adds:

```ts
if (filters.request_kind) params.set("request_kind", filters.request_kind);
```

### 4. Why not …

- **"Why not a `request-kinds` endpoint?"** The enum lives in `types.AllRequestKinds` and changes only when a new wire protocol is added — averaging once every few months. Hard-coding 8 strings in the frontend is two-orders-of-magnitude cheaper than a roundtrip, and a new kind is a coordinated backend+frontend change anyway.
- **"Why not DISTINCT from requests for the model dropdown?"** Two reasons. First, the existing `useProjectModels` hook is already there and matches the dropdown's semantic ("models this project can hit"). Second, on a brand-new project the DISTINCT query returns empty until the user makes a call — a worse UX than showing the full catalog.
- **"Why no CONCURRENTLY?"** The migration runner wraps each migration in a transaction (`store.go:111`), and `CREATE INDEX CONCURRENTLY` is forbidden inside a transaction. Changing the runner to support non-tx migrations is a bigger refactor than this PR warrants; one short write lock during off-hours is fine for the current `requests` table size.

## Testing

| Layer | Test | Where |
|---|---|---|
| Migration | Index `idx_requests_model` exists after migration 054 | `migrations_054_test.go` |
| Store | `buildRequestFilters` emits `request_kind = $N` only when set, composes with other filters | `requests_filters_test.go` (new) |
| Frontend | No unit test layer in this repo's dashboard today — manual: dropdowns render, change-event updates URL params, queries refire with new filters | manual |

## Rollout

1. Merge → `054_add_request_indexes.sql` applies on next service restart.
2. Frontend ships in the same PR.
3. No data migration. No client breakage — `request_kind` is a new optional query param.

## File touch list

```
internal/store/migrations/054_add_request_indexes.sql   (new)
internal/store/migrations_054_test.go                   (new)
internal/store/requests.go                              (modify — RequestFilters + 2 builders)
internal/store/requests_filters_test.go                 (new — TDD)
internal/admin/handle_requests.go                       (modify — 2 handler funcs, 1 line each)
dashboard/src/api/requests.ts                           (modify — RequestFilters + useRequests param)
dashboard/src/pages/requests/RequestsPage.tsx           (modify — model Input→Select, new kind Select)
```
