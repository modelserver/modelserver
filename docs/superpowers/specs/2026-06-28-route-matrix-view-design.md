# Route Matrix View — Design

## Problem

The current `/admin/routes` page lists routes as flat rows: each row is one route
record (model_names, request_kinds, group, priority, scope, status). Answering a
basic operational question — *"if a client calls model X on endpoint Y, which
upstream group will handle it?"* — requires reading every route in priority
order in one's head and re-running the resolver. With multiple routes and 8
request kinds, this is error-prone enough that it's worth a dedicated view.

## Goal

Add a **Model × Request-Kind matrix** to `/admin/routes` that shows, for each
active catalog model and each of the 8 request kinds, the upstream group that
the proxy's `Router.Match` would resolve to (global routes only).

The matrix must reuse the proxy's actual matching logic so the view cannot
drift from runtime behavior.

## Non-goals

- **No project-scoped overlay.** The matrix is for global routes only. A
  project selector / overlay can be added later if the operational need
  appears; the backend response shape leaves room for it.
- **No write operations** in the matrix view itself. Editing a route is reached
  by clicking a cell, which opens the existing route-edit dialog.
- **No new routing semantics.** The matrix is a read-only projection of the
  existing routing data; no schema changes, no migration.

## Scope decisions (from brainstorming)

| Decision | Choice |
|---|---|
| Matrix scope | Global routes only |
| Placement | New tab on `/admin/routes` (`?view=list\|matrix`) |
| Rows | All active catalog models |
| Columns | The 8 `RequestKind` constants, **alphabetical** order, full names |
| Compute site | Backend admin endpoint reusing `Router.Match` rules |
| Cell content | Upstream-group name as outline `Badge` |
| Empty cell | Subtle dimmed dash `—` |
| Cell click | Opens the **existing** route-edit dialog for the winning route |
| Row sort | Alphabetical by canonical model name |
| Group colors | None — same outline `Badge` style for every group |

## Backend

### New endpoint

`GET /api/v1/routing/matrix` — superadmin only, mounted under the existing
`r.Route("/routing", ...)` group in `internal/admin/routes.go`.

Response:

```json
{
  "data": {
    "models": ["claude-haiku-4-5", "gemini-2.5-pro", "gpt-5"],
    "kinds":  [
      "anthropic_count_tokens",
      "anthropic_messages",
      "google_generate_content",
      "openai_chat_completions",
      "openai_images_edits",
      "openai_images_generations",
      "openai_responses",
      "openai_responses_compact"
    ],
    "cells": [
      {
        "model": "claude-haiku-4-5",
        "kind":  "anthropic_messages",
        "upstream_group_id":   "ug_abc",
        "upstream_group_name": "anthropic-pool",
        "route_id":            "rt_def",
        "match_priority":      10
      }
    ]
  }
}
```

- `models` — canonical names from `store.ListModelsByStatus(active)`, sorted
  ASCII-ascending. The frontend uses this exact order for rows.
- `kinds` — `types.AllRequestKinds`, sorted ASCII-ascending. Sorting on the
  server (not relying on the Go constant order) guarantees the frontend's
  alphabetical column order is identical without re-sorting in TS.
- `cells` — **sparse**. Only the `(model, kind)` pairs that resolve to a group
  appear. Pairs with no matching route are simply absent; the frontend renders
  a dash for them.

### Resolution logic

A new method on `*proxy.Router`:

```go
type MatrixCell struct {
    Model           string
    Kind            string
    UpstreamGroupID string
    RouteID         string
    MatchPriority   int
}

func (r *Router) MatrixGlobal(models []string) []MatrixCell
```

Behavior (mirrors the global-route branch of `Router.Match` exactly):

1. Take an `RLock` on `r.mu`.
2. For each `(model, kind)` in `models × types.AllRequestKinds`:
   - Walk `r.routes` in their existing sorted order (priority descending).
   - Skip routes with `Status != "active"`.
   - Skip routes with `ProjectID != ""` (this view is global-only).
   - First route whose `ModelNames` contains `model` and `RequestKinds`
     contains `kind` wins.
   - If the winning route's `UpstreamGroupID` is not present in `r.groups`,
     skip the pair (defensive: matches the silent-drop behavior of `Match`).
3. Emit one `MatrixCell` per winner; pairs with no winner produce nothing.
4. Release the lock. Returns are by value; the caller can mutate freely.

A second small helper exposes group names without leaking the internal
`resolvedGroup` type:

```go
func (r *Router) SnapshotGroupNames() map[string]string
```

Returns a fresh map keyed by group ID. Used by the admin handler to join the
human-readable group name onto each cell.

