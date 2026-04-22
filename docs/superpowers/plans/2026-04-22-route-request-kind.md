# Route Request-Kind Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace ingress-based provider filtering with a `request_kind` dimension on routes — so `(project_id, model, request_kind)` keys the routing decision and `RequestContext.AllowedProviders` can be deleted, while `HandleCountTokens` becomes a normal `executor.Execute` consumer.

**Architecture:** A single atomic deploy. Add `request_kinds TEXT[]` to `routes`, backfill via per-group provider inference, extend `Router.Match` with a kind argument, set `reqCtx.RequestKind` from each handler, drop the `AllowedProviders` field everywhere, rewrite `HandleCountTokens` to delegate to `executor.Execute`, expose the new field through the admin API and dashboard.

**Tech Stack:** Go 1.22+ (`net/http`, `database/sql`, `chi`), PostgreSQL (raw SQL migrations under `internal/store/migrations/`), TypeScript + React + TanStack Query (dashboard).

**Reference spec:** `docs/superpowers/specs/2026-04-22-route-request-kind-design.md`

---

## Setup

- [ ] **Setup-1: Create a fresh worktree off `main`**

The existing `.claude/worktrees/routing-redesign` worktree has diverged from `main` and must NOT be reused. Create a new one.

```bash
cd /root/coding/modelserver
git worktree add .claude/worktrees/route-request-kind -b feat/route-request-kind main
cd .claude/worktrees/route-request-kind
go test ./... 2>&1 | tail -3   # confirm green baseline
```

Expected: `ok` for every package.

- [ ] **Setup-2: Open and re-read the spec inside the new worktree before writing any code**

```bash
$EDITOR docs/superpowers/specs/2026-04-22-route-request-kind-design.md
```

Mental model: routes today are matched by `(project_id, model)`; the per-handler `AllowedProviders` filter is what stops `/v1/responses` from accidentally landing on an Anthropic upstream. We are moving that filter into the routing table itself.

---

## Phase 1: Type constants

### Task 1: Add request-kind constants and validator

**Files:**
- Create: `internal/types/request_kind.go`
- Test: `internal/types/request_kind_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/types/request_kind_test.go`:

```go
package types

import "testing"

func TestIsValidRequestKind_KnownConstants(t *testing.T) {
	for _, k := range AllRequestKinds {
		if !IsValidRequestKind(k) {
			t.Errorf("IsValidRequestKind(%q) = false, want true", k)
		}
	}
}

func TestIsValidRequestKind_RejectsUnknown(t *testing.T) {
	for _, k := range []string{"", "anthropic_complete", "OPENAI_RESPONSES", "openai-responses"} {
		if IsValidRequestKind(k) {
			t.Errorf("IsValidRequestKind(%q) = true, want false", k)
		}
	}
}

func TestAllRequestKinds_ContainsExactlyFive(t *testing.T) {
	if got := len(AllRequestKinds); got != 5 {
		t.Errorf("len(AllRequestKinds) = %d, want 5", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/types/ -run TestIsValidRequestKind -v
```

Expected: `undefined: AllRequestKinds`, `undefined: IsValidRequestKind`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/types/request_kind.go`:

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
		if k == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./internal/types/ -run TestIsValidRequestKind -v
```

