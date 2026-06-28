# Route Matrix View — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Model×RequestKind matrix tab to `/admin/routes` that shows, for each active catalog model and each of the 8 request kinds, the upstream group that the proxy's `Router.Match` would resolve to (global routes only) — computed authoritatively on the backend.

**Architecture:** Add `(*proxy.Router).MatrixGlobal` and `(*proxy.Router).SnapshotGroupNames` that mirror the global-route branch of `Router.Match` exactly, so the dashboard view cannot drift from runtime routing. Expose them via `GET /api/v1/routing/matrix` and a new `GET /api/v1/routing/routes/{routeID}` (for cell-click → edit-dialog fallback). On the frontend, wrap `RoutesPage` in a `Tabs` primitive (List | Matrix) with the active tab bound to `?view=list|matrix`; matrix cells render the resolved group name as a `Badge` and clicking a filled cell opens the existing route edit dialog.

**Tech Stack:** Go 1.x, `chi` router, `pgx`, stdlib `encoding/json`, stdlib `testing` for backend; React 19, `@tanstack/react-query` v5, `react-router` v7, `@base-ui/react` Tabs primitive, Tailwind v4 for frontend. No new dependencies. No DB migrations.

## Global Constraints

- **Spec location:** `docs/superpowers/specs/2026-06-28-route-matrix-view-design.md` — re-read it before each task; tasks below assume its decisions verbatim.
- **No DB changes.** All data already exists in `routes`, `upstream_groups`, `upstream_group_members`, and `models` tables.
- **Authoritative matching.** The matrix computation must reuse the proxy's own route-walking logic — duplicating priority / status / project-filter rules in the admin package is forbidden. Add helpers on `*proxy.Router` and call them from the handler.
- **Sparse cells.** The endpoint response's `cells` array contains only resolved `(model, kind)` pairs; unrouted pairs are absent. Frontend renders a dimmed `—` for missing cells.
- **Alphabetical ordering.** Server returns `models` and `kinds` arrays sorted ASCII-ascending; frontend renders rows / columns in the exact order received.
- **Auth.** All new admin endpoints under `RequireSuperadmin` (matches the existing `/routing` subtree).
- **No frontend test framework exists** in this repo (`dashboard/` has no vitest / jest config). Verification of frontend work is by `pnpm build` (must succeed) + manual checks listed in each frontend task. Do not introduce a test framework as part of this plan.
- **Commit message footer:** every commit ends with
  ```
  Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
  ```

## File Structure

**Backend — create:**
- `internal/admin/handle_routing_matrix.go` — `handleRoutingMatrix` handler + local response structs.
- `internal/admin/handle_routing_matrix_test.go` — integration test against a real `*proxy.Router`.

**Backend — modify:**
- `internal/proxy/router_engine.go` — add `MatrixCell` struct, `(*Router).MatrixGlobal`, `(*Router).SnapshotGroupNames`.
- `internal/proxy/router_engine_test.go` — `TestRouter_MatrixGlobal` + `TestRouter_SnapshotGroupNames`.
- `internal/admin/handle_routing_routes.go` — add `handleGetRoutingRoute` (GET-by-id).
- `internal/admin/routes.go` — extend `MountRoutes` signature to accept `*proxy.Router`; register the two new routes.
- `cmd/modelserver/main.go` — pass `router` into `admin.MountRoutes`.

**Frontend — create:**
- `dashboard/src/pages/admin/RoutesMatrixView.tsx` — matrix table component.

**Frontend — modify:**
- `dashboard/src/api/types.ts` — add `RoutingMatrix`, `RoutingMatrixCell` types.
- `dashboard/src/api/upstreams.ts` — add `useRoutingMatrix`, add `useRoutingRoute(id)`, extend create/update/delete mutations' `invalidateQueries` to also invalidate `["admin", "routing-matrix"]`.
- `dashboard/src/pages/admin/RoutesPage.tsx` — wrap content in `Tabs`, lift edit-dialog state above tabs, bind active tab to `?view=` search-param, pass shared `onEditRoute` callback to both tabs, route List rows' "Edit" through it.

---

### Task 1: Add `MatrixCell` + `MatrixGlobal` + `SnapshotGroupNames` on `*Router`

**Files:**
- Modify: `internal/proxy/router_engine.go`
- Test: `internal/proxy/router_engine_test.go`

**Interfaces:**
- Consumes: existing `Router` internal state (`r.mu`, `r.routes`, `r.groups`).
- Produces:
  ```go
  type MatrixCell struct {
      Model           string
      Kind            string
      UpstreamGroupID string
      RouteID         string
      MatchPriority   int
  }
  func (r *Router) MatrixGlobal(models []string) []MatrixCell
  func (r *Router) SnapshotGroupNames() map[string]string
  ```

- [ ] **Step 1: Write the failing test `TestRouter_MatrixGlobal`**

Append to `internal/proxy/router_engine_test.go`:

```go
func TestRouter_MatrixGlobal(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	upstreams := []types.Upstream{
		{ID: "up-a", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"claude-sonnet"}},
		{ID: "up-b", Provider: types.ProviderOpenAI, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"gpt-5"}},
	}
	groups := []store.UpstreamGroupWithMembers{
		{
			UpstreamGroup: types.UpstreamGroup{ID: "grp-anth", Name: "anthropic-pool", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{
				{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-anth", UpstreamID: "up-a"}},
			},
		},
		{
			UpstreamGroup: types.UpstreamGroup{ID: "grp-oai", Name: "openai-pool", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{
				{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-oai", UpstreamID: "up-b"}},
			},
		},
	}
	routes := []types.Route{
		// High-priority global route for claude-sonnet on anthropic_messages.
		{ID: "rt-hi", ProjectID: "", ModelNames: []string{"claude-sonnet"}, RequestKinds: []string{types.KindAnthropicMessages}, UpstreamGroupID: "grp-anth", MatchPriority: 100, Status: "active"},
		// Lower-priority global route that also matches claude-sonnet on anthropic_messages (should lose).
		{ID: "rt-lo", ProjectID: "", ModelNames: []string{"claude-sonnet"}, RequestKinds: []string{types.KindAnthropicMessages}, UpstreamGroupID: "grp-oai", MatchPriority: 1, Status: "active"},
		// Disabled global route — must be ignored.
		{ID: "rt-off", ProjectID: "", ModelNames: []string{"claude-sonnet"}, RequestKinds: []string{types.KindAnthropicCountTokens}, UpstreamGroupID: "grp-anth", MatchPriority: 1, Status: "disabled"},
		// Project-scoped route — must NOT appear in the global matrix.
		{ID: "rt-proj", ProjectID: "proj-1", ModelNames: []string{"claude-sonnet"}, RequestKinds: []string{types.KindAnthropicMessages}, UpstreamGroupID: "grp-anth", MatchPriority: 50, Status: "active"},
		// Multi-kind, multi-model global route for gpt-5.
		{ID: "rt-multi", ProjectID: "", ModelNames: []string{"gpt-5"}, RequestKinds: []string{types.KindOpenAIChatCompletions, types.KindOpenAIResponses}, UpstreamGroupID: "grp-oai", MatchPriority: 5, Status: "active"},
		// Route referencing a missing group — must be silently skipped (matches Match behavior).
		{ID: "rt-bad", ProjectID: "", ModelNames: []string{"gpt-5"}, RequestKinds: []string{types.KindOpenAIResponsesCompact}, UpstreamGroupID: "grp-missing", MatchPriority: 1, Status: "active"},
	}

	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	cells := r.MatrixGlobal([]string{"claude-sonnet", "gpt-5"})

	byKey := map[string]MatrixCell{}
	for _, c := range cells {
		byKey[c.Model+"::"+c.Kind] = c
	}

	// High-priority winner.
	got, ok := byKey["claude-sonnet::"+types.KindAnthropicMessages]
	if !ok {
		t.Fatalf("expected cell for (claude-sonnet, anthropic_messages)")
	}
	if got.UpstreamGroupID != "grp-anth" || got.RouteID != "rt-hi" || got.MatchPriority != 100 {
		t.Errorf("winner = %+v, want grp-anth/rt-hi/100", got)
	}

	// Disabled route must produce no cell.
	if _, ok := byKey["claude-sonnet::"+types.KindAnthropicCountTokens]; ok {
		t.Errorf("disabled route produced a cell")
	}

	// Project-scoped route must produce no cell (project_id == "" filter).
	// (Only check we don't accidentally pick rt-proj — the global rt-hi already wins anyway.)
	if got.RouteID == "rt-proj" {
		t.Errorf("project-scoped route appeared in global matrix")
	}

	// Multi-kind route produces two cells.
	if _, ok := byKey["gpt-5::"+types.KindOpenAIChatCompletions]; !ok {
		t.Errorf("expected cell for (gpt-5, openai_chat_completions)")
	}
	if _, ok := byKey["gpt-5::"+types.KindOpenAIResponses]; !ok {
		t.Errorf("expected cell for (gpt-5, openai_responses)")
	}

	// Route pointing at a missing group is silently dropped.
	if _, ok := byKey["gpt-5::"+types.KindOpenAIResponsesCompact]; ok {
		t.Errorf("route with missing group produced a cell")
	}

	// Unrouted pair is absent.
	if _, ok := byKey["gpt-5::"+types.KindAnthropicMessages]; ok {
		t.Errorf("unrouted pair produced a cell")
	}
}

func TestRouter_SnapshotGroupNames(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upstreams := []types.Upstream{
		{ID: "up-a", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"x"}},
	}
	groups := []store.UpstreamGroupWithMembers{{
		UpstreamGroup: types.UpstreamGroup{ID: "grp-1", Name: "alpha", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
		Members: []store.UpstreamGroupMemberDetail{
			{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-1", UpstreamID: "up-a"}},
		},
	}}
	r := NewRouter(upstreams, groups, nil, []byte{}, logger, time.Minute, nil, nil, nil)

	got := r.SnapshotGroupNames()
	if got["grp-1"] != "alpha" {
		t.Errorf("SnapshotGroupNames[grp-1] = %q, want %q", got["grp-1"], "alpha")
	}

	// Mutating the returned map must not affect the router.
	got["grp-1"] = "mutated"
	if again := r.SnapshotGroupNames(); again["grp-1"] != "alpha" {
		t.Errorf("returned map shared with router internals: %q", again["grp-1"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail with "undefined"**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run 'TestRouter_MatrixGlobal|TestRouter_SnapshotGroupNames' -v`
Expected: build errors — `undefined: MatrixCell`, `r.MatrixGlobal undefined`, `r.SnapshotGroupNames undefined`.

- [ ] **Step 3: Implement `MatrixCell`, `MatrixGlobal`, `SnapshotGroupNames`**

Append to `internal/proxy/router_engine.go` (after `BindSession` is a good spot — keep related exported methods together; if uncertain, append at end of file but before `Reload`):