**Why expose this on `*Router` rather than recompute in `internal/admin`:**
the admin package would otherwise have to re-derive the priority / active-status
rules, and any future change to `Match` (added `Conditions` checks, new
fallback layer, etc.) would silently skip the matrix view. One source of
truth.

### Handler

`internal/admin/handle_routing_matrix.go` (new file, sibling of
`handle_routing_routes.go`):

```go
func handleRoutingMatrix(st *store.Store, router *proxy.Router) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        models, err := st.ListModelsByStatus(types.ModelStatusActive)
        if err != nil {
            writeError(w, http.StatusInternalServerError, "internal",
                "failed to list models")
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

        // matrixResponse + matrixCell are local types with json tags matching
        // the documented response shape (snake_case).
        out := matrixResponse{Models: names, Kinds: kinds,
            Cells: make([]matrixCell, 0, len(cells))}
        for _, c := range cells {
            out.Cells = append(out.Cells, matrixCell{
                Model:             c.Model,
                Kind:              c.Kind,
                UpstreamGroupID:   c.UpstreamGroupID,
                UpstreamGroupName: groupNames[c.UpstreamGroupID], // "" if missing
                RouteID:           c.RouteID,
                MatchPriority:     c.MatchPriority,
            })
        }
        writeData(w, http.StatusOK, out)
    }
}
```

The handler signature gains a `*proxy.Router` parameter, threaded through
`admin.MountRoutes` (the router is constructed before admin mounts and lives
for the process lifetime — same pattern would apply to any handler that needed
live routing state).

### No DB changes, no migration

The matrix is a pure projection over existing tables. All data already exists.

## Frontend

### Routing & page shell

`dashboard/src/pages/admin/RoutesPage.tsx` keeps its current responsibilities
(data fetching for List, edit/delete dialogs) but its body becomes a Tabs
primitive:

- **List** — the existing table, columns unchanged.
- **Matrix** — the new view.

Active tab is bound to a `?view=list|matrix` URL search-param so refresh and
back-button preserve the user's tab. Default is `list` (backward compatible).

### Lifting edit-dialog state

The edit dialog currently lives inside `RoutesPage` and is opened by clicking a
row's "Edit" menu item. To let matrix cells reuse it without prop-drilling:

- `editingId`, `dialogOpen`, and the form state stay in `RoutesPage` (outside
  the Tabs body), so the dialog renders once at page level.
- `RoutesPage` passes a shared `onEditRoute(routeId: string)` to both tabs.
- The List tab's existing "Edit" menu item invokes the same callback.
- The Matrix tab passes the callback into cells.

If a cell's `route_id` isn't in the cached routes list (e.g. fresh session,
matrix loaded but List query not yet warm), `onEditRoute` falls back to
`api.get<DataResponse<RoutingRoute>>(`/api/v1/routing/routes/${id}`)`.
This endpoint doesn't exist today (`handle_routing_routes.go` only has list /
create / update / delete) — **adding a GET-by-id is in scope** since it's the
only way to populate the edit dialog from a cold cache. Keep its shape
identical to the elements of the list response.

### API hook

`dashboard/src/api/upstreams.ts`:

```ts
export interface RoutingMatrixCell {
  model: string;
  kind: string;
  upstream_group_id: string;
  upstream_group_name: string;
  route_id: string;
  match_priority: number;
}

export interface RoutingMatrix {
  models: string[];
  kinds:  string[];
  cells:  RoutingMatrixCell[];
}

export function useRoutingMatrix() {
  return useQuery({
    queryKey: ["admin", "routing-matrix"],
    queryFn: () =>
      api.get<DataResponse<RoutingMatrix>>("/api/v1/routing/matrix"),
  });
}
```

Existing `useCreateRoutingRoute`, `useUpdateRoutingRoute`,
`useDeleteRoutingRoute` add `["admin", "routing-matrix"]` to their
`invalidateQueries` arrays so the matrix refreshes when routes change.

### Matrix component

`dashboard/src/pages/admin/RoutesMatrixView.tsx` (new):

- Custom `<table>` (not the shared `DataTable` — that one is row-of-records
  shaped; the matrix is a cell grid).
- Sticky left column (model name) and sticky header row (kind name) via
  `position: sticky` + `z-index` layering. Horizontal scroll lives inside the
  outer Card on narrow viewports.
- Builds an index `Map<\`${model}::${kind}\`, RoutingMatrixCell>` once per
  data change for O(1) cell lookup.
- Each filled cell renders `<Badge variant="outline">{group_name}</Badge>` (or
  the truncated `upstream_group_id` if the name is empty — defensive). Click
  fires `onEditRoute(cell.route_id)`.