Expected: PASS for all three subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/types/request_kind.go internal/types/request_kind_test.go
git commit -m "feat(types): add request-kind constants and validator"
```

---

## Phase 2: Database & store

### Task 2: Migration 021 — add column, backfill, constrain

**Files:**
- Create: `internal/store/migrations/021_route_request_kinds.sql`
- Test: `internal/store/store_test.go` (extend, or add `migrations_021_test.go` if the package convention is per-migration)

The spec drafted "019" but the highest existing migration is `020_add_max_200x_plan.sql`. Use **021**.

- [ ] **Step 1: Write a failing migration test**

Locate the existing migration test pattern first:

```bash
ls internal/store/migrations/
grep -l "ApplyMigrations\|RunMigrations\|migrate" internal/store/*.go | head -3
```

Add a test (in whichever file the existing migration tests live; if none exist, create `internal/store/migration_021_test.go`). The test fixture seeds one route per provider family and asserts the inferred kinds.

```go
func TestMigration021_BackfillsRequestKindsByProviderFamily(t *testing.T) {
	db := newTestDB(t) // existing helper that runs migrations 001..020
	mustExec(t, db, `INSERT INTO upstream_groups(id,name) VALUES ('g_anth','anth'),('g_va','va'),('g_oai','oai'),('g_voai','voai'),('g_gem','gem'),('g_mix','mix')`)
	mustExec(t, db, `INSERT INTO upstreams(id,name,provider) VALUES
		('u_anth','u','anthropic'),('u_va','u','vertex_anthropic'),
		('u_oai','u','openai'),('u_voai','u','vertex_openai'),
		('u_gem','u','gemini'),('u_mix1','u','anthropic'),('u_mix2','u','openai')`)
	mustExec(t, db, `INSERT INTO upstream_group_members(upstream_group_id,upstream_id) VALUES
		('g_anth','u_anth'),('g_va','u_va'),
		('g_oai','u_oai'),('g_voai','u_voai'),('g_gem','u_gem'),
		('g_mix','u_mix1'),('g_mix','u_mix2')`)
	mustExec(t, db, `INSERT INTO routes(id,model_names,upstream_group_id,match_priority,status) VALUES
		('r_anth', ARRAY['m'], 'g_anth', 0, 'active'),
		('r_va',   ARRAY['m'], 'g_va',   0, 'active'),
		('r_oai',  ARRAY['m'], 'g_oai',  0, 'active'),
		('r_voai', ARRAY['m'], 'g_voai', 0, 'active'),
		('r_gem',  ARRAY['m'], 'g_gem',  0, 'active'),
		('r_mix',  ARRAY['m'], 'g_mix',  0, 'active')`)

	applyMigration(t, db, "021_route_request_kinds.sql")

	cases := map[string][]string{
		"r_anth": {"anthropic_messages", "anthropic_count_tokens"},
		"r_va":   {"anthropic_messages"},
		"r_oai":  {"openai_responses"},
		"r_voai": {"openai_chat_completions"},
		"r_gem":  {"google_generate_content"},
		"r_mix":  {"anthropic_messages"}, // placeholder for cross-family
	}
	for id, want := range cases {
		var got []string
		mustQueryRow(t, db, `SELECT request_kinds FROM routes WHERE id=$1`, id).Scan(pq.Array(&got))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("route %s: got %v, want %v", id, got, want)
		}
	}
}

func TestMigration021_CheckRejectsEmptyAndUnknown(t *testing.T) {
	db := newTestDB(t)
	applyMigration(t, db, "021_route_request_kinds.sql")
	mustExec(t, db, `INSERT INTO upstream_groups(id,name) VALUES ('g','g')`)
	if _, err := db.Exec(`INSERT INTO routes(id,model_names,upstream_group_id,request_kinds,match_priority,status) VALUES ('x', ARRAY['m'], 'g', ARRAY[]::TEXT[], 0, 'active')`); err == nil {
		t.Error("empty request_kinds should be rejected by CHECK constraint")
	}
	if _, err := db.Exec(`INSERT INTO routes(id,model_names,upstream_group_id,request_kinds,match_priority,status) VALUES ('y', ARRAY['m'], 'g', ARRAY['nope'], 0, 'active')`); err == nil {
		t.Error("unknown request_kind should be rejected by CHECK constraint")
	}
}
```

If the project already has a migration test harness with different helpers, mirror its style.

- [ ] **Step 2: Run the migration test to verify it fails**

```bash
go test ./internal/store/ -run TestMigration021 -v
```

Expected: cannot open `021_route_request_kinds.sql` (file does not exist).

- [ ] **Step 3: Write the migration file**

Create `internal/store/migrations/021_route_request_kinds.sql` with the exact contents from spec §Schema (the `BEGIN ... COMMIT` block including the `WITH group_providers AS ...` UPDATE, the empty-fallback UPDATE, the CHECK constraint, the GIN index, and the cross-family DO-block NOTICE).

- [ ] **Step 4: Run the migration test to verify it passes**

```bash
go test ./internal/store/ -run TestMigration021 -v
```

Expected: both subtests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/021_route_request_kinds.sql internal/store/migration_021_test.go
git commit -m "feat(store): migration 021 adds request_kinds with provider-inferred backfill"
```

### Task 3: Add `RequestKinds` to the `Route` struct

**Files:**
- Modify: `internal/types/route.go:1-19`

- [ ] **Step 1: Write the failing test**

Append to `internal/types/route.go`'s test file (create `internal/types/route_test.go` if missing):

```go
package types

import (
	"encoding/json"
	"testing"
)

func TestRoute_JSONRoundtripIncludesRequestKinds(t *testing.T) {
	in := Route{ID: "r1", ModelNames: []string{"m"}, RequestKinds: []string{KindAnthropicMessages}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(b), `"request_kinds":["anthropic_messages"]`) {
		t.Errorf("missing request_kinds in JSON: %s", b)
	}
	var out Route
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.RequestKinds) != 1 || out.RequestKinds[0] != KindAnthropicMessages {
		t.Errorf("RequestKinds roundtrip lost data: %v", out.RequestKinds)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || (len(s) > 0 && len(sub) > 0 && (string(s[0]) == string(sub[0]) && (s == sub || (len(s) > len(sub) && (containsHelper(s, sub))))))) }
func containsHelper(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub { return true }
	}
	return false
}
```

(If the package already imports `strings`, just use `strings.Contains` and skip the helper.)

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/types/ -run TestRoute_JSONRoundtripIncludesRequestKinds -v
```

Expected: `unknown field RequestKinds in struct literal of type types.Route`.

- [ ] **Step 3: Add the field**

Edit `internal/types/route.go` — add the field after `ModelNames`:

```go
type Route struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id,omitempty"`
	ModelNames      []string          `json:"model_names"`
	RequestKinds    []string          `json:"request_kinds"`
	UpstreamGroupID string            `json:"upstream_group_id"`
	MatchPriority   int               `json:"match_priority"`
	Conditions      map[string]string `json:"conditions,omitempty"`
	Status          string            `json:"status"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}
```

- [ ] **Step 4: Verify the test passes**

```bash
go test ./internal/types/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/types/route.go internal/types/route_test.go
git commit -m "feat(types): add RequestKinds field to Route"
```

### Task 4: Wire `request_kinds` through the store

**Files:**
- Modify: `internal/store/routes.go`
  - `scanRoute` (lines 161-170)
  - `CreateRoute` (lines 14-30)
  - `UpdateRoute` allowlist (around lines 143-148)
  - `ListRoutes` (lines 53-75), `ListRoutesPaginated` (lines 78-109), `ListRoutesForProject` (lines 114-140)
- Test: `internal/store/routes_test.go` (extend)

- [ ] **Step 1: Write failing tests covering create + update + list round-trip**

Add to `internal/store/routes_test.go`:

```go
func TestCreateRoute_PersistsRequestKinds(t *testing.T) {
	st := newTestStore(t) // existing helper
	mustSeedGroup(t, st, "g1")
	r := &types.Route{
		ID: "r1", ModelNames: []string{"m"}, UpstreamGroupID: "g1",
		RequestKinds: []string{types.KindAnthropicMessages, types.KindAnthropicCountTokens},
		MatchPriority: 0, Status: "active",
	}
	if err := st.CreateRoute(r); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListRoutes()
	if err != nil { t.Fatal(err) }
	if len(got) != 1 || !reflect.DeepEqual(got[0].RequestKinds, r.RequestKinds) {
		t.Errorf("RequestKinds not persisted: %+v", got)
	}
}

func TestUpdateRoute_ReplacesRequestKinds(t *testing.T) {
	st := newTestStore(t)
	mustSeedGroup(t, st, "g1")
	st.CreateRoute(&types.Route{ID: "r1", ModelNames: []string{"m"}, UpstreamGroupID: "g1",
		RequestKinds: []string{types.KindAnthropicMessages}, Status: "active"})
	if err := st.UpdateRoute("r1", map[string]any{
		"request_kinds": []string{types.KindOpenAIResponses},
	}); err != nil { t.Fatal(err) }
	got, _ := st.ListRoutes()
	if got[0].RequestKinds[0] != types.KindOpenAIResponses {
		t.Errorf("update did not replace RequestKinds: %v", got[0].RequestKinds)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/store/ -run "TestCreateRoute_PersistsRequestKinds|TestUpdateRoute_ReplacesRequestKinds" -v
```

Expected: column "request_kinds" not in scan / not in INSERT.

- [ ] **Step 3: Update the store**

In `internal/store/routes.go`:

1. **`scanRoute`** — change the SELECT column list and the `Scan(...)` call to include `request_kinds`. Use `pq.Array(&r.RequestKinds)` for the `[]string` decode (the package already imports `github.com/lib/pq`).
2. **`CreateRoute`** — add `request_kinds` to the INSERT column list and to the parameter slice; pass `pq.Array(r.RequestKinds)`.
3. **`UpdateRoute`** — extend the updatable-field allowlist to include `"request_kinds"`. The existing `buildUpdateQuery` path needs to wrap `[]string` values via `pq.Array(...)` before binding; if it doesn't, add a small switch in the helper.
4. **`ListRoutes`, `ListRoutesPaginated`, `ListRoutesForProject`** — extend each SELECT to include `request_kinds` (they all funnel through `scanRoute`, so updating `scanRoute` is sufficient if the SELECT lists are factored; otherwise update each SELECT explicitly).

Concrete diffs (representative — engineer to apply to actual columns list):

```go
// scanRoute SELECT (line ~161)
const routeSelectCols = `id, COALESCE(project_id::text, ''), model_names, request_kinds,
    upstream_group_id, match_priority, conditions, status, created_at, updated_at`

// scan into struct
err := row.Scan(&r.ID, &r.ProjectID, pq.Array(&r.ModelNames), pq.Array(&r.RequestKinds),
    &r.UpstreamGroupID, &r.MatchPriority, &conditionsJSON, &r.Status, &r.CreatedAt, &r.UpdatedAt)
```

```go
// CreateRoute (line ~14)
_, err := s.db.Exec(`INSERT INTO routes(
    id, project_id, model_names, request_kinds, upstream_group_id,
    match_priority, conditions, status
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
    r.ID, nullableID(r.ProjectID), pq.Array(r.ModelNames), pq.Array(r.RequestKinds),
    r.UpstreamGroupID, r.MatchPriority, conditionsJSON, r.Status)
```

```go
// UpdateRoute allowlist (line ~143)
allowed := map[string]bool{
    "project_id": true, "model_names": true, "request_kinds": true,
    "upstream_group_id": true, "match_priority": true, "conditions": true, "status": true,
}
```

If `buildUpdateQuery` doesn't already special-case `[]string`:

```go
case []string:
    args = append(args, pq.Array(v))
```

- [ ] **Step 4: Run all store tests**

```bash
go test ./internal/store/ -v 2>&1 | tail -30
```

Expected: PASS, including the two new tests and any pre-existing route tests.

- [ ] **Step 5: Commit**

```bash
git add internal/store/routes.go internal/store/routes_test.go
git commit -m "feat(store): read/write request_kinds on Route"
```

---

## Phase 3: Router

### Task 5: Extend `Router.Match` with a `kind` argument

**Files:**
- Modify: `internal/proxy/router_engine.go:232-261`
- Test: `internal/proxy/router_engine_test.go` (extend)

- [ ] **Step 1: Write failing tests**

Add to `internal/proxy/router_engine_test.go`:

```go
func TestMatch_KindIsRequired_NoMatchingKindReturnsError(t *testing.T) {
	r := newTestRouter(t,
		&types.Route{ID: "r1", ProjectID: "p", ModelNames: []string{"m"},
			UpstreamGroupID: "g", RequestKinds: []string{types.KindAnthropicMessages}, Status: "active"})
	if _, err := r.Match("p", "m", types.KindOpenAIResponses); err == nil {
		t.Error("expected error when kind doesn't match any route")
	}
}

func TestMatch_MultiKindRouteServesBothEndpoints(t *testing.T) {
	r := newTestRouter(t,
		&types.Route{ID: "r1", ProjectID: "p", ModelNames: []string{"m"},
			UpstreamGroupID: "g", RequestKinds: []string{
				types.KindAnthropicMessages, types.KindAnthropicCountTokens,
			}, Status: "active"})
	for _, k := range []string{types.KindAnthropicMessages, types.KindAnthropicCountTokens} {
		if _, err := r.Match("p", "m", k); err != nil {
			t.Errorf("kind %s: unexpected error %v", k, err)
		}
	}
}

func TestMatch_KindMismatchSkipsRoute_FallsThroughToGlobal(t *testing.T) {
	r := newTestRouter(t,
		&types.Route{ID: "r_proj", ProjectID: "p", ModelNames: []string{"m"},
			UpstreamGroupID: "g_proj", RequestKinds: []string{types.KindAnthropicMessages},
			MatchPriority: 100, Status: "active"},
		&types.Route{ID: "r_global", ProjectID: "", ModelNames: []string{"m"},
			UpstreamGroupID: "g_global", RequestKinds: []string{types.KindAnthropicCountTokens},
			MatchPriority: 0, Status: "active"})
	g, err := r.Match("p", "m", types.KindAnthropicCountTokens)
	if err != nil { t.Fatal(err) }
	if g.group.ID != "g_global" {
		t.Errorf("expected fallthrough to g_global, got %s", g.group.ID)
	}
}

func TestMatch_ProjectKindBeatsGlobalKind(t *testing.T) {
	r := newTestRouter(t,
		&types.Route{ID: "r_proj", ProjectID: "p", ModelNames: []string{"m"},
			UpstreamGroupID: "g_proj", RequestKinds: []string{types.KindAnthropicMessages},
			MatchPriority: 0, Status: "active"},
		&types.Route{ID: "r_global", ProjectID: "", ModelNames: []string{"m"},
			UpstreamGroupID: "g_global", RequestKinds: []string{types.KindAnthropicMessages},
			MatchPriority: 100, Status: "active"})
	g, err := r.Match("p", "m", types.KindAnthropicMessages)
	if err != nil { t.Fatal(err) }
	if g.group.ID != "g_proj" {
		t.Errorf("project route should beat global, got %s", g.group.ID)
	}
}
```

If `newTestRouter` doesn't exist, the existing tests will show the established pattern for constructing a `Router` with seeded routes; replicate it.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/proxy/ -run TestMatch_ -v 2>&1 | tail -20
```

Expected: compilation error (`Match` takes 2 args, not 3) or test failures.

- [ ] **Step 3: Update `Router.Match`**

Edit `internal/proxy/router_engine.go:232`:

```go
func (r *Router) Match(projectID, model, kind string) (*resolvedGroup, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Project-specific pass.
	for _, route := range r.projectRoutes[projectID] {
		if slices.Contains(route.ModelNames, model) &&
			slices.Contains(route.RequestKinds, kind) {
			if g, ok := r.groups[route.UpstreamGroupID]; ok {
				return g, nil
			}
		}
	}
	// Global pass.
	for _, route := range r.globalRoutes {
		if slices.Contains(route.ModelNames, model) &&
			slices.Contains(route.RequestKinds, kind) {
			if g, ok := r.groups[route.UpstreamGroupID]; ok {
				return g, nil
			}
		}
	}
	return nil, fmt.Errorf("no route configured for model %s on endpoint %s", model, kind)
}
```

(Adapt to the actual data structures `projectRoutes`/`globalRoutes` as the file currently has them.)

- [ ] **Step 4: Update the existing call sites so the package still builds**

Two callers must change in this same task to keep the tree compilable:

- `internal/proxy/executor.go:165` — `group, err := e.router.Match(reqCtx.ProjectID, reqCtx.Model, reqCtx.RequestKind)`. `RequestKind` doesn't exist yet — defer the actual struct edit to Task 6 but add a placeholder field ONLY temporarily? No — instead, change this call in the same edit and accept that the build will be broken until Task 6 completes. To avoid that: do this task **and** Task 6's struct edit in the same commit.
- `internal/proxy/handler.go:385` — same change inside `HandleCountTokens`. This is going to be deleted by Task 12 anyway. For now, pass `types.KindAnthropicCountTokens`.

Easiest sequencing: combine Tasks 5 + 6 into one commit. Use this checkpoint to do that.

- [ ] **Step 5: Run the router tests**

```bash
go test ./internal/proxy/ -run TestMatch_ -v
```

Expected: 4 PASS.

- [ ] **Step 6: Commit (after Task 6 is also done — see Task 6 step 5)**

---

## Phase 4: Executor

### Task 6: `RequestContext`: drop `AllowedProviders`, add `RequestKind`

**Files:**
- Modify: `internal/proxy/executor.go:36-73` (RequestContext)
- Modify: `internal/proxy/executor.go:155-587` (Execute body — the AllowedProviders filter block at 192-207)
- Modify: every call site of `Match` to pass `reqCtx.RequestKind`

- [ ] **Step 1: Edit the struct**

In `internal/proxy/executor.go`:

```go
type RequestContext struct {
	// ... existing fields ...

	// REMOVE this line:
	// AllowedProviders []string

	// ADD:
	RequestKind string

	// ... rest unchanged ...
}
```

- [ ] **Step 2: Delete the AllowedProviders filter block in `Execute`**

Remove lines 192-207 of `internal/proxy/executor.go` (the entire `if len(reqCtx.AllowedProviders) > 0 { ... }` block). Leave `candidates` as returned by `SelectWithRetry`.

- [ ] **Step 3: Pass `kind` to `Match`**

Replace:

```go
group, err := e.router.Match(reqCtx.ProjectID, reqCtx.Model)
```

with:

```go
group, err := e.router.Match(reqCtx.ProjectID, reqCtx.Model, reqCtx.RequestKind)
```

- [ ] **Step 4: Build to surface every remaining caller of the old field/signature**

```bash
go build ./... 2>&1 | head -40
```

Expected: errors at `handler.go` for both `AllowedProviders` references and the old `Match` call inside `HandleCountTokens` and `HandleGemini`. Note them — they will be fixed in Task 9–12.

- [ ] **Step 5: Stub the handler errors so the tree compiles for Phase-4 tests**

The fastest patch: in `handler.go`, every place that today does `AllowedProviders: []string{...}` change to `RequestKind: types.KindXxx`, and every `Match(... project.ID, reqShape.Model)` call gets a third arg. This duplicates Task 9–12 but keeps the build green during Task 7 testing — so do it now even if Task 9 polishes further.

Mapping table to apply right now:

| Handler              | Set `RequestKind` to             |
| -------------------- | -------------------------------- |
| HandleMessages       | `types.KindAnthropicMessages`     |
| HandleResponses      | `types.KindOpenAIResponses`       |
| HandleChatCompletions| `types.KindOpenAIChatCompletions` |
| HandleGemini         | `types.KindGoogleGenerateContent` |
| HandleCountTokens    | `types.KindAnthropicCountTokens`  |

- [ ] **Step 6: Run the proxy package tests**

```bash
go test ./internal/proxy/ 2>&1 | tail -15
```

Expected: PASS for router tests; existing handler tests may need adjustment if they constructed `RequestContext` with `AllowedProviders`. Update those tests to use `RequestKind` instead. Other tests should be unaffected.

- [ ] **Step 7: Commit**

```bash
git add internal/proxy/router_engine.go internal/proxy/router_engine_test.go \
        internal/proxy/executor.go internal/proxy/handler.go
git commit -m "feat(proxy): replace AllowedProviders filter with RequestKind dimension"
```

This commit covers Tasks 5 + 6 together because they form one coherent compile-passing change.

### Task 7: Skip `ParseResponse` for count_tokens

**Files:**
- Modify: `internal/proxy/executor.go` — `commitNonStreamingResponse` (lines 921-1067)
- Test: `internal/proxy/executor_test.go` (extend)

- [ ] **Step 1: Write the failing test**

Add a test that drives a non-streaming count_tokens response (`{"input_tokens": 42}`) through the executor and asserts:

1. The request row is finalized with status=success.
2. `transformer.ParseResponse` is never called on the body (or its `failed to parse response` warning is suppressed).
3. Token counts on the request row are zero.

Use the existing fake-upstream test scaffolding (look at `executor_test.go` for the closest existing test that exercises a non-stream response).

```go
func TestExecute_CountTokens_SkipsParseResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer upstream.Close()

	e := newTestExecutor(t, upstream.URL) // existing helper
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens",
		bytes.NewReader([]byte(`{"model":"claude-opus-4-7","messages":[]}`)))
	reqCtx := &RequestContext{
		ProjectID: "p", Model: "claude-opus-4-7",
		RequestKind: types.KindAnthropicCountTokens,
		IsStream: false, RequestID: "req-1",
	}
	e.Execute(rec, req, reqCtx)

	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	row := mustGetRequest(t, e.store, "req-1")
	if row.Status != types.RequestStatusSuccess {
		t.Errorf("status = %s, want success", row.Status)
	}
	if row.InputTokens != 0 || row.OutputTokens != 0 {
		t.Errorf("token counts should be zero for count_tokens, got %d/%d", row.InputTokens, row.OutputTokens)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/proxy/ -run TestExecute_CountTokens_SkipsParseResponse -v
```

Expected: a `failed to parse response` log warning will fire AND the row may still be marked success (depending on existing handling); the assertion most likely to fail first is whichever indicates `ParseResponse` ran. If the existing code already tolerates parse failure with success status + zero tokens, the test will pass — in which case still add the explicit kind-skip below to make the intent durable.

- [ ] **Step 3: Add the kind-skip in both ParseResponse paths**

`internal/proxy/executor.go` has two `ParseResponse` call sites (~lines 954 and 975). Wrap each:

```go
if reqCtx.RequestKind != types.KindAnthropicCountTokens {
    respMetrics, parseErr = transformer.ParseResponse(body)
} else {
    respMetrics, parseErr = nil, nil
}
```

Also gate the extra-usage settle path (around line 954 area) on the same condition — count_tokens has no usage, so skipping is correct.

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test ./internal/proxy/ -run TestExecute_CountTokens_SkipsParseResponse -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/executor.go internal/proxy/executor_test.go
git commit -m "feat(proxy): skip ParseResponse for count_tokens kind"
```

---

## Phase 5: Handlers

### Task 8: Refactor `handleProxyRequest` to take `(ingress, kind)` directly

**Files:**
- Modify: `internal/proxy/handler.go:221` (handleProxyRequest)
- Modify: `internal/proxy/handler.go:72-95` (HandleMessages, HandleResponses, HandleChatCompletions wrappers)
- Modify: `internal/proxy/handler.go:100-217` (HandleGemini)

`ingressForProviders` derived the ingress string from `allowedProviders`. With `allowedProviders` gone, `handleProxyRequest` should take `ingress` as a parameter (each handler already knows its own ingress).

- [ ] **Step 1: Change `handleProxyRequest` signature**

```go
func (h *Handler) handleProxyRequest(w http.ResponseWriter, r *http.Request, ingress, kind string) {
    // ... existing body, but:
    // - delete: ingress := ingressForProviders(allowedProviders)
    // - delete: any reference to allowedProviders
    // - keep: canonical, ok := h.resolveModel(w, reqShape.Model, ingress)
    // - set: reqCtx.RequestKind = kind  (the wrappers already set it, but keep the parameter authoritative)
}
```

- [ ] **Step 2: Update the three thin wrappers**

```go
func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
    h.handleProxyRequest(w, r, IngressAnthropic, types.KindAnthropicMessages)
}
func (h *Handler) HandleResponses(w http.ResponseWriter, r *http.Request) {
    h.handleProxyRequest(w, r, IngressOpenAI, types.KindOpenAIResponses)
}
func (h *Handler) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
    h.handleProxyRequest(w, r, IngressOpenAI, types.KindOpenAIChatCompletions)
}
```

- [ ] **Step 3: Update `HandleGemini`**

It builds `RequestContext` directly. Replace `AllowedProviders: []string{...}` with `RequestKind: types.KindGoogleGenerateContent`. Verify it already passes `IngressGemini` to `resolveModel`; if not, do so.

- [ ] **Step 4: Build and run all tests**

```bash
go build ./... && go test ./internal/proxy/ 2>&1 | tail -10
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/handler.go
git commit -m "refactor(proxy): pass ingress+kind into handleProxyRequest directly"
```

### Task 9: Rewrite `HandleCountTokens` to delegate to `executor.Execute`

**Files:**
- Modify: `internal/proxy/handler.go:348-432` (entire function body)
- Test: `internal/proxy/handler_count_tokens_test.go` (create or extend)

- [ ] **Step 1: Write the failing tests**

Create `internal/proxy/handler_count_tokens_test.go`:

```go
package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestCountTokens_NoMatchingRouteReturns404(t *testing.T) {
	h := newTestHandler(t, /* no count_tokens route configured */)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens",
		bytes.NewReader([]byte(`{"model":"m","messages":[]}`)))
	req = withAPIKeyAndProject(req, "k", "p")
	h.HandleCountTokens(rec, req)
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("count_tokens")) {
		t.Errorf("body should mention endpoint name, got %s", rec.Body.String())
	}
}