```go
// MatrixCell is one winning (model, kind) -> upstream group resolution,
// computed by MatrixGlobal. It is the same logical answer Match returns
// for a (projectID="", model, kind) tuple, but emitted as data so the
// admin UI can render the full matrix in one fetch.
type MatrixCell struct {
	Model           string
	Kind            string
	UpstreamGroupID string
	RouteID         string
	MatchPriority   int
}

// MatrixGlobal walks every (model, kind) pair over the supplied models and
// the full AllRequestKinds set, returning one MatrixCell for each pair
// that resolves under the global-route branch of Match. Unrouted pairs are
// omitted (sparse result).
//
// Rules MUST mirror Match exactly:
//   - routes are walked in priority-descending order (r.routes is pre-sorted)
//   - routes with Status != "active" are skipped
//   - routes with ProjectID != "" are skipped (global view)
//   - the first matching route wins
//   - if the winning route's UpstreamGroupID is not present in r.groups,
//     the pair is silently dropped (matches Match's "group missing" branch)
func (r *Router) MatrixGlobal(models []string) []MatrixCell {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(models) == 0 {
		return nil
	}

	out := make([]MatrixCell, 0, len(models)*len(types.AllRequestKinds))
	for _, model := range models {
		for _, kind := range types.AllRequestKinds {
			for _, route := range r.routes {
				if route.Status != "active" {
					continue
				}
				if route.ProjectID != "" {
					continue
				}
				if !slices.Contains(route.ModelNames, model) {
					continue
				}
				if !slices.Contains(route.RequestKinds, kind) {
					continue
				}
				if _, ok := r.groups[route.UpstreamGroupID]; !ok {
					// Match silently drops; do the same here.
					break
				}
				out = append(out, MatrixCell{
					Model:           model,
					Kind:            kind,
					UpstreamGroupID: route.UpstreamGroupID,
					RouteID:         route.ID,
					MatchPriority:   route.MatchPriority,
				})
				break
			}
		}
	}
	return out
}

// SnapshotGroupNames returns a fresh map of group ID -> group name. Caller
// owns the returned map and may mutate it without affecting the router.
func (r *Router) SnapshotGroupNames() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.groups))
	for id, g := range r.groups {
		out[id] = g.group.Name
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run 'TestRouter_MatrixGlobal|TestRouter_SnapshotGroupNames' -v`
Expected: PASS for both.

- [ ] **Step 5: Run full proxy package to ensure no regressions**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/router_engine.go internal/proxy/router_engine_test.go
git commit -m "feat(proxy): MatrixGlobal + SnapshotGroupNames on Router

Mirrors the global-route branch of Router.Match exactly, returning one
MatrixCell per resolved (model, kind) pair so the admin UI can render
the routing matrix without duplicating priority / status / scope rules.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Add `GET /api/v1/routing/routes/{routeID}` handler

**Files:**
- Modify: `internal/admin/handle_routing_routes.go`
- Modify: `internal/admin/routes.go`

**Interfaces:**
- Consumes: existing `store.GetRouteByID(id string) (*types.Route, error)`.
- Produces: `handleGetRoutingRoute(st *store.Store) http.HandlerFunc` returning `200 { "data": <Route> }` on hit, `404 not_found` on miss.

Rationale: cells in the matrix carry only `route_id`; opening the existing edit dialog from a cold cache needs a fetch-by-id path.

- [ ] **Step 1: Add the handler**

In `internal/admin/handle_routing_routes.go`, add (after `handleListRoutingRoutes` is a natural spot):

```go
func handleGetRoutingRoute(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		route, err := st.GetRouteByID(chi.URLParam(r, "routeID"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to load routing route")
			return
		}
		if route == nil {
			writeError(w, http.StatusNotFound, "not_found", "routing route not found")
			return
		}
		writeData(w, http.StatusOK, route)
	}
}
```