- Each empty cell renders `<span className="text-muted-foreground">—</span>`.
- Loading / error / empty (no models) states render in the same Card shell so
  the matrix doesn't flicker the tab layout.

### Visual layout

```
┌── Routes ────────────────────────────────────────────────────────┐
│  [ List ] [ Matrix ]                              + Add Route    │
├──────────────────────────────────────────────────────────────────┤
│  ┌──────────────┬────────────────────┬──────────────┬──…──┐     │
│  │  Model       │ anthropic_count_…  │ anthropic_…  │  …  │     │
│  ├──────────────┼────────────────────┼──────────────┼──…──┤     │
│  │ claude-h-4-5 │ [anthropic-pool]   │ [anthropic-…]│  —  │     │
│  │ gpt-5        │   —                │   —          │[oai]│     │
│  │ gemini-2-5   │   —                │   —          │  —  │     │
│  └──────────────┴────────────────────┴──────────────┴──…──┘     │
└──────────────────────────────────────────────────────────────────┘
```

## Data flow

```
Browser opens /admin/routes?view=matrix
  └─► useRoutingMatrix() ─► GET /api/v1/routing/matrix
        └─► admin handler:
             1. st.ListModelsByStatus(active)  → []Model
             2. router.MatrixGlobal(names)     → []MatrixCell (sparse)
             3. router.SnapshotGroupNames()    → map[id]name
             4. assemble + write JSON
  ◄─ { models, kinds, cells }
  └─► RoutesMatrixView indexes cells; renders R×8 grid
  user clicks a filled cell
  └─► onEditRoute(cell.route_id)
       └─► existing dialog opens
            (route from List query if cached; else GET /routing/routes/{id})
```

## Error handling

| Failure | Behavior |
|---|---|
| `ListModelsByStatus` returns error | `500 internal`, dashboard shows page-level alert + retry |
| Router not yet warmed | Defensive `503 service_unavailable` — should never trigger after bootstrap |
| Cell's group ID missing from `groups` map | Cell omitted by `MatrixGlobal` (matches `Match` behavior) |
| Group name lookup returns "" | Cell still emitted with the ID; UI falls back to a truncated ID badge so the operator can identify it |
| Empty catalog | Friendly empty state in the matrix view ("No active models in catalog") |
| Cell click after the route was deleted | Edit fetch 404s → `toast.error("Route no longer exists")` + invalidate matrix query |

## Testing

### Backend

`internal/proxy/router_engine_test.go` — extend with `TestRouter_MatrixGlobal`:

- Sparse output: unrouted pairs absent.
- Priority resolution: high-priority route wins over a lower-priority route
  that also matches.
- `Status != "active"` routes skipped even if otherwise matching.
- Project-scoped routes (`ProjectID != ""`) never appear.
- A route covering N models × M kinds produces N×M cells.
- Group ID missing from the resolved map → pair silently omitted (matches
  `Match`).

`internal/admin/handle_routing_matrix_test.go` (new):

- Integration test using a real `*proxy.Router` built from in-memory store
  data, asserting JSON shape, sparseness of `cells`, and alphabetical
  ordering of `models` / `kinds`.
- Auth: non-superadmin caller gets 403.

`internal/admin/handle_routing_routes_test.go` — add a case for the new
GET-by-id endpoint (404 path + happy path).

### Frontend

`dashboard/src/pages/admin/RoutesMatrixView.test.tsx`:

- Renders given fixture data: filled cells show the group name badge; empty
  cells show a dash.
- Clicking a filled cell calls `onEditRoute(routeId)`.
- Alphabetical row/column ordering matches the fixture's `models` / `kinds`.

`dashboard/src/pages/admin/RoutesPage.test.tsx`:

- Tab switch updates `?view=…` and reading the URL on mount selects the
  correct tab.
- Edit dialog opened from the Matrix tab populates from the List cache when
  available; falls back to the GET-by-id fetch when not.

## Build sequence

1. Backend: add `MatrixGlobal` + `SnapshotGroupNames` on `*Router` with unit
   tests. Pure addition, no callers yet.
2. Backend: add the GET-by-id route handler with tests.
3. Backend: add `handleRoutingMatrix` + register on the admin router; thread
   `*proxy.Router` through `admin.MountRoutes`; update the single caller in
   `cmd/`. Integration test.
4. Frontend: API hook + types.
5. Frontend: lift edit-dialog state in `RoutesPage`; wire the shared
   `onEditRoute` callback through the List tab (no visual change yet).
6. Frontend: add Tabs primitive + URL search-param sync; add `RoutesMatrixView`
   wired to the hook and the shared callback.
7. Frontend: tests.

Each step ends in a green build; the user-visible change lands only at step 6.