func TestCountTokens_RoutesIndependentlyFromMessages(t *testing.T) {
	groupA := startFakeAnthropicUpstream(t, `{"id":"msg_1","content":[],"usage":{"input_tokens":1}}`)
	groupB := startFakeAnthropicUpstream(t, `{"input_tokens":99}`)
	h := newTestHandler(t,
		&types.Route{ID: "r1", ProjectID: "p", ModelNames: []string{"m"},
			UpstreamGroupID: groupA.ID, RequestKinds: []string{types.KindAnthropicMessages}, Status: "active"},
		&types.Route{ID: "r2", ProjectID: "p", ModelNames: []string{"m"},
			UpstreamGroupID: groupB.ID, RequestKinds: []string{types.KindAnthropicCountTokens}, Status: "active"})

	// /v1/messages goes to group A
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages",
			bytes.NewReader([]byte(`{"model":"m","messages":[]}`)))
		h.HandleMessages(rec, withAPIKeyAndProject(req, "k", "p"))
		if rec.Code != 200 || groupB.HitCount() != 0 {
			t.Errorf("messages should hit groupA only; A=%d B=%d", groupA.HitCount(), groupB.HitCount())
		}
	}
	// count_tokens goes to group B
	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages/count_tokens",
			bytes.NewReader([]byte(`{"model":"m","messages":[]}`)))
		h.HandleCountTokens(rec, withAPIKeyAndProject(req, "k", "p"))
		if rec.Code != 200 {
			t.Fatalf("count_tokens status = %d body=%s", rec.Code, rec.Body.String())
		}
		var got struct{ InputTokens int `json:"input_tokens"` }
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got.InputTokens != 99 || groupB.HitCount() != 1 {
			t.Errorf("count_tokens should hit groupB; got %+v B=%d", got, groupB.HitCount())
		}
	}
}