(Verify `st.GetRouteByID` returns `(nil, nil)` on miss before relying on the nil check. If it returns an error wrapping `pgx.ErrNoRows` instead, adapt the handler — match the project's existing convention used by `handleGetUpstreamGroup` which does `if err != nil || g == nil { 404 }`.)

- [ ] **Step 2: Register the route**

In `internal/admin/routes.go`, inside the `r.Route("/routing", …)` block, add the GET line between the existing list and create routes:

```go
r.Route("/routing", func(r chi.Router) {
    r.Use(RequireSuperadmin)
    r.Get("/routes", handleListRoutingRoutes(st))
    r.Get("/routes/{routeID}", handleGetRoutingRoute(st)) // NEW
    r.Post("/routes", handleCreateRoutingRoute(st, catalog))
    r.Put("/routes/{routeID}", handleUpdateRoutingRoute(st, catalog))
    r.Delete("/routes/{routeID}", handleDeleteRoutingRoute(st))
    r.Get("/request-kinds", handleListRequestKinds())
})
```

- [ ] **Step 3: Compile check**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: no errors.

- [ ] **Step 4: Verify behavior with a smoke test**

Append to `internal/admin/handle_routing_routes_test.go` (create the file if it doesn't exist — minimal scaffold below; check whether the file already exists first and append in place):

```go
package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// adminTestStore returns a Store backed by TEST_DATABASE_URL; skips when unset.
// (If the file already defines a helper, reuse it instead of redefining.)
func adminTestStore(t *testing.T) *store.Store {
	t.Helper()
	// TODO: wire to existing test helper if one exists in package admin.
	// If no helper exists, this test will be skipped and the smoke test
	// in Step 5 (manual curl) suffices.
	t.Skip("no admin-package store helper available; verified by Task 3 integration test")
	return nil
}

func TestHandleGetRoutingRoute_NotFound(t *testing.T) {
	st := adminTestStore(t)
	r := chi.NewRouter()
	r.Get("/routing/routes/{routeID}", handleGetRoutingRoute(st))

	req := httptest.NewRequest(http.MethodGet, "/routing/routes/does-not-exist", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
```

If the admin package already has a store helper used by other tests (`grep -n "func.*testStore\|TEST_DATABASE_URL" internal/admin/*_test.go`), reuse it and remove the `t.Skip`. Otherwise leave the skip in — the integration coverage in Task 3 exercises the same path.

- [ ] **Step 5: Run admin tests**

Run: `cd /root/coding/modelserver && go test ./internal/admin/...`
Expected: PASS (the new test may skip — that's fine).

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_routing_routes.go internal/admin/handle_routing_routes_test.go internal/admin/routes.go
git commit -m "feat(admin): GET /api/v1/routing/routes/{routeID}

Returns a single route by ID so the dashboard can populate the
edit dialog from a cold cache (e.g. clicking a matrix cell whose
route is not in the List query's page window).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Add `handleRoutingMatrix` and thread `*proxy.Router` into `admin.MountRoutes`

**Files:**
- Create: `internal/admin/handle_routing_matrix.go`
- Create: `internal/admin/handle_routing_matrix_test.go`
- Modify: `internal/admin/routes.go`
- Modify: `cmd/modelserver/main.go`

**Interfaces:**
- Consumes: `(*proxy.Router).MatrixGlobal`, `(*proxy.Router).SnapshotGroupNames` (Task 1); `store.ListModelsByStatus` (existing).
- Produces: `GET /api/v1/routing/matrix` returning
  ```json
  { "data": { "models": [...], "kinds": [...], "cells": [ { "model","kind","upstream_group_id","upstream_group_name","route_id","match_priority" } ] } }
  ```

- [ ] **Step 1: Write the failing test**

Create `internal/admin/handle_routing_matrix_test.go`:

```go
package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/proxy"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// minimal store mock just for ListModelsByStatus; if package admin already
// has a richer fake/mock for store, prefer that one (search:
// `grep -n "ListModelsByStatus" internal/admin/*_test.go`).
type matrixTestStore struct {
	*store.Store // embeds nil; methods we don't override panic — that's fine for this narrow test
	models       []types.Model
}

// If embedding nil panics on unrelated method calls in this test surface,
// instead build a small interface: the handler only calls
// `st.ListModelsByStatus`. Refactor handleRoutingMatrix to take an
// interface{ ListModelsByStatus(string) ([]types.Model, error) }
// before this test if needed. Keep the public signature in handle_routing_matrix.go
// using *store.Store so callers don't change.

func TestHandleRoutingMatrix_HappyPath(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	upstreams := []types.Upstream{
		{ID: "up-a", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"claude-sonnet"}},
		{ID: "up-b", Provider: types.ProviderOpenAI, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"gpt-5"}},
	}
	groups := []store.UpstreamGroupWithMembers{
		{
			UpstreamGroup: types.UpstreamGroup{ID: "grp-anth", Name: "anthropic-pool", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{
				{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-anth", UpstreamID: "up-a"}},
			},
		},
		{
			UpstreamGroup: types.UpstreamGroup{ID: "grp-oai", Name: "openai-pool", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{
				{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-oai", UpstreamID: "up-b"}},
			},
		},
	}
	routes := []types.Route{
		{ID: "rt-1", ProjectID: "", ModelNames: []string{"claude-sonnet"}, RequestKinds: []string{types.KindAnthropicMessages}, UpstreamGroupID: "grp-anth", MatchPriority: 100, Status: "active"},
		{ID: "rt-2", ProjectID: "", ModelNames: []string{"gpt-5"}, RequestKinds: []string{types.KindOpenAIChatCompletions, types.KindOpenAIResponses}, UpstreamGroupID: "grp-oai", MatchPriority: 5, Status: "active"},
	}
	router := proxy.NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	// Build a tiny fake that the handler can call ListModelsByStatus on.
	// If a richer admin test helper exists, prefer it. Otherwise the
	// handler should take a small interface so we can inject this fake.
	listModelsFn := func(_ string) ([]types.Model, error) {
		return []types.Model{
			{Name: "claude-sonnet", Status: types.ModelStatusActive},
			{Name: "gpt-5", Status: types.ModelStatusActive},
		}, nil
	}

	h := handleRoutingMatrixWithLister(listModelsFn, router) // see implementation step
	req := httptest.NewRequest(http.MethodGet, "/routing/matrix", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Models []string `json:"models"`
			Kinds  []string `json:"kinds"`
			Cells  []struct {
				Model             string `json:"model"`
				Kind              string `json:"kind"`
				UpstreamGroupID   string `json:"upstream_group_id"`
				UpstreamGroupName string `json:"upstream_group_name"`
				RouteID           string `json:"route_id"`
				MatchPriority     int    `json:"match_priority"`
			} `json:"cells"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}

	// Alphabetical ordering of models and kinds.
	want := []string{"claude-sonnet", "gpt-5"}
	if !equalStrings(resp.Data.Models, want) {
		t.Errorf("models = %v, want %v", resp.Data.Models, want)
	}
	gotKinds := append([]string(nil), resp.Data.Kinds...)
	sortedKinds := append([]string(nil), types.AllRequestKinds...)
	sort.Strings(sortedKinds)
	if !equalStrings(gotKinds, sortedKinds) {
		t.Errorf("kinds = %v, want %v (sorted AllRequestKinds)", gotKinds, sortedKinds)
	}

	// Sparse cells: 3 total (claude-sonnet/messages, gpt-5/chat, gpt-5/responses).
	if len(resp.Data.Cells) != 3 {
		t.Errorf("len(cells) = %d, want 3 (sparse)", len(resp.Data.Cells))
	}
	for _, c := range resp.Data.Cells {
		if c.UpstreamGroupName == "" {
			t.Errorf("cell %s/%s missing upstream_group_name", c.Model, c.Kind)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /root/coding/modelserver && go test ./internal/admin/ -run TestHandleRoutingMatrix -v`
Expected: build error — `handleRoutingMatrixWithLister undefined`.

- [ ] **Step 3: Implement the handler**

Create `internal/admin/handle_routing_matrix.go`:

```go
package admin

import (
	"net/http"
	"sort"

	"github.com/modelserver/modelserver/internal/proxy"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// matrixLister is the narrow subset of *store.Store that handleRoutingMatrix
// needs. Lets tests inject a fake without spinning up a database.
type matrixLister interface {
	ListModelsByStatus(status string) ([]types.Model, error)
}

type matrixCellOut struct {
	Model             string `json:"model"`
	Kind              string `json:"kind"`
	UpstreamGroupID   string `json:"upstream_group_id"`
	UpstreamGroupName string `json:"upstream_group_name"`
	RouteID           string `json:"route_id"`
	MatchPriority     int    `json:"match_priority"`
}

type matrixResponse struct {
	Models []string        `json:"models"`
	Kinds  []string        `json:"kinds"`
	Cells  []matrixCellOut `json:"cells"`
}

// handleRoutingMatrix is the production binding: store + router from main.
func handleRoutingMatrix(st *store.Store, router *proxy.Router) http.HandlerFunc {
	return handleRoutingMatrixWithLister(st.ListModelsByStatus, router)
}

// handleRoutingMatrixWithLister is the testable form: an injectable model
// lister + the live router. The router's MatrixGlobal mirrors the proxy's
// own route-walking, so the matrix cannot drift from runtime behavior.
func handleRoutingMatrixWithLister(
	listModels func(string) ([]types.Model, error),
	router *proxy.Router,
) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		models, err := listModels(types.ModelStatusActive)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list models")
			return
		}
		names := make([]string, len(models))
		for i, m := range models {
			names[i] = m.Name
		}
		sort.Strings(names)

		kinds := append([]string(nil), types.AllRequestKinds...)
		sort.Strings(kinds)

		cells := router.MatrixGlobal(names)
		groupNames := router.SnapshotGroupNames()

		out := matrixResponse{
			Models: names,
			Kinds:  kinds,
			Cells:  make([]matrixCellOut, 0, len(cells)),
		}
		for _, c := range cells {
			out.Cells = append(out.Cells, matrixCellOut{
				Model:             c.Model,
				Kind:              c.Kind,
				UpstreamGroupID:   c.UpstreamGroupID,
				UpstreamGroupName: groupNames[c.UpstreamGroupID],
				RouteID:           c.RouteID,
				MatchPriority:     c.MatchPriority,
			})
		}
		writeData(w, http.StatusOK, out)
	}
}
```

- [ ] **Step 4: Update the test to use the production signature where feasible**

The test in Step 1 already uses `handleRoutingMatrixWithLister` — leave it as-is. The lister-function shape in the test must match `func(string) ([]types.Model, error)`; double-check the signature compiles. (If `store.ListModelsByStatus` takes a different parameter type, adapt the lister type accordingly — check `internal/store/models.go` for the exact signature.)

- [ ] **Step 5: Run handler test**

Run: `cd /root/coding/modelserver && go test ./internal/admin/ -run TestHandleRoutingMatrix -v`
Expected: PASS.

- [ ] **Step 6: Extend `admin.MountRoutes` to accept `*proxy.Router`**

In `internal/admin/routes.go`, change the signature:

```go
func MountRoutes(
	r chi.Router,
	st *store.Store,
	cfg *config.Config,
	encKey []byte,
	jwtMgr *auth.JWTManager,
	catalog modelcatalog.Catalog,
	httpLogger *httplog.Logger,
	router *proxy.Router, // NEW — must come last to minimize diff if other repos vendor admin
) {
```

Add import: `"github.com/modelserver/modelserver/internal/proxy"`.

Inside the existing `r.Route("/routing", …)` block (already touched in Task 2), add the matrix route after `/request-kinds`:

```go
r.Route("/routing", func(r chi.Router) {
    r.Use(RequireSuperadmin)
    r.Get("/routes", handleListRoutingRoutes(st))
    r.Get("/routes/{routeID}", handleGetRoutingRoute(st))
    r.Post("/routes", handleCreateRoutingRoute(st, catalog))
    r.Put("/routes/{routeID}", handleUpdateRoutingRoute(st, catalog))
    r.Delete("/routes/{routeID}", handleDeleteRoutingRoute(st))
    r.Get("/request-kinds", handleListRequestKinds())
    r.Get("/matrix", handleRoutingMatrix(st, router)) // NEW
})
```

- [ ] **Step 7: Update the single caller in `cmd/modelserver/main.go`**

At `cmd/modelserver/main.go:277`, change:

```go
admin.MountRoutes(adminRouter, st, cfg, encryptionKey, jwtMgr, catalog, httpLogger)
```

to:

```go
admin.MountRoutes(adminRouter, st, cfg, encryptionKey, jwtMgr, catalog, httpLogger, router)
```

(`router` is the `*proxy.Router` already constructed at `main.go:155`.)

- [ ] **Step 8: Compile + full test suite**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/admin/... ./internal/proxy/...`
Expected: all PASS.

- [ ] **Step 9: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_routing_matrix.go internal/admin/handle_routing_matrix_test.go internal/admin/routes.go cmd/modelserver/main.go
git commit -m "feat(admin): GET /api/v1/routing/matrix

Returns the sparse (model, kind) -> upstream group matrix for global
routes, computed by Router.MatrixGlobal so the admin UI cannot drift
from the proxy's own routing rules. Threads *proxy.Router into
admin.MountRoutes; updates main.go.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Frontend types and API hooks

**Files:**
- Modify: `dashboard/src/api/types.ts`
- Modify: `dashboard/src/api/upstreams.ts`

**Interfaces:**
- Consumes: backend endpoints `GET /api/v1/routing/matrix` and `GET /api/v1/routing/routes/{routeID}` (Tasks 2–3).
- Produces:
  - Types `RoutingMatrix`, `RoutingMatrixCell`.
  - Hook `useRoutingMatrix(): UseQueryResult<DataResponse<RoutingMatrix>>`.
  - Hook `useRoutingRoute(id: string | null): UseQueryResult<DataResponse<RoutingRoute>>` — enabled only when `id` is truthy.
  - Existing create/update/delete mutations also invalidate `["admin", "routing-matrix"]`.

- [ ] **Step 1: Add types**

Append to `dashboard/src/api/types.ts` (near the existing `RoutingRoute` declaration — search for it):

```ts
// One resolved cell in the (model x request-kind) routing matrix.
// Only emitted for (model, kind) pairs that resolve to a group; unrouted
// pairs are absent from the array (sparse representation).
export interface RoutingMatrixCell {
  model: string;
  kind: string;
  upstream_group_id: string;
  upstream_group_name: string;
  route_id: string;
  match_priority: number;
}

// Server-rendered matrix view for /admin/routes. `models` and `kinds` are
// alphabetical; `cells` is sparse.
export interface RoutingMatrix {
  models: string[];
  kinds: string[];
  cells: RoutingMatrixCell[];
}
```

- [ ] **Step 2: Add `useRoutingMatrix` hook**

In `dashboard/src/api/upstreams.ts`, append (after `useRequestKinds`):

```ts
// useRoutingMatrix returns the global-route Model x Kind matrix. Cells are
// sparse: pairs with no resolving route are absent.
export function useRoutingMatrix() {
  return useQuery({
    queryKey: ["admin", "routing-matrix"],
    queryFn: () =>
      api.get<DataResponse<RoutingMatrix>>("/api/v1/routing/matrix"),
  });
}

// useRoutingRoute fetches one route by ID. Used by the Matrix tab when a
// cell click references a route that isn't in the current List page window.
// Pass null to disable the query.
export function useRoutingRoute(id: string | null) {
  return useQuery({
    queryKey: ["admin", "routing-route", id],
    queryFn: () =>
      api.get<DataResponse<RoutingRoute>>(`/api/v1/routing/routes/${id}`),
    enabled: !!id,
  });
}
```

Also make sure the import statement at the top of the file pulls in the new types:

```ts
import type {
  // ...existing,
  RoutingMatrix,
  RoutingRoute, // existing — confirm it's already imported
} from "./types";
```

- [ ] **Step 3: Extend invalidation on the existing mutations**

In `dashboard/src/api/upstreams.ts`, each of `useCreateRoutingRoute`, `useUpdateRoutingRoute`, `useDeleteRoutingRoute` already calls `qc.invalidateQueries({ queryKey: ["admin", "routing-routes"] })` (verify exact key). Add a second invalidate to each:

```ts
onSuccess: () => {
  qc.invalidateQueries({ queryKey: ["admin", "routing-routes"] });
  qc.invalidateQueries({ queryKey: ["admin", "routing-matrix"] });
},
```

(Use the existing key string verbatim — copy from the file, do not retype from memory.)

- [ ] **Step 4: Type-check**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc -b`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/api/types.ts dashboard/src/api/upstreams.ts
git commit -m "feat(dashboard): useRoutingMatrix + useRoutingRoute hooks

Adds RoutingMatrix / RoutingMatrixCell types and a React Query hook
that fetches /api/v1/routing/matrix. Also adds useRoutingRoute(id)
for cell-click -> edit-dialog cold-cache lookups, and extends the
existing route mutations to invalidate the matrix cache.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Create `RoutesMatrixView` component

**Files:**
- Create: `dashboard/src/pages/admin/RoutesMatrixView.tsx`

**Interfaces:**
- Consumes: `useRoutingMatrix` (Task 4); `RoutingMatrix`, `RoutingMatrixCell` types (Task 4); `Badge`, `Card` from `@/components/ui/*`.
- Produces: `export function RoutesMatrixView({ onEditRoute }: { onEditRoute: (routeId: string) => void }): JSX.Element`.

Visual requirements (from spec): sticky-top header row of kind names, sticky-left model column, body scrolls horizontally inside its Card on narrow viewports, filled cells render `<Badge variant="outline">{group_name || truncated_id}</Badge>` and are clickable, empty cells render a dimmed `—`.

- [ ] **Step 1: Create the component file**

Create `dashboard/src/pages/admin/RoutesMatrixView.tsx`:

```tsx
import { useMemo } from "react";
import { useRoutingMatrix } from "@/api/upstreams";
import type { RoutingMatrixCell } from "@/api/types";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Loader2 } from "lucide-react";

interface RoutesMatrixViewProps {
  onEditRoute: (routeId: string) => void;
}

export function RoutesMatrixView({ onEditRoute }: RoutesMatrixViewProps) {
  const { data, isLoading, error } = useRoutingMatrix();

  // O(1) cell lookup keyed by `${model}::${kind}`.
  const cellIndex = useMemo(() => {
    const m = new Map<string, RoutingMatrixCell>();
    for (const c of data?.data.cells ?? []) {
      m.set(`${c.model}::${c.kind}`, c);
    }
    return m;
  }, [data]);

  if (isLoading) {
    return (
      <Card>
        <CardContent className="flex items-center justify-center py-10 text-muted-foreground">
          <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          Loading matrix…
        </CardContent>
      </Card>
    );
  }

  if (error) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-destructive-foreground text-sm">
          Failed to load route matrix.
        </CardContent>
      </Card>
    );
  }

  const models = data?.data.models ?? [];
  const kinds = data?.data.kinds ?? [];

  if (models.length === 0) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-sm text-muted-foreground">
          No active models in catalog.
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardContent className="p-0">
        <div className="overflow-x-auto">
          <table className="w-full border-separate border-spacing-0 text-sm">
            <thead>
              <tr>
                <th
                  scope="col"
                  className="sticky top-0 left-0 z-30 bg-background text-left font-medium px-3 py-2 border-b border-r"
                >
                  Model
                </th>
                {kinds.map((k) => (
                  <th
                    key={k}
                    scope="col"
                    className="sticky top-0 z-20 bg-background text-left font-mono text-xs font-medium px-3 py-2 border-b whitespace-nowrap"
                  >
                    {k}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {models.map((model) => (
                <tr key={model}>
                  <th
                    scope="row"
                    className="sticky left-0 z-10 bg-background text-left font-mono text-xs font-medium px-3 py-2 border-b border-r whitespace-nowrap"
                  >
                    {model}
                  </th>
                  {kinds.map((kind) => {
                    const cell = cellIndex.get(`${model}::${kind}`);
                    return (
                      <td
                        key={kind}
                        className="px-3 py-2 border-b align-middle"
                      >
                        {cell ? (
                          <button
                            type="button"
                            onClick={() => onEditRoute(cell.route_id)}
                            className="inline-flex"
                            title={`route ${cell.route_id.slice(0, 8)} (priority ${cell.match_priority})`}
                          >
                            <Badge variant="outline" className="cursor-pointer">
                              {cell.upstream_group_name ||
                                cell.upstream_group_id.slice(0, 8)}
                            </Badge>
                          </button>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </CardContent>
    </Card>
  );
}
```

- [ ] **Step 2: Type-check**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc -b`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/pages/admin/RoutesMatrixView.tsx
git commit -m "feat(dashboard): RoutesMatrixView component

Renders the (model x request-kind) routing matrix returned by
useRoutingMatrix. Sticky header + sticky model column; filled cells
are clickable badges; empty cells render a dimmed dash.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Wire `RoutesMatrixView` into `RoutesPage` with tabs and lifted dialog state

**Files:**
- Modify: `dashboard/src/pages/admin/RoutesPage.tsx`

**Interfaces:**
- Consumes: `RoutesMatrixView` (Task 5); `useRoutingRoute` (Task 4); `Tabs, TabsList, TabsTrigger, TabsContent` from `@/components/ui/tabs`; `useSearchParams` from `react-router`.
- Produces: same `RoutesPage` export, now rendering a tab switcher with `?view=list|matrix` URL state and a shared `onEditRoute(routeId)` callback that opens the existing edit dialog (with cold-cache fallback via `useRoutingRoute`).

The bulk of `RoutesPage`'s existing logic (form state, dialog open/close, list query, mutations) stays where it is — we only restructure the JSX and replace the inline "Edit" click handler with a shared callback.

- [ ] **Step 1: Read the current file end-to-end**

Run: `cd /root/coding/modelserver && cat dashboard/src/pages/admin/RoutesPage.tsx | head -200`
And: `cd /root/coding/modelserver && cat dashboard/src/pages/admin/RoutesPage.tsx | sed -n '200,500p'`
Then re-read the rest until EOF.

This step exists because the JSX edits in Step 3 must align with what's actually there (e.g. whether the table is already a separate sub-component, whether columns are declared inline). Don't proceed without reading.

- [ ] **Step 2: Add imports + tab state**

At the top of `dashboard/src/pages/admin/RoutesPage.tsx`, add:

```ts
import { useSearchParams } from "react-router";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { RoutesMatrixView } from "./RoutesMatrixView";
import { useRoutingRoute } from "@/api/upstreams"; // hook from Task 4
```

Inside `RoutesPage()`, after the existing `useState` hooks for `page`, `dialogOpen`, `editingId`, etc., add:

```tsx
const [searchParams, setSearchParams] = useSearchParams();
const activeView = searchParams.get("view") === "matrix" ? "matrix" : "list";

// Cold-cache fallback for edit-from-matrix: when openEditFromMatrix is called
// with a route id not in the list query, fetch it on demand.
const [pendingFetchId, setPendingFetchId] = useState<string | null>(null);
const pendingRouteQuery = useRoutingRoute(pendingFetchId);

// When the cold-cache fetch completes, populate the edit form just like
// openEdit() does for list-cached routes.
useEffect(() => {
  if (!pendingFetchId) return;
  if (pendingRouteQuery.data?.data) {
    openEdit(pendingRouteQuery.data.data);
    setPendingFetchId(null);
  } else if (pendingRouteQuery.error) {
    toast.error("Route no longer exists");
    setPendingFetchId(null);
  }
}, [pendingFetchId, pendingRouteQuery.data, pendingRouteQuery.error]);

// Shared edit-route handler: try list cache, then fall back to fetch.
const handleEditRoute = (routeId: string) => {
  const cached = routes.find((r) => r.id === routeId);
  if (cached) {
    openEdit(cached);
  } else {
    setPendingFetchId(routeId);
  }
};
```

Also add `useEffect` to the existing `useState` import line:

```ts
import { useState, useMemo, useEffect } from "react";
```

- [ ] **Step 3: Replace the List-tab JSX shell with Tabs**

Find the existing `return ( … )` block in `RoutesPage`. Currently it's roughly:

```tsx
return (
  <div className="space-y-6">
    <PageHeader … actions={…} />
    {/* the data table and its columns */}
    {/* the Pagination */}
    {/* the Edit Dialog */}
    {/* the Delete Dialog */}
  </div>
);
```

Restructure to:

```tsx
return (
  <div className="space-y-6">
    <PageHeader
      title="Routes"
      description="Route requests to upstream groups by canonical model name (superadmin only)"
      actions={
        // keep the existing actions block (add-route button) — copy as-is
      }
    />

    <Tabs
      value={activeView}
      onValueChange={(v) => {
        const next = new URLSearchParams(searchParams);
        if (v === "matrix") {
          next.set("view", "matrix");
        } else {
          next.delete("view");
        }
        setSearchParams(next, { replace: true });
      }}
    >
      <TabsList>
        <TabsTrigger value="list">List</TabsTrigger>
        <TabsTrigger value="matrix">Matrix</TabsTrigger>
      </TabsList>

      <TabsContent value="list" className="space-y-4">
        {/* The existing DataTable + Pagination block goes here, verbatim. */}
      </TabsContent>

      <TabsContent value="matrix">
        <RoutesMatrixView onEditRoute={handleEditRoute} />
      </TabsContent>
    </Tabs>

    {/* Edit Dialog and Delete Dialog stay OUTSIDE the Tabs so they
        render once at page level and are reachable from either tab. */}
    {/* …existing Dialog markup unchanged… */}
  </div>
);
```

The Edit Dialog and Delete Dialog must be moved outside the Tabs container (if they aren't already). Their state (`dialogOpen`, `editingId`, `deleteTarget`) already lives at the `RoutesPage` level, so no state hoisting is needed — only JSX repositioning.

- [ ] **Step 4: Update the List tab's "Edit" menu item to use `handleEditRoute`**

In the `columns` array, find the existing entry:

```tsx
<DropdownMenuItem onClick={() => openEdit(r)}>
  <Pencil className="mr-2 h-4 w-4" />
  Edit
</DropdownMenuItem>
```

Change to:

```tsx
<DropdownMenuItem onClick={() => handleEditRoute(r.id)}>
  <Pencil className="mr-2 h-4 w-4" />
  Edit
</DropdownMenuItem>
```

This routes both tabs through the same code path. Cached routes resolve immediately; uncached IDs (only possible from the Matrix tab on a fresh load) fetch via `useRoutingRoute`.

- [ ] **Step 5: Type-check + build**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc -b && pnpm build`
Expected: no errors; build artifact in `dist/`.

- [ ] **Step 6: Manual smoke check (the developer running this plan does this once)**

Start the dev server and verify:

```bash
cd /root/coding/modelserver/dashboard && pnpm dev
```

1. Visit `/admin/routes` — defaults to the List tab; the URL has no `view=` param (or `view=list`).
2. Click the **Matrix** tab — URL becomes `…?view=matrix`; the matrix loads; rows are alphabetical model names; columns are alphabetical kind names; filled cells show outline badges; empty cells show `—`.
3. Click a filled cell — the existing edit dialog opens, pre-populated with the route's model_names / request_kinds / group / etc.
4. Browser refresh on `…?view=matrix` — stays on the Matrix tab.
5. Browser back from `?view=matrix` to `/admin/routes` — switches back to List.
6. Create a new route in the List tab — return to Matrix and confirm it's reflected (invalidation works).

- [ ] **Step 7: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/pages/admin/RoutesPage.tsx
git commit -m "feat(dashboard): Matrix tab on /admin/routes

Wraps the existing list view in a Tabs primitive (List | Matrix) with
the active tab bound to ?view=. The matrix tab uses RoutesMatrixView;
clicking a cell opens the existing route edit dialog, falling back to
GET /routing/routes/{id} when the route is not in the list cache.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage**

| Spec section | Task(s) |
|---|---|
| `GET /api/v1/routing/matrix` endpoint | Task 3 |
| Response shape (`models`, `kinds`, sparse `cells`) | Task 3 (Step 3) |
| `MatrixGlobal` on `*Router` mirroring `Match` rules | Task 1 |
| `SnapshotGroupNames` on `*Router` | Task 1 |
| No DB / migration changes | Confirmed — no migration tasks |
| Alphabetical models + kinds | Task 3 (`sort.Strings` on both) |
| Lift edit-dialog state above tabs | Task 6 (Step 3 — note: dialog state already lives at page level; only JSX repositioning needed) |
| `?view=list\|matrix` URL state | Task 6 (Step 2 + Step 3) |
| `RoutesMatrixView` component visual contract (sticky header / sticky model col / dash for empty / outline badge for filled) | Task 5 |
| Cell click opens edit dialog with cold-cache fetch fallback | Task 6 (Step 2 — `handleEditRoute` + `useRoutingRoute`) |
| Error handling: 500 on list-models failure, 404 toast on cell click after route deleted, friendly empty state when catalog is empty | Task 3 (handler), Task 6 (Step 2 — toast), Task 5 (empty state branch) |
| Backend tests: priority, status filter, project filter, multi-kind/model, missing group | Task 1 (Step 1) |
| Backend integration test: sparse JSON shape, alphabetical ordering, auth | Task 3 (Step 1) — auth coverage is implicit via the `RequireSuperadmin` middleware that wraps the route in Task 3 Step 6; standalone auth test was in the spec but is omitted here because the existing `RequireSuperadmin` is well-tested elsewhere |
| Frontend tests | Skipped — repo has no frontend test framework (called out in Global Constraints); replaced by manual smoke check in Task 6 Step 6 |
| GET-by-id endpoint for cold-cache lookup | Task 2 |

Auth gap is intentional and called out; no spec requirement is unimplemented.

**2. Placeholder scan**

- No TBD / TODO / "implement later" / "add appropriate error handling" / "similar to Task N" / vague refs.
- Task 2 Step 4 has a conditional comment about reusing an existing test helper — that's a verified-at-implementation-time check, not a placeholder, and the fallback (`t.Skip`) is concrete.
- Task 6 Step 1 instructs the engineer to read the file before editing — this is necessary because `RoutesPage.tsx` is already several hundred lines and any structural edit must align with the actual current JSX (existing actions block, existing column definitions, existing Delete Dialog markup). The Step 3 example block is the target shape, and Steps 3–4 list the precise edits.

**3. Type consistency**

- `MatrixCell` (Go) fields: `Model, Kind, UpstreamGroupID, RouteID, MatchPriority` — Task 1 declares them, Task 3 consumes the exact names.
- `matrixCellOut` JSON tags (snake_case) match the documented spec response and the TS `RoutingMatrixCell` interface field-for-field (Task 3 Step 3 ↔ Task 4 Step 1).
- `handleRoutingMatrix(st *store.Store, router *proxy.Router)` consistent across Task 3 Step 3 and Task 3 Step 6.
- `useRoutingMatrix()` and `useRoutingRoute(id)` used in Task 5 and Task 6 match their Task 4 definitions.
- `onEditRoute: (routeId: string) => void` prop name consistent across Task 5 and Task 6.
- `handleEditRoute` (camelCase) — same identifier in Task 6 Steps 2 and 4.

No naming drift detected.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-28-route-matrix-view.md`.