func TestCountTokens_DoesNotEnableHttpBodyLog(t *testing.T) {
	logger := newRecordingHttpLogger(t) // existing helper or one-line stub
	h := newTestHandlerWithLogger(t, logger,
		&types.Route{ID: "r1", ProjectID: "p", ModelNames: []string{"m"},
			UpstreamGroupID: "g", RequestKinds: []string{types.KindAnthropicCountTokens}, Status: "active"})
	// (model in catalog has Publisher=Anthropic so the publisher gate alone
	//  would have enabled logging — verify count_tokens overrides it.)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens",
		bytes.NewReader([]byte(`{"model":"m","messages":[]}`)))
	h.HandleCountTokens(rec, withAPIKeyAndProject(req, "k", "p"))
	if got := logger.RecordCount(); got != 0 {
		t.Errorf("httplog records = %d, want 0 for count_tokens", got)
	}
}

func TestCountTokens_RetriesOnUpstreamError(t *testing.T) {
	failing := startFlakyUpstream(t, /*fail first, succeed second*/)
	h := newTestHandlerWithRetry(t, &types.RetryPolicy{MaxRetries: 1}, failing)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages/count_tokens",
		bytes.NewReader([]byte(`{"model":"m","messages":[]}`)))
	h.HandleCountTokens(rec, withAPIKeyAndProject(req, "k", "p"))
	if rec.Code != 200 {
		t.Errorf("expected retry success, got %d", rec.Code)
	}
}
```

(The `newTestHandler*`/`startFakeAnthropicUpstream`/`withAPIKeyAndProject` helpers should already exist for the other handler tests — replicate the established pattern. If a `RequestStatusSuccess` row insertion check is needed, copy from the closest existing test.)

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/proxy/ -run TestCountTokens_ -v 2>&1 | tail -20
```

Expected: today's `HandleCountTokens` returns 502 / 503 / 200 with hardcoded provider whitelist behavior — the new test expectations will fail.

- [ ] **Step 3: Replace `HandleCountTokens`'s body**

Delete lines 348-432 entirely and rewrite to mirror `handleProxyRequest`'s body but with these specific differences:

```go
func (h *Handler) HandleCountTokens(w http.ResponseWriter, r *http.Request) {
	apiKey := APIKeyFromContext(r.Context())
	project := ProjectFromContext(r.Context())
	if apiKey == nil || project == nil {
		writeProxyError(w, http.StatusInternalServerError, "missing auth context")
		return
	}

	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBodySize))
	if err != nil {
		writeProxyError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var reqShape struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(bodyBytes, &reqShape)

	canonical, ok := h.resolveModel(w, reqShape.Model, IngressAnthropic)
	if !ok {
		return
	}
	if canonical != reqShape.Model {
		bodyBytes, _ = sjson.SetBytes(bodyBytes, "model", canonical)
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
		reqShape.Model = canonical
	}
	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, canonical) {
		writeProxyError(w, http.StatusForbidden, "model not allowed for this API key")
		return
	}

	policy := PolicyFromContext(r.Context())
	traceID := TraceIDFromContext(r.Context())
	oauthGrantID := OAuthGrantIDFromContext(r.Context())

	metadata := map[string]string{}
	if v := r.Header.Get("Anthropic-Beta"); v != "" {
		metadata["anthropic_beta"] = v
	}
	if v := r.Header.Get("Anthropic-Version"); v != "" {
		metadata["anthropic_version"] = v
	}
	if v := r.Header.Get("User-Agent"); v != "" {
		metadata["user_agent"] = v
	}

	pendingReq := &types.Request{
		ProjectID: project.ID, APIKeyID: apiKey.ID, OAuthGrantID: oauthGrantID,
		CreatedBy: apiKey.CreatedBy, TraceID: traceID,
		Model: reqShape.Model, Streaming: false,
		Status: types.RequestStatusProcessing,
		ClientIP: r.RemoteAddr, Metadata: metadata,
	}
	if err := h.store.CreateRequest(pendingReq); err != nil {
		h.logger.Warn("failed to insert pending request", "error", err)
		pendingReq.ID = ""
	}

	reqCtx := &RequestContext{
		ProjectID:    project.ID,
		APIKeyID:     apiKey.ID,
		OAuthGrantID: oauthGrantID,
		UserID:       apiKey.CreatedBy,
		Model:        reqShape.Model,
		ModelRef:     ModelFromContext(r.Context()),
		IsStream:     false,
		RequestKind:  types.KindAnthropicCountTokens,
		TraceID:      traceID,
		TraceSource:  TraceSourceFromContext(r.Context()),
		SessionID:    traceID,
		ClientIP:     r.RemoteAddr,
		Policy:       policy,
		APIKey:       apiKey,
		Project:      project,
		RequestID:    pendingReq.ID,
		// Explicit: count_tokens is a high-frequency editor probe; never log
		// full bodies regardless of model publisher.
		HttpLogEnabled: false,
	}
	h.executor.Execute(w, r, reqCtx)
}
```

Key differences from `handleProxyRequest`:
- `IsStream: false` (count_tokens never streams).
- `RequestKind: types.KindAnthropicCountTokens`.
- `HttpLogEnabled: false` set unconditionally (overrides the publisher-based gate).
- The `cch_status`/`fingerprint_status` metadata block is **omitted** — count_tokens bodies don't carry the billing header.

- [ ] **Step 4: Verify the tests pass**

```bash
go test ./internal/proxy/ -run TestCountTokens_ -v
```

Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/handler.go internal/proxy/handler_count_tokens_test.go
git commit -m "refactor(proxy): delegate HandleCountTokens to executor.Execute"
```

### Task 10: Delete `ingressForProviders`

**Files:**
- Modify: `internal/proxy/handler.go:461-476` (delete function)

- [ ] **Step 1: Confirm there are no remaining callers**

```bash
grep -rn "ingressForProviders" --include="*.go" .
```

Expected: only the function definition itself.

- [ ] **Step 2: Delete the function**

Remove lines 461-476.

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/handler.go
git commit -m "refactor(proxy): remove dead ingressForProviders helper"
```

---

## Phase 6: Admin API

### Task 11: Validate `request_kinds` on POST/PUT route handlers

**Files:**
- Modify: `internal/admin/handle_routing_routes.go`
  - `handleCreateRoutingRoute` (lines 28-73)
  - `handleUpdateRoutingRoute` (lines 75-121)
- Test: `internal/admin/handle_routing_routes_test.go` (extend or create)

- [ ] **Step 1: Write failing tests**

```go
func TestCreateRoutingRoute_RequiresRequestKinds(t *testing.T) {
	h := newTestAdminHandler(t)
	body := `{"model_names":["m"],"upstream_group_id":"g"}`
	rec := postJSON(t, h, "/admin/routing/routes", body)
	if rec.Code != 400 {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "request_kinds") {
		t.Errorf("error should mention request_kinds, got %s", rec.Body.String())
	}
}

func TestCreateRoutingRoute_RejectsUnknownKind(t *testing.T) {
	h := newTestAdminHandler(t)
	body := `{"model_names":["m"],"upstream_group_id":"g","request_kinds":["nope_kind"]}`
	rec := postJSON(t, h, "/admin/routing/routes", body)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "nope_kind") {
		t.Errorf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateRoutingRoute_AcceptsRequestKindsOnly(t *testing.T) {
	h := newTestAdminHandler(t)
	mustSeedRoute(t, h.Store(), "r1")
	body := `{"request_kinds":["openai_responses"]}`
	rec := putJSON(t, h, "/admin/routing/routes/r1", body)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	r := mustGetRoute(t, h.Store(), "r1")
	if !reflect.DeepEqual(r.RequestKinds, []string{"openai_responses"}) {
		t.Errorf("RequestKinds = %v", r.RequestKinds)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/admin/ -run "TestCreateRoutingRoute_|TestUpdateRoutingRoute_" -v
```

Expected: 200 (no validation today) or compilation error if struct fields differ.

- [ ] **Step 3: Add validation to POST**

In `handleCreateRoutingRoute`:

```go
type createReq struct {
	ProjectID       *string           `json:"project_id"`
	ModelNames      []string          `json:"model_names"`
	RequestKinds    []string          `json:"request_kinds"`
	UpstreamGroupID string            `json:"upstream_group_id"`
	MatchPriority   int               `json:"match_priority"`
	Conditions      map[string]string `json:"conditions"`
	Status          string            `json:"status"`
}
// ... after decode ...
if len(in.RequestKinds) == 0 {
	writeError(w, 400, "request_kinds is required and must be non-empty")
	return
}
for _, k := range in.RequestKinds {
	if !types.IsValidRequestKind(k) {
		writeError(w, 400, fmt.Sprintf("invalid request_kind: %q", k))
		return
	}
}
// ... build types.Route with RequestKinds and call store.CreateRoute ...
```

- [ ] **Step 4: Add validation to PUT**

In `handleUpdateRoutingRoute`, extend the updatable-field allowlist to include `"request_kinds"`. When the field is present in the request body, run the same validity check before forwarding to `store.UpdateRoute`.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
go test ./internal/admin/ -run "TestCreateRoutingRoute_|TestUpdateRoutingRoute_" -v
```

Expected: 3 PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/admin/handle_routing_routes.go internal/admin/handle_routing_routes_test.go
git commit -m "feat(admin): validate request_kinds on route POST/PUT"
```

### Task 12: New `GET /admin/routing/request-kinds` endpoint

**Files:**
- Modify: `internal/admin/handle_routing_routes.go` (add handler)
- Modify: `internal/admin/routes.go` (mount; per the existing routing block at lines 276-282)
- Test: `internal/admin/handle_routing_routes_test.go` (extend)

- [ ] **Step 1: Write the failing test**

```go
func TestListRequestKinds_ReturnsAllConstants(t *testing.T) {
	h := newTestAdminHandler(t)
	rec := getJSON(t, h, "/admin/routing/request-kinds")
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var resp struct{ Data []string `json:"data"` }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !reflect.DeepEqual(resp.Data, types.AllRequestKinds) {
		t.Errorf("Data = %v, want %v", resp.Data, types.AllRequestKinds)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./internal/admin/ -run TestListRequestKinds_ -v
```

Expected: 404.

- [ ] **Step 3: Add the handler**

In `internal/admin/handle_routing_routes.go`:

```go
func handleListRequestKinds(_ *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": types.AllRequestKinds})
	}
}
```

- [ ] **Step 4: Mount the route**

In `internal/admin/routes.go` inside the `r.Route("/routing", ...)` block (around line 276), add:

```go
r.Get("/request-kinds", handleListRequestKinds(st))
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./internal/admin/ -run TestListRequestKinds_ -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/admin/handle_routing_routes.go internal/admin/routes.go internal/admin/handle_routing_routes_test.go
git commit -m "feat(admin): expose request-kinds enumeration endpoint"
```

---

## Phase 7: Dashboard

### Task 13: Show `request_kinds` in the routes table

**Files:**
- Modify: `dashboard/src/api/types.ts` (add field on `RoutingRoute` type)
- Modify: `dashboard/src/api/upstreams.ts:166-197` (no schema change unless types are colocated)
- Modify: `dashboard/src/pages/admin/RoutesPage.tsx:143-222` (add column)

- [ ] **Step 1: Add the field to the TS type**

In whichever file declares `RoutingRoute`, add:

```ts
export interface RoutingRoute {
  // ... existing ...
  request_kinds: string[];
}
```

- [ ] **Step 2: Add the table column**

In `RoutesPage.tsx` add an "Endpoints" column rendering `route.request_kinds.join(', ')` as comma-joined chips (or a single text cell — whichever matches the table component's existing chip style).

- [ ] **Step 3: Manual smoke**

```bash
cd dashboard && npm run dev
```

Open the routes page; confirm the new column renders for each existing route (post-migration backfill values).

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/api/types.ts dashboard/src/pages/admin/RoutesPage.tsx
git commit -m "feat(dashboard): show request_kinds in routes table"
```

### Task 14: Multi-select for `request_kinds` in the route create/edit modal

**Files:**
- Modify: `dashboard/src/api/upstreams.ts` (add `useRequestKinds` hook fetching `/admin/routing/request-kinds`)
- Modify: `dashboard/src/pages/admin/RoutesPage.tsx` (form fields)

- [ ] **Step 1: Add the data hook**

```ts
export function useRequestKinds() {
  return useQuery({
    queryKey: ['admin', 'routing', 'request-kinds'],
    queryFn: async () => {
      const resp = await api.get<{ data: string[] }>('/admin/routing/request-kinds');
      return resp.data;
    },
  });
}
```

- [ ] **Step 2: Add the multi-select to the form**

In `RoutesPage.tsx`'s create/edit form, add a `<MultiSelect>` (or whatever the project's existing multi-select component is) bound to `request_kinds`. Default value for new rows: `['anthropic_messages']`. Include client-side required-non-empty validation that mirrors the server-side check.

- [ ] **Step 3: Manual smoke**

Create a route via the dashboard with two kinds; confirm the POST body includes `request_kinds` and the row renders with both chips after creation. Edit the row; toggle a kind off; save; verify update persists.

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/api/upstreams.ts dashboard/src/pages/admin/RoutesPage.tsx
git commit -m "feat(dashboard): edit request_kinds in route create/edit modal"
```

### Task 15: Soft warning for kind/group provider mismatch

**Files:**
- Modify: `dashboard/src/pages/admin/RoutesPage.tsx`

- [ ] **Step 1: Compute the warning client-side**

In the form, when both `request_kinds` and `upstream_group_id` are selected, look up the chosen group's member providers from the already-fetched groups list. If the kinds chosen don't intersect the providers' typical kinds (use the same inference table as the migration), render a yellow inline note: "The selected upstream group's providers don't typically serve <kind>. The route will still save."

- [ ] **Step 2: Manual smoke**

Pick `openai_responses` with an Anthropic-only group. Confirm the warning appears. Toggle to `anthropic_messages`. Confirm the warning disappears.

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/pages/admin/RoutesPage.tsx
git commit -m "feat(dashboard): warn on kind/group provider mismatch"
```

---

## Phase 8: End-to-end smoke

### Task 16: Final smoke run before opening the PR

- [ ] **Step 1: Full test suite**

```bash
go test ./... 2>&1 | tail -20
cd dashboard && npm test 2>&1 | tail -5
```

Expected: PASS in every package.

- [ ] **Step 2: Spin up the server against a local DB and run the spec's manual smoke checklist (§Testing → Manual smoke)**

1. Existing `/v1/messages` traffic still flows.
2. New `anthropic_count_tokens` route to a separate group → `/v1/messages/count_tokens` lands there; `/v1/messages` unchanged.
3. `/v1/responses` against a model with only an `anthropic_messages` route → 404 with `no route configured for model <m> on endpoint openai_responses`.

- [ ] **Step 3: Open a single PR**

The deploy must be atomic per spec §Rollout. Push the branch and open one PR titled `feat: route request_kind dimension` with the spec linked from the description and a short summary of the migration's NOTICE behavior so the on-call operator knows to scan the migration log for cross-family groups.

```bash
git push -u origin feat/route-request-kind
gh pr create --title "feat: route request_kind dimension" --body "$(cat <<'EOF'
## Summary
- Add `request_kinds TEXT[]` to `routes`; backfill from per-group provider inference (migration 021).
- `Router.Match` now keys on `(project_id, model, request_kind)`.
- `RequestContext.AllowedProviders` removed; each handler sets `RequestKind`.
- `HandleCountTokens` rewritten to delegate to `executor.Execute` (gains retry, request log row, observability) with `HttpLogEnabled=false` to avoid request-log spam.
- Admin API: `request_kinds` now required on route POST; new `GET /admin/routing/request-kinds`.
- Dashboard: routes table gains an "Endpoints" column; create/edit modal gains a multi-select.

## Migration impact
Run `021_route_request_kinds.sql` — backfill is provider-inferred, no operator action required for typical Anthropic-only / pure-OpenAI / pure-Google groups. Cross-family upstream groups emit a `RAISE NOTICE` per route during migration; operators must split those groups and assign explicit `request_kinds`.

## Test plan
- [ ] `go test ./...` green
- [ ] Migration test asserts the inference table from spec §Schema
- [ ] Manual: `/v1/messages`, `/v1/messages/count_tokens`, `/v1/responses` per spec §Testing → Manual smoke

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-Review

Spec coverage check (every section in the spec maps to a task above):

- §Request kinds (table of 5 kinds) → Task 1
- §Schema migration + backfill SQL → Task 2
- §Code changes → `internal/types/` → Tasks 1, 3
- §Code changes → `internal/store/routes.go` → Task 4
- §Code changes → `internal/proxy/router_engine.go` → Task 5
- §Code changes → `internal/proxy/executor.go` (`AllowedProviders` removal + Match call) → Task 6
- §Code changes → `internal/proxy/executor.go` (skip ParseResponse for count_tokens) → Task 7
- §Code changes → `internal/proxy/handler.go` (handlers + ingressForProviders deletion + HandleCountTokens rewrite + HttpLogEnabled=false) → Tasks 8, 9, 10
- §Code changes → `internal/admin/handle_routing_routes.go` (POST/PUT validation + new GET /request-kinds) → Tasks 11, 12
- §Dashboard (column, multi-select, soft warning) → Tasks 13, 14, 15
- §Behavior changes visible to users — covered by the unit/integration assertions across Tasks 5, 7, 9, 11
- §Testing → Unit/integration → Tasks 2, 5, 7, 9, 11, 12
- §Testing → Manual smoke → Task 16
- §Rollout (single atomic PR) → Task 16

No placeholders remain. Type names and function signatures used in later tasks are consistent with their definitions in earlier tasks (e.g., `KindAnthropicCountTokens`, `RequestKind`, `Match(projectID, model, kind)`).
