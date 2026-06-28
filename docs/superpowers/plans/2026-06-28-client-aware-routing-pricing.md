# Client-Aware Routing + Pricing Implementation Plan (v3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Revision note (v3).** The first plan added `billing_mode` as a routing dimension. v3 drops it: routing is by client only, because Claude Code / Codex upstreams are wire-format gated (only their own clients can land on them); the subscription-vs-extra-usage distinction is a runtime billing concern handled by the existing chain. The pricing layer (per-client overrides on plans for subscription consumption) stays.

**Goal:** Thread a per-request `client_bucket ∈ {claude-code-cli, claude-desktop, codex-cli, codex-desktop, other}` (derived from the existing `ClientKind*` enum) through routing and subscription pricing. Routes can optionally be scoped by `clients []string` (empty = match any). Plans can override the credit rate per `(client, model)` for subscription consumption. Extra-usage pricing is unchanged.

**Architecture:** Routing layer extends `Route` with `clients []string` and `Router.Match` with a weighted-specificity tiebreak (project 10, clients 1; then `match_priority desc`; then `id asc`). Pricing layer extends `Plan` with `client_model_credit_rates map[client]map[model]CreditRate` and adds `Policy.ComputeCreditsForClient` resolving per-client → per-model → catalog default → plan `_default`. **No new middleware, no new balance check, no `billing_mode` axis.** The runtime decision between subscription and extra-usage stays in the existing `RateLimit + ExtraUsageGuard` chain, exactly as today. Backward-compat invariants: plans without the new field produce identical credit counts to today; routes with empty `clients` match any request.

**Tech Stack:** Go 1.x, `pgx` (`pool.Begin` / `tx.Exec`), `JSONB`, stdlib `testing`. React 19, `@tanstack/react-query` v5, Tailwind v4. Two SQL migrations (056, 057). No new dependencies.

## Global Constraints

- **Spec:** `docs/superpowers/specs/2026-06-28-client-aware-routing-pricing-design.md` — re-read before each task.
- **`billing_mode` is NOT in the routing layer.** Do NOT add a `BillingMode` Go type, a `billing_modes` column, a `?billing_mode=` query param, or a dashboard control. The runtime distinction lives in `reqCtx.IsExtraUsage`, which the Executor uses internally to pick between subscription pricing (`ComputeCreditsForClient`) and extra-usage pricing (`computeExtraUsageCostCredits`) — that's the only place a billing label appears, and it stays a local variable.
- **No new middleware.** Trace middleware gains one line (writes `ctxClientBucket`); `SubscriptionEligibilityMiddleware`'s `SubscriptionEligibility{Eligible, Reason}` output shape is unchanged.
- **Extra-usage pricing is unchanged.** Do NOT touch `computeExtraUsageCostCredits` or `settleExtraUsage`. Per-client overrides apply ONLY to the subscription pricing path (`executor.go:1090, :1328`).
- **Backward-compat invariants:**
  - A plan WITHOUT `client_model_credit_rates` MUST produce identical credit counts to today (resolver falls through to existing `ModelCreditRates[model]` at step 2).
  - A route with empty `clients` MUST match every request (= today's behavior).
- **Client bucket values:** exactly `claude-code-cli`, `claude-desktop`, `codex-cli`, `codex-desktop`, `other`. Constants in `internal/types/client_bucket.go`.
- **`ClientBucketCodexDesktop` is reserved.** `MapClientKindToBucket` returns `other` for any input today — codex-desktop will be wired when the product ships an identifiable client.
- **Specificity weights:** project=10, clients=1. Final tiebreak `ID asc`. Same code path used by `Match` AND `MatrixGlobal` via the shared `matchesGlobalRoute` predicate.
- **Migrations:** 056 (`routes.clients` TEXT[]) and 057 (`plans.client_model_credit_rates` JSONB). `IF NOT EXISTS` guarded. No down step.
- **No frontend test framework** — dashboard task verifies via `pnpm exec tsc -b && pnpm build` + manual smoke.
- **Commit message footer:** every commit ends with
  ```
  Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
  ```

## File Structure

**Backend — create:**
- `internal/types/client_bucket.go` — 5 constants + `MapClientKindToBucket` + `IsValidClientBucket` + `AllClientBuckets`.
- `internal/types/client_bucket_test.go`
- `internal/store/migrations/056_route_clients.sql`
- `internal/store/migrations_056_test.go`
- `internal/store/migrations/057_plan_client_credit_rates.sql`
- `internal/store/migrations_057_test.go`

**Backend — modify:**
- `internal/types/route.go` — add `Clients []string` field.
- `internal/types/plan.go` — add `ClientModelCreditRates` field + thread through `ToPolicy`.
- `internal/types/policy.go` — add `ClientModelCreditRates` field on `RateLimitPolicy`; add `ComputeCreditsForClient` resolver; keep `ComputeCreditsWithDefault` as wrapper.
- `internal/types/policy_test.go` — resolver tests + backward-compat invariant.
- `internal/store/routes.go` — extend `routeSelectCols`, `CreateRoute`, `scanRoute`, `GetRouteByID` with the new column.
- `internal/store/plans.go` — extend SELECT lists, `Scan`, `CreatePlan`, `unmarshalPlanJSON` with the new column.
- `internal/proxy/router_engine.go` — extend `Router.Match` signature; extend `matchesGlobalRoute`; replace priority-only walk with weighted-specificity sort; extend `MatrixGlobal` + add optional `client` filter; extend `MatrixCell` with `Client` and `Clients`.
- `internal/proxy/router_engine_test.go` — update existing call sites; add precedence / tiebreak / matrix-filter tests.
- `internal/proxy/trace_middleware.go` — write `ctxClientBucket` next to `ctxClientKind`; export `ClientBucketFromContext`.
- `internal/proxy/trace_middleware_test.go` — assert bucket populated for every `ClientKind*`.
- `internal/proxy/executor.go` — populate `reqCtx.ClientBucket` at request entry; pass it to `router.Match`; switch the two subscription pricing call sites to `ComputeCreditsForClient`.
- `internal/proxy/executor_finalize_test.go` — no-regression invariant tests.
- `internal/admin/handle_routing_routes.go` — accept + validate `clients` on create/update; add `handleListClientBuckets`.
- `internal/admin/handle_routing_matrix.go` — accept `?client=` query param; extend `matrixCellOut` with `client` + `clients`.
- `internal/admin/handle_routing_matrix_test.go` — extend with filter cases.
- `internal/admin/handle_plans.go` — extend allow-list with `client_model_credit_rates`.
- `internal/admin/routes.go` — register the new `GET /routing/clients` endpoint.

**Frontend — modify:**
- `dashboard/src/api/types.ts` — extend `RoutingRoute` + `RoutingMatrixCell` with the new fields.
- `dashboard/src/api/upstreams.ts` — add `useClientBuckets()`; extend `useRoutingMatrix` to accept optional `{ client? }`.
- `dashboard/src/pages/admin/RoutesPage.tsx` — one new table column; one new toggle-button row in the Create/Edit dialog.
- `dashboard/src/pages/admin/RoutesMatrixView.tsx` — one filter dropdown at the top; URL state.

---

### Task 1: ClientBucket type primitives

**Files:**
- Create: `internal/types/client_bucket.go`
- Create: `internal/types/client_bucket_test.go`

**Interfaces:**
- Consumes: existing `ClientKind*` constants in `internal/types/extra_usage.go`.
- Produces:
  ```go
  const (
      ClientBucketClaudeCodeCLI = "claude-code-cli"
      ClientBucketClaudeDesktop = "claude-desktop"
      ClientBucketCodexCLI      = "codex-cli"
      ClientBucketCodexDesktop  = "codex-desktop"
      ClientBucketOther         = "other"
  )
  var AllClientBuckets []string
  func IsValidClientBucket(s string) bool
  func MapClientKindToBucket(kind string) string
  ```

Pure additions. No callers yet. Task 5 wires `MapClientKindToBucket` into the trace middleware; Tasks 6-7 consume the constants downstream.

- [ ] **Step 1: Write the failing tests**

Create `internal/types/client_bucket_test.go`:

```go
package types

import "testing"

func TestMapClientKindToBucket(t *testing.T) {
	cases := []struct {
		kind, want string
	}{
		{ClientKindClaudeCode, ClientBucketClaudeCodeCLI},
		{ClientKindClaudeDesktop, ClientBucketClaudeDesktop},
		{ClientKindCodex, ClientBucketCodexCLI},
		{ClientKindOpenCode, ClientBucketOther},
		{ClientKindOpenClaw, ClientBucketOther},
		{ClientKindUnknown, ClientBucketOther},
		{"", ClientBucketOther},
		{"some-future-thing", ClientBucketOther},
	}
	for _, c := range cases {
		if got := MapClientKindToBucket(c.kind); got != c.want {
			t.Errorf("MapClientKindToBucket(%q) = %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestIsValidClientBucket(t *testing.T) {
	for _, b := range AllClientBuckets {
		if !IsValidClientBucket(b) {
			t.Errorf("IsValidClientBucket(%q) = false, want true", b)
		}
	}
	for _, b := range []string{"", "claude-code", "anything-else"} {
		if IsValidClientBucket(b) {
			t.Errorf("IsValidClientBucket(%q) = true, want false", b)
		}
	}
}

func TestAllClientBuckets_ContainsFive(t *testing.T) {
	if got := len(AllClientBuckets); got != 5 {
		t.Errorf("len(AllClientBuckets) = %d, want 5", got)
	}
}

func TestClientBucketCodexDesktop_ReservedReturnsOther(t *testing.T) {
	for _, k := range []string{ClientKindClaudeCode, ClientKindClaudeDesktop,
		ClientKindCodex, ClientKindOpenCode, ClientKindOpenClaw, ClientKindUnknown} {
		if got := MapClientKindToBucket(k); got == ClientBucketCodexDesktop {
			t.Errorf("ClientKind %q unexpectedly maps to codex-desktop", k)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /root/coding/modelserver && go test ./internal/types/ -run 'TestMapClientKindToBucket|TestIsValidClientBucket|TestAllClientBuckets|TestClientBucketCodexDesktop' -v`
Expected: build errors — undefined constants and functions.

- [ ] **Step 3: Implement `client_bucket.go`**

Create `internal/types/client_bucket.go`:

```go
package types

// Client bucket constants. Five-bucket projection of the existing
// ClientKind* enum, used for routing and per-client pricing.
//
// claude-code-cli, claude-desktop, codex-cli are derived from today's
// deriveClientKind output. codex-desktop is reserved for a future Codex
// desktop product; MapClientKindToBucket returns "other" for every
// current input. The bucket exists in the schema today so admin tools
// and the dashboard can name it without a follow-up migration.
const (
	ClientBucketClaudeCodeCLI = "claude-code-cli"
	ClientBucketClaudeDesktop = "claude-desktop"
	ClientBucketCodexCLI      = "codex-cli"
	ClientBucketCodexDesktop  = "codex-desktop"
	ClientBucketOther         = "other"
)

// AllClientBuckets enumerates every ClientBucket* constant.
// Used by admin input validation and the dashboard dropdown.
var AllClientBuckets = []string{
	ClientBucketClaudeCodeCLI,
	ClientBucketClaudeDesktop,
	ClientBucketCodexCLI,
	ClientBucketCodexDesktop,
	ClientBucketOther,
}

// IsValidClientBucket reports whether s is one of the five bucket values.
func IsValidClientBucket(s string) bool {
	for _, b := range AllClientBuckets {
		if b == s {
			return true
		}
	}
	return false
}

// MapClientKindToBucket projects the six-value ClientKind* enum onto the
// five-bucket axis used by routing and pricing.
//
// The codex-desktop case is intentionally absent: no current
// deriveClientKind output identifies that product. When Codex ships a
// desktop client with a recognizable signature (UA / header / body),
// add a dedicated case here. Today every codex-desktop request falls
// into "other".
func MapClientKindToBucket(kind string) string {
	switch kind {
	case ClientKindClaudeCode:
		return ClientBucketClaudeCodeCLI
	case ClientKindClaudeDesktop:
		return ClientBucketClaudeDesktop
	case ClientKindCodex:
		return ClientBucketCodexCLI
	default:
		// ClientKindOpenCode, ClientKindOpenClaw, ClientKindUnknown,
		// and any future kind not explicitly mapped above.
		return ClientBucketOther
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /root/coding/modelserver && go test ./internal/types/ -run 'TestMapClientKindToBucket|TestIsValidClientBucket|TestAllClientBuckets|TestClientBucketCodexDesktop' -v`
Expected: all PASS.

- [ ] **Step 5: Run full types package**

Run: `cd /root/coding/modelserver && go test ./internal/types/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/types/client_bucket.go internal/types/client_bucket_test.go
git commit -m "feat(types): ClientBucket (5) primitives for client-aware routing

Five-value projection of the existing ClientKind* enum.
MapClientKindToBucket collapses claude-desktop, codex-cli, opencode,
openclaw, unknown onto the bucket axis. codex-desktop is reserved and
always returns other today.

Later tasks wire this into trace middleware (Task 5), router (Task 6),
and pricing (Task 7).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Migration 056 — routes.clients

**Files:**
- Create: `internal/store/migrations/056_route_clients.sql`
- Create: `internal/store/migrations_056_test.go`

**Interfaces:**
- Consumes: existing `routes` table schema.
- Produces: new column `routes.clients TEXT[] NOT NULL DEFAULT '{}'`. Subsequent tasks (`types.Route` struct change, store load/save updates, `Router.Match` extension) consume it.

Pre-existing migration max is `055_revoke_orphaned_api_keys.sql` (merged via PR #65). 056 is the next number.

- [ ] **Step 1: Write the SQL**

Create `internal/store/migrations/056_route_clients.sql`:

```sql
-- 056_route_clients.sql
--
-- Add the routing client-bucket dimension to the routes table.
--
--   clients — when populated, only requests whose derived ClientBucket
--             (5 values: claude-code-cli, claude-desktop, codex-cli,
--             codex-desktop, other) is in this list match the route.
--
-- Empty array means "match any value", preserving today's behavior for
-- every existing route. Migration is therefore safe to deploy ahead of
-- the matcher upgrade — old routes simply continue to match every
-- request as they do today.
--
-- Idempotent via IF NOT EXISTS. No down step.

ALTER TABLE routes
    ADD COLUMN IF NOT EXISTS clients TEXT[] NOT NULL DEFAULT '{}';
```

- [ ] **Step 2: Write the migration test**

Create `internal/store/migrations_056_test.go`:

```go
package store

import (
	"context"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// TestMigration056_AddsRouteClientsWithEmptyDefault asserts the
// migration adds clients as TEXT[] NOT NULL DEFAULT '{}', leaves
// existing rows with empty array, and round-trips populated values.
func TestMigration056_AddsRouteClientsWithEmptyDefault(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var groupID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO upstream_groups (name, lb_policy, status)
		VALUES ('mig056-test', 'weighted_random', 'active')
		RETURNING id`).Scan(&groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}

	// Insert using ONLY pre-056 columns — new column must accept the row
	// via its default.
	var oldRouteID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO routes (model_names, request_kinds, upstream_group_id, match_priority, status)
		VALUES (ARRAY['claude-sonnet'], ARRAY[$1::text], $2, 10, 'active')
		RETURNING id`, types.KindAnthropicMessages, groupID).Scan(&oldRouteID); err != nil {
		t.Fatalf("insert old-style route: %v", err)
	}

	var clients []string
	if err := st.pool.QueryRow(ctx,
		`SELECT clients FROM routes WHERE id = $1`, oldRouteID).
		Scan(&clients); err != nil {
		t.Fatalf("select clients: %v", err)
	}
	if len(clients) != 0 {
		t.Errorf("default clients = %v, want []", clients)
	}

	var newRouteID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO routes (model_names, request_kinds, upstream_group_id, match_priority, status, clients)
		VALUES (ARRAY['claude-sonnet'], ARRAY[$1::text], $2, 20, 'active',
		        ARRAY['claude-code-cli','claude-desktop'])
		RETURNING id`, types.KindAnthropicMessages, groupID).Scan(&newRouteID); err != nil {
		t.Fatalf("insert new-style route: %v", err)
	}
	if err := st.pool.QueryRow(ctx,
		`SELECT clients FROM routes WHERE id = $1`, newRouteID).
		Scan(&clients); err != nil {
		t.Fatalf("select populated clients: %v", err)
	}
	want := []string{"claude-code-cli", "claude-desktop"}
	if !equalStringSlices(clients, want) {
		t.Errorf("populated clients = %v, want %v", clients, want)
	}
}

func TestMigration056_Idempotent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := st.pool.Exec(ctx, `
		ALTER TABLE routes ADD COLUMN IF NOT EXISTS clients TEXT[] NOT NULL DEFAULT '{}'`); err != nil {
		t.Fatalf("re-run migration: %v", err)
	}
}

func equalStringSlices(a, b []string) bool {
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

If `equalStringSlices` already exists in the package, drop the local copy.

- [ ] **Step 3: Run the migration test**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -run TestMigration056 -v`
Expected: SKIP without `TEST_DATABASE_URL`; PASS with it.

- [ ] **Step 4: Run the full store package**

Run: `cd /root/coding/modelserver && go test ./internal/store/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/store/migrations/056_route_clients.sql internal/store/migrations_056_test.go
git commit -m "feat(store): migration 056 — routes.clients TEXT[]

Adds the client-bucket dimension column to routes with empty-array
default. Existing routes carry empty arrays and continue to match
every request. Subsequent tasks wire the matcher to read it.

Idempotent via IF NOT EXISTS; no down step.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Migration 057 — plans.client_model_credit_rates

**Files:**
- Create: `internal/store/migrations/057_plan_client_credit_rates.sql`
- Create: `internal/store/migrations_057_test.go`

**Interfaces:**
- Consumes: existing `plans` table schema.
- Produces: new column `plans.client_model_credit_rates JSONB` (nullable).

- [ ] **Step 1: Write the SQL**

Create `internal/store/migrations/057_plan_client_credit_rates.sql`:

```sql
-- 057_plan_client_credit_rates.sql
--
-- Per-client per-model credit rate overlay for subscription consumption.
-- Shape (JSON object indexed by client bucket, then model name):
--
--   {
--     "claude-code-cli": {
--       "claude-sonnet-4": { "input_rate": 3, "output_rate": 15, ... },
--       "claude-opus-4":   { "input_rate": 15, "output_rate": 75, ... }
--     },
--     "codex-cli": {
--       "gpt-5":           { "input_rate": 0.5, "output_rate": 4 }
--     }
--   }
--
-- Resolution order at runtime (Policy.ComputeCreditsForClient):
--   1. client_model_credit_rates[client][model]   (this column)
--   2. model_credit_rates[model]                  (existing column)
--   3. catalog model.default_credit_rate           (catalog truth)
--   4. model_credit_rates["_default"]              (plan-wide safety net)
--   5. zero (no billing)
--
-- Extra-usage requests do NOT consult this column — they bill at the
-- catalog default rate via computeExtraUsageCostCredits.
--
-- Default NULL on existing rows. Idempotent via IF NOT EXISTS. No down step.

ALTER TABLE plans
    ADD COLUMN IF NOT EXISTS client_model_credit_rates JSONB;
```

- [ ] **Step 2: Write the migration test**

Create `internal/store/migrations_057_test.go`:

```go
package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMigration057_AddsPlanClientRatesColumnNullByDefault(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var planID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO plans (name, slug, display_name, description, tier_level,
		    price_cny_fen, period_months, is_active)
		VALUES ('mig057-test', 'mig057-test', 'Migration 057 Test', '', 0,
		        0, 1, FALSE)
		RETURNING id`).Scan(&planID); err != nil {
		t.Fatalf("seed old-style plan: %v", err)
	}

	var raw []byte
	if err := st.pool.QueryRow(ctx,
		`SELECT client_model_credit_rates FROM plans WHERE id = $1`, planID).
		Scan(&raw); err != nil {
		t.Fatalf("select new column: %v", err)
	}
	if raw != nil {
		t.Errorf("default client_model_credit_rates = %q, want NULL", raw)
	}

	want := map[string]map[string]map[string]float64{
		"claude-code-cli": {"claude-sonnet-4": {"input_rate": 3, "output_rate": 15}},
		"codex-cli":       {"gpt-5": {"input_rate": 0.5, "output_rate": 4}},
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if _, err := st.pool.Exec(ctx,
		`UPDATE plans SET client_model_credit_rates = $1 WHERE id = $2`,
		wantJSON, planID); err != nil {
		t.Fatalf("populate column: %v", err)
	}
	if err := st.pool.QueryRow(ctx,
		`SELECT client_model_credit_rates FROM plans WHERE id = $1`, planID).
		Scan(&raw); err != nil {
		t.Fatalf("select populated column: %v", err)
	}
	var got map[string]map[string]map[string]float64
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	for client, models := range want {
		for model, rates := range models {
			for field, v := range rates {
				if got[client][model][field] != v {
					t.Errorf("got[%q][%q][%q] = %v, want %v",
						client, model, field, got[client][model][field], v)
				}
			}
		}
	}
}

func TestMigration057_Idempotent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := st.pool.Exec(ctx,
		`ALTER TABLE plans ADD COLUMN IF NOT EXISTS client_model_credit_rates JSONB`); err != nil {
		t.Fatalf("re-run migration: %v", err)
	}
}
```

- [ ] **Step 3: Run the migration test**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -run TestMigration057 -v`
Expected: SKIP without `TEST_DATABASE_URL`; PASS with it.

- [ ] **Step 4: Run the full store package**

Run: `cd /root/coding/modelserver && go test ./internal/store/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/store/migrations/057_plan_client_credit_rates.sql internal/store/migrations_057_test.go
git commit -m "feat(store): migration 057 — plans.client_model_credit_rates JSONB

Adds a nullable JSONB column for per-client per-model credit-rate
overlays on subscription consumption. Resolution order (in
Policy.ComputeCreditsForClient, landing in a later task):
  client_model_credit_rates[client][model]
    -> model_credit_rates[model]
    -> catalog model.default_credit_rate
    -> model_credit_rates['_default']
    -> zero

Extra-usage requests bypass this column entirely.

Idempotent via IF NOT EXISTS; no down step.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: types.Route + types.Plan + store load/save wiring

**Files:**
- Modify: `internal/types/route.go` (add `Clients` field)
- Modify: `internal/types/plan.go` (add `ClientModelCreditRates` + thread `ToPolicy`)
- Modify: `internal/types/policy.go` (add `ClientModelCreditRates` on `RateLimitPolicy`)
- Modify: `internal/store/routes.go` (extend `routeSelectCols`, `CreateRoute`, `scanRoute`, `GetRouteByID`)
- Modify: `internal/store/plans.go` (extend SELECT lists, `Scan`, `CreatePlan`, `unmarshalPlanJSON`)

**Interfaces:**
- Consumes: migrations 056 + 057.
- Produces:
  ```go
  type Route struct { /* ... */; Clients []string `json:"clients"` }
  type Plan struct { /* ... */; ClientModelCreditRates map[string]map[string]CreditRate `json:"client_model_credit_rates,omitempty"` }
  type RateLimitPolicy struct { /* ... */; ClientModelCreditRates map[string]map[string]CreditRate `json:"client_model_credit_rates,omitempty"` }
  ```

This task is the data plane: every CRUD path round-trips the new fields. `Router.Match` still ignores the new route field; `ComputeCreditsWithDefault` still ignores the new rate map. Tasks 6 + 7 wire them in.

- [ ] **Step 1: Extend `types.Route`**

In `internal/types/route.go`, change the struct:

```go
type Route struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id,omitempty"` // "" = global route
	ModelNames      []string          `json:"model_names"`
	RequestKinds    []string          `json:"request_kinds"`
	Clients         []string          `json:"clients"`              // NEW — ClientBucket values; empty = match any
	UpstreamGroupID string            `json:"upstream_group_id"`
	MatchPriority   int               `json:"match_priority"`
	Conditions      map[string]string `json:"conditions,omitempty"`
	Status          string            `json:"status"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}
```

- [ ] **Step 2: Extend `types.Plan` + `Plan.ToPolicy`**

In `internal/types/plan.go`, add the field:

```go
type Plan struct {
	// ... existing fields, unchanged ...
	ModelCreditRates       map[string]CreditRate            `json:"model_credit_rates,omitempty"`
	ClientModelCreditRates map[string]map[string]CreditRate `json:"client_model_credit_rates,omitempty"`
	// ... other existing fields ...
}
```

Extend `ToPolicy`:

```go
return &RateLimitPolicy{
	ID:                     "plan:" + p.ID,
	ProjectID:              projectID,
	Name:                   p.Name,
	CreditRules:            rules,
	ModelCreditRates:       p.ModelCreditRates,
	ClientModelCreditRates: p.ClientModelCreditRates, // NEW
	ClassicRules:           p.ClassicRules,
}
```

- [ ] **Step 3: Extend `RateLimitPolicy`**

In `internal/types/policy.go`, add the field:

```go
type RateLimitPolicy struct {
	// ... existing fields, unchanged ...
	ModelCreditRates       map[string]CreditRate            `json:"model_credit_rates,omitempty"`
	ClientModelCreditRates map[string]map[string]CreditRate `json:"client_model_credit_rates,omitempty"`
	// ... other existing fields ...
}
```

- [ ] **Step 4: Build + run types tests**

Run: `cd /root/coding/modelserver && go build ./internal/types/... && go test ./internal/types/...`
Expected: green. Existing tests continue to pass because the new field is unused.

- [ ] **Step 5: Extend `internal/store/routes.go`**

Replace `routeSelectCols`:

```go
const routeSelectCols = `id, COALESCE(project_id::text, ''), model_names, request_kinds,
	upstream_group_id, match_priority, conditions, status, created_at, updated_at,
	clients`
```

Update `CreateRoute`:

```go
func (s *Store) CreateRoute(r *types.Route) error {
	conditionsJSON, _ := json.Marshal(r.Conditions)
	if r.Conditions == nil {
		conditionsJSON = []byte("{}")
	}
	modelNames := r.ModelNames
	if modelNames == nil {
		modelNames = []string{}
	}
	requestKinds := r.RequestKinds
	if requestKinds == nil {
		requestKinds = []string{}
	}
	clients := r.Clients
	if clients == nil {
		clients = []string{}
	}
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO routes (project_id, model_names, request_kinds, upstream_group_id,
		    match_priority, conditions, status, clients)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`,
		nullString(r.ProjectID), modelNames, requestKinds, r.UpstreamGroupID,
		r.MatchPriority, conditionsJSON, r.Status, clients,
	).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}
```

Update `GetRouteByID` Scan list (one new pointer at the end):

```go
).Scan(&r.ID, &r.ProjectID, &r.ModelNames, &r.RequestKinds, &r.UpstreamGroupID,
	&r.MatchPriority, &conditionsRaw, &r.Status, &r.CreatedAt, &r.UpdatedAt,
	&r.Clients)
```

Update `scanRoute`:

```go
if err := rows.Scan(&r.ID, &r.ProjectID, &r.ModelNames, &r.RequestKinds,
	&r.UpstreamGroupID, &r.MatchPriority, &conditionsRaw, &r.Status,
	&r.CreatedAt, &r.UpdatedAt, &r.Clients); err != nil {
	return nil, err
}
```

`ListRoutes`, `ListRoutesPaginated`, and `ListRoutesForProject` reuse `routeSelectCols` + `scanRoute` so they need no further edits.

The admin update path (`handleUpdateRoutingRoute`) field allow-list is extended in Task 8 — not here. The store change alone keeps `go build ./...` green.

- [ ] **Step 6: Extend `internal/store/plans.go`**

The plan SELECT list is inlined in five places (`GetPlanByID`, `GetPlanBySlug`, `ListPlans`, `ListPlansPaginated`, `ListPlansForProject`). Add the new column to every SELECT in a stable position (e.g. immediately after `model_credit_rates`):

```go
// BEFORE:
SELECT id, name, slug, ..., credit_rules, model_credit_rates, classic_rules,
    is_active, created_at, updated_at
FROM plans ...

// AFTER:
SELECT id, name, slug, ..., credit_rules, model_credit_rates,
    client_model_credit_rates,
    classic_rules, is_active, created_at, updated_at
FROM plans ...
```

Add `var clientRates []byte` next to the existing `var creditRules, rates, classic []byte` in each Scan call, and pass `&clientRates` in the matching position.

Extend `unmarshalPlanJSON`:

```go
func unmarshalPlanJSON(p *types.Plan, creditRules, rates, clientRates, classic []byte) error {
	if creditRules != nil {
		if err := json.Unmarshal(creditRules, &p.CreditRules); err != nil {
			return fmt.Errorf("unmarshal credit_rules: %w", err)
		}
	}
	if rates != nil {
		if err := json.Unmarshal(rates, &p.ModelCreditRates); err != nil {
			return fmt.Errorf("unmarshal model_credit_rates: %w", err)
		}
	}
	if clientRates != nil {
		if err := json.Unmarshal(clientRates, &p.ClientModelCreditRates); err != nil {
			return fmt.Errorf("unmarshal client_model_credit_rates: %w", err)
		}
	}
	if classic != nil {
		if err := json.Unmarshal(classic, &p.ClassicRules); err != nil {
			return fmt.Errorf("unmarshal classic_rules: %w", err)
		}
	}
	return nil
}
```

Update every caller of `unmarshalPlanJSON` to pass the new arg.

Extend `CreatePlan`:

```go
func (s *Store) CreatePlan(p *types.Plan) error {
	creditRulesJSON, _ := marshalJSON(p.CreditRules)
	ratesJSON, _ := marshalJSON(p.ModelCreditRates)
	clientRatesJSON, _ := marshalJSON(p.ClientModelCreditRates)
	classicJSON, _ := marshalJSON(p.ClassicRules)

	return s.pool.QueryRow(context.Background(), `
		INSERT INTO plans (name, slug, display_name, description, tier_level, group_tag,
			price_cny_fen, price_usd_cents, period_months, credit_rules, model_credit_rates,
			client_model_credit_rates, classic_rules, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING id, created_at, updated_at`,
		p.Name, p.Slug, p.DisplayName, p.Description, p.TierLevel, p.GroupTag,
		p.PriceCNYFen, p.PriceUSDCents, p.PeriodMonths, creditRulesJSON, ratesJSON,
		clientRatesJSON, classicJSON, p.IsActive,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}
```

`UpdatePlan` is generic via `buildUpdateQuery` — no change.

- [ ] **Step 7: Build + run store tests**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/store/...`
Expected: green.

- [ ] **Step 8: Commit**

```bash
cd /root/coding/modelserver
git add internal/types/route.go internal/types/plan.go internal/types/policy.go \
        internal/store/routes.go internal/store/plans.go
git commit -m "feat(types,store): plumb Clients + ClientModelCreditRates

Route gains Clients []string; Plan and RateLimitPolicy gain a two-level
map[client][model]CreditRate field. Store load/save round-trip the new
columns added in migrations 056+057.

No behavior change yet: Router.Match still ignores Clients and
Policy.ComputeCreditsWithDefault still ignores the new rate map.
Tasks 6 + 7 wire them in.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Trace middleware — `ctxClientBucket` plumbing

**Files:**
- Modify: `internal/proxy/trace_middleware.go`
- Modify: `internal/proxy/trace_middleware_test.go`

**Interfaces:**
- Consumes: `types.MapClientKindToBucket` (Task 1); existing `deriveClientKind` and `ctxClientKind`.
- Produces:
  ```go
  const ctxClientBucket contextKey = "client_bucket"
  func ClientBucketFromContext(ctx context.Context) string  // returns types.ClientBucketOther on miss
  ```

Task 6 and Task 7 read the bucket via `ClientBucketFromContext` (and via the new `RequestContext.ClientBucket` field that the Executor stamps in Task 7).

- [ ] **Step 1: Write the failing test**

Append to `internal/proxy/trace_middleware_test.go`:

```go
func TestTraceMiddleware_WritesClientBucket(t *testing.T) {
	var gotKind, gotBucket string
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKind = ClientKindFromContext(r.Context())
		gotBucket = ClientBucketFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name       string
		setup      func(*http.Request)
		wantKind   string
		wantBucket string
	}{
		{
			name:       "claude_code_cli",
			setup:      func(r *http.Request) { r.Header.Set("User-Agent", "claude-cli/1.0 (external, cli)"); r.Body = io.NopCloser(strings.NewReader(`{"metadata":{"user_id":"user_` + strings.Repeat("a", 64) + `_account__session_00000000-0000-0000-0000-000000000000"}}`)) },
			wantKind:   types.ClientKindClaudeCode,
			wantBucket: types.ClientBucketClaudeCodeCLI,
		},
		{
			name:       "claude_desktop",
			setup:      func(r *http.Request) { r.Header.Set("User-Agent", "Mozilla/5.0 Claude/1.0 (Electron/30.0)") },
			wantKind:   types.ClientKindClaudeDesktop,
			wantBucket: types.ClientBucketClaudeDesktop,
		},
		{
			name:       "codex_cli",
			setup:      func(r *http.Request) { r.Header.Set("session_id", "00000000-0000-0000-0000-000000000000") },
			wantKind:   types.ClientKindCodex,
			wantBucket: types.ClientBucketCodexCLI,
		},
		{
			name:       "opencode_other",
			setup:      func(r *http.Request) { r.Header.Set("User-Agent", "opencode/0.1.0") },
			wantKind:   types.ClientKindOpenCode,
			wantBucket: types.ClientBucketOther,
		},
		{
			name:       "unknown_other",
			setup:      func(r *http.Request) { r.Header.Set("User-Agent", "curl/8.0") },
			wantKind:   types.ClientKindUnknown,
			wantBucket: types.ClientBucketOther,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotKind, gotBucket = "", ""
			mw := TraceMiddleware(config.TraceConfig{TraceHeader: "X-Trace-Id"}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			req := httptest.NewRequest("POST", "/v1/messages", nil)
			c.setup(req)
			mw(probe).ServeHTTP(httptest.NewRecorder(), req)
			if gotKind != c.wantKind {
				t.Errorf("client_kind = %q, want %q", gotKind, c.wantKind)
			}
			if gotBucket != c.wantBucket {
				t.Errorf("client_bucket = %q, want %q", gotBucket, c.wantBucket)
			}
		})
	}
}

func TestClientBucketFromContext_Default(t *testing.T) {
	if got := ClientBucketFromContext(context.Background()); got != types.ClientBucketOther {
		t.Errorf("ClientBucketFromContext(empty) = %q, want %q", got, types.ClientBucketOther)
	}
}
```

Verify the `setup` lambdas match the production matchers (`deriveClientKind` shapes are stable; adapt to existing test fixtures if needed).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run 'TestTraceMiddleware_WritesClientBucket|TestClientBucketFromContext_Default' -v`
Expected: build error — `ctxClientBucket` and `ClientBucketFromContext` undefined.

- [ ] **Step 3: Add context key + getter + writer**

In `internal/proxy/trace_middleware.go`, add `ctxClientBucket` next to `ctxClientKind`:

```go
const (
	ctxTraceID              contextKey = "trace_id"
	ctxTraceSource          contextKey = "trace_source"
	ctxClientKind           contextKey = "client_kind"
	ctxClientBucket         contextKey = "client_bucket" // NEW
	ctxClaudeAgentSDKSource contextKey = "claude_agent_sdk_source"
)
```

Add `ClientBucketFromContext`:

```go
// ClientBucketFromContext returns the 5-value ClientBucket classification
// derived by the trace middleware from ClientKindFromContext. Callers
// that run outside the trace middleware (or in tests that don't set it)
// see ClientBucketOther as a defensive default — never propagate a
// misleading bucket if the upstream wiring drops.
func ClientBucketFromContext(ctx context.Context) string {
	if b, ok := ctx.Value(ctxClientBucket).(string); ok {
		return b
	}
	return types.ClientBucketOther
}
```

Inside `TraceMiddleware`'s handler, immediately after the existing `ctxClientKind` write:

```go
kind, sdkSource := deriveClientKind(r, traceCfg)
ctx = context.WithValue(ctx, ctxClientKind, kind)
ctx = context.WithValue(ctx, ctxClientBucket, types.MapClientKindToBucket(kind))
```

- [ ] **Step 4: Run focused tests**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run 'TestTraceMiddleware_WritesClientBucket|TestClientBucketFromContext_Default' -v`
Expected: PASS.

- [ ] **Step 5: Run full proxy package**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/trace_middleware.go internal/proxy/trace_middleware_test.go
git commit -m "feat(proxy): TraceMiddleware writes ctxClientBucket alongside ctxClientKind

Adds the 5-value bucket projection of the existing client_kind onto the
request context, plus ClientBucketFromContext getter that returns
ClientBucketOther for any miss.

No production code path reads the new context value yet — Tasks 6 + 7
wire those consumers in.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Router refactor — Match signature + weighted specificity + MatrixGlobal extension

**Files:**
- Modify: `internal/proxy/router_engine.go` (Match signature; matchesGlobalRoute predicate; specificity sort; MatrixCell struct; MatrixGlobal signature + body)
- Modify: `internal/proxy/router_engine_test.go` (update 9 existing `r.Match(...)` call sites; rewrite `TestRouter_MatrixGlobal`; add 5 new tests)
- Modify: `internal/proxy/executor.go` (one-line caller update at executor.go:253)

**Interfaces:**
- Consumes: `ClientBucketFromContext` (Task 5), `types.Route.Clients` (Task 4).
- Produces:
  ```go
  func (r *Router) Match(projectID, model, kind, client string) (*resolvedGroup, error)

  type MatrixCell struct {
      Model           string
      Kind            string
      Client          string   // NEW — the bucket this cell was resolved for
      UpstreamGroupID string
      RouteID         string
      MatchPriority   int
      Clients         []string // NEW — verbatim from the winning route
  }

  func (r *Router) MatrixGlobal(models []string, filterClient string) []MatrixCell
  ```

This task lands as one commit because the signature break propagates through ~10 call sites; staging it would leave a window of red builds with no shipping value.

#### Algorithm recap

Match collects every route that matches `(projectID, model, kind, client)`, scores each by weighted specificity, and picks the best with a deterministic tiebreak:

```
spec = (project_id_specific ? 10 : 0)
     + (clients_specific    ?  1 : 0)
sort: spec desc, MatchPriority desc, ID asc
take head
```

Weights 10/1 are lexicographic: project trumps client; `MatchPriority desc` decides within a spec bucket; `ID asc` is the deterministic floor.

`matchesGlobalRoute` is the **shared** predicate used by both Match and MatrixGlobal (drift between them was a defect fixed in the prior route-matrix PR). The new `Clients` clause is added to the predicate; both callers benefit automatically.

- [ ] **Step 1: Extend `matchesGlobalRoute` predicate**

In `internal/proxy/router_engine.go`, find the existing `matchesGlobalRoute` (around line 221) and add one clause:

```go
func matchesGlobalRoute(route types.Route, projectID, model, kind, client string) bool {
	if route.Status != "active" {
		return false
	}
	if route.ProjectID != projectID {
		return false
	}
	if !slices.Contains(route.ModelNames, model) {
		return false
	}
	if !slices.Contains(route.RequestKinds, kind) {
		return false
	}
	if len(route.Clients) > 0 && !slices.Contains(route.Clients, client) {
		return false
	}
	return true
}
```

- [ ] **Step 2: Replace `Router.Match` body with the scored algorithm**

Replace the existing `Match` (around line 240) with:

```go
// Match finds the upstream group for a request, scoring all eligible
// routes by weighted specificity (project 10, clients 1) then
// MatchPriority desc with ID asc as the final deterministic tiebreak.
// The shared matchesGlobalRoute predicate keeps Match and MatrixGlobal
// aligned on what "eligible" means.
//
// Specificity weights are lexicographic by construction: project-scoped
// routes always beat global ones; among same-project routes,
// client-specific beats client-agnostic; MatchPriority breaks further
// ties; ID asc is the deterministic floor.
func (r *Router) Match(projectID, model, kind, client string) (*resolvedGroup, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	type candidate struct {
		route *types.Route
		spec  int
	}
	var best *candidate

	consider := func(route *types.Route) {
		if _, ok := r.groups[route.UpstreamGroupID]; !ok {
			// Mirror the prior "if g, ok := r.groups[...]; ok" fall-through.
			return
		}
		spec := 0
		if route.ProjectID != "" {
			spec += 10
		}
		if len(route.Clients) > 0 {
			spec += 1
		}
		if best == nil {
			best = &candidate{route: route, spec: spec}
			return
		}
		if spec > best.spec ||
			(spec == best.spec && route.MatchPriority > best.route.MatchPriority) ||
			(spec == best.spec && route.MatchPriority == best.route.MatchPriority && route.ID < best.route.ID) {
			best = &candidate{route: route, spec: spec}
		}
	}

	for i := range r.routes {
		route := &r.routes[i]
		// Project-scoped pass.
		if matchesGlobalRoute(*route, projectID, model, kind, client) {
			consider(route)
			continue
		}
		// Global fallback pass.
		if matchesGlobalRoute(*route, "", model, kind, client) {
			consider(route)
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no route configured for model %s on endpoint %s (client=%s)",
			model, kind, client)
	}
	return r.groups[best.route.UpstreamGroupID], nil
}
```

The error message includes `client` so over-narrow route definitions are diagnosable. The pre-sort of `r.routes` by `MatchPriority desc` in `buildMaps` is no longer load-bearing for Match, but keep it — `ListRoutes` admin endpoints still rely on it.

- [ ] **Step 3: Extend `MatrixCell` struct**

Find the `MatrixCell` declaration (around line 463) and add two informational fields:

```go
type MatrixCell struct {
	Model           string
	Kind            string
	Client          string   // bucket this cell was resolved for
	UpstreamGroupID string
	RouteID         string
	MatchPriority   int
	Clients         []string // verbatim from the winning route — informational for UI badges
}
```

- [ ] **Step 4: Rewrite `MatrixGlobal`**

Replace the existing `MatrixGlobal` (around line 484) with:

```go
// MatrixGlobal walks every (model, kind, client) 3-tuple over the
// supplied models, the full AllRequestKinds set, and the 5
// ClientBuckets, returning one MatrixCell for each tuple that resolves
// under the global-route branch of Match (projectID == ""). Unrouted
// tuples are omitted (sparse result).
//
// filterClient: when non-empty, restrict the client axis to that single
// bucket; the returned cells carry the filter value in their Client
// field. Empty means iterate over all 5 buckets.
//
// Rules MUST mirror Match exactly:
//   - matchesGlobalRoute predicate (shared)
//   - weighted specificity scoring (clients 1; project does NOT apply
//     here since this is the global pass)
//   - missing group → skip and keep walking
func (r *Router) MatrixGlobal(models []string, filterClient string) []MatrixCell {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(models) == 0 {
		return nil
	}

	clients := types.AllClientBuckets
	if filterClient != "" {
		clients = []string{filterClient}
	}

	out := make([]MatrixCell, 0, len(models)*len(types.AllRequestKinds))
	for _, model := range models {
		for _, kind := range types.AllRequestKinds {
			for _, client := range clients {
				var best *types.Route
				var bestSpec int
				for i := range r.routes {
					route := &r.routes[i]
					if !matchesGlobalRoute(*route, "", model, kind, client) {
						continue
					}
					if _, ok := r.groups[route.UpstreamGroupID]; !ok {
						continue
					}
					spec := 0
					if len(route.Clients) > 0 {
						spec += 1
					}
					if best == nil ||
						spec > bestSpec ||
						(spec == bestSpec && route.MatchPriority > best.MatchPriority) ||
						(spec == bestSpec && route.MatchPriority == best.MatchPriority && route.ID < best.ID) {
						best = route
						bestSpec = spec
					}
				}
				if best == nil {
					continue
				}
				out = append(out, MatrixCell{
					Model:           model,
					Kind:            kind,
					Client:          client,
					UpstreamGroupID: best.UpstreamGroupID,
					RouteID:         best.ID,
					MatchPriority:   best.MatchPriority,
					Clients:         best.Clients,
				})
			}
		}
	}
	return out
}
```

Without a filter, the iteration is `models × 8 kinds × 5 clients`. For 100 active models that's 4 000 inner loops, each scanning `len(r.routes)` candidates — single-digit milliseconds at typical route counts.

- [ ] **Step 5: Update the executor caller + add `RequestContext.ClientBucket`**

In `internal/proxy/executor.go`:

(a) Add the `ClientBucket` field to `RequestContext`:

```go
type RequestContext struct {
	// ... existing fields ...
	ClientBucket string // populated in Execute() from ClientBucketFromContext(r.Context())
}
```

(b) In `Execute()`, near where `reqCtx.IsExtraUsage` is stamped (around line 240), also write:

```go
reqCtx.ClientBucket = ClientBucketFromContext(r.Context())
```

Always populate the field (not only on the extra-usage branch) — subscription requests also need it for the pricing path in Task 7.

(c) Update the `router.Match` call at line ~253:

```go
// BEFORE:
group, err := e.router.Match(reqCtx.ProjectID, reqCtx.Model, reqCtx.RequestKind)

// AFTER:
group, err := e.router.Match(reqCtx.ProjectID, reqCtx.Model, reqCtx.RequestKind, reqCtx.ClientBucket)
```

- [ ] **Step 6: Update all 9 existing `r.Match(...)` call sites in `router_engine_test.go`**

Verify line numbers before editing (they may have drifted):

```
internal/proxy/router_engine_test.go:50   r.Match("", "claude-sonnet", types.KindAnthropicMessages)
internal/proxy/router_engine_test.go:210  r.Match("", "claude-sonnet", types.KindAnthropicMessages)
internal/proxy/router_engine_test.go:345  r.Match("p", "m", types.KindOpenAIResponses)
internal/proxy/router_engine_test.go:359  r.Match("p", "m", k)
internal/proxy/router_engine_test.go:378  r.Match("p", "m", types.KindAnthropicCountTokens)
internal/proxy/router_engine_test.go:400  r.Match("p", "m", types.KindAnthropicMessages)
internal/proxy/router_engine_test.go:436  r.Match("", "gpt-5", types.KindOpenAIResponsesCompact)
internal/proxy/router_engine_test.go:451  r.Match("", "gpt-5", types.KindOpenAIResponses)
internal/proxy/router_engine_test.go:454  r.Match("", "gpt-5", types.KindOpenAIResponsesCompact)
```

Append `, types.ClientBucketOther` to each. Example:

```go
// BEFORE
g, err := r.Match("", "claude-sonnet", types.KindAnthropicMessages)

// AFTER
g, err := r.Match("", "claude-sonnet", types.KindAnthropicMessages, types.ClientBucketOther)
```

Update the existing `TestRouter_MatrixGlobal` (around line 460) — `MatrixGlobal` now takes two args:

```go
cells := r.MatrixGlobal(modelNames, "")
```

Add assertions that every cell from the unfiltered call has `Client ∈ AllClientBuckets`.

- [ ] **Step 7: Add new precedence + tiebreak + matrix tests**

Append to `internal/proxy/router_engine_test.go`:

```go
func TestRouter_Match_ClientSpecificity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upstreams := []types.Upstream{
		{ID: "up-a", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"m"}},
	}
	groups := []store.UpstreamGroupWithMembers{
		{
			UpstreamGroup: types.UpstreamGroup{ID: "grp-cc", Name: "claude-code-pool", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members:       []store.UpstreamGroupMemberDetail{{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-cc", UpstreamID: "up-a"}}},
		},
		{
			UpstreamGroup: types.UpstreamGroup{ID: "grp-any", Name: "any-pool", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members:       []store.UpstreamGroupMemberDetail{{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-any", UpstreamID: "up-a"}}},
		},
	}
	routes := []types.Route{
		{ID: "rt-cc", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			Clients: []string{types.ClientBucketClaudeCodeCLI},
			UpstreamGroupID: "grp-cc", MatchPriority: 1, Status: "active"},
		{ID: "rt-any", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			Clients: nil,
			UpstreamGroupID: "grp-any", MatchPriority: 100, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	g, err := r.Match("", "m", types.KindAnthropicMessages, types.ClientBucketClaudeCodeCLI)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if g.group.ID != "grp-cc" {
		t.Errorf("client-specific lost: group = %s, want grp-cc", g.group.ID)
	}

	g, err = r.Match("", "m", types.KindAnthropicMessages, types.ClientBucketOther)
	if err != nil {
		t.Fatalf("Match (other): %v", err)
	}
	if g.group.ID != "grp-any" {
		t.Errorf("client-agnostic fallback: group = %s, want grp-any", g.group.ID)
	}
}

func TestRouter_Match_FullPrecedence(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upstreams := []types.Upstream{
		{ID: "up", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"m"}},
	}
	mkGroup := func(id string) store.UpstreamGroupWithMembers {
		return store.UpstreamGroupWithMembers{
			UpstreamGroup: types.UpstreamGroup{ID: id, Name: id, LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members:       []store.UpstreamGroupMemberDetail{{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: id, UpstreamID: "up"}}},
		}
	}
	groups := []store.UpstreamGroupWithMembers{
		mkGroup("g-plain"), mkGroup("g-c"), mkGroup("g-p"), mkGroup("g-pc"),
	}
	routes := []types.Route{
		// All 4 match (project p, model m, kind anthropic_messages, client cc).
		{ID: "rt-plain", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "g-plain", MatchPriority: 1000, Status: "active"},
		{ID: "rt-c", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			Clients: []string{types.ClientBucketClaudeCodeCLI},
			UpstreamGroupID: "g-c", MatchPriority: 999, Status: "active"},
		{ID: "rt-p", ProjectID: "p", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "g-p", MatchPriority: 0, Status: "active"},
		{ID: "rt-pc", ProjectID: "p", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			Clients: []string{types.ClientBucketClaudeCodeCLI},
			UpstreamGroupID: "g-pc", MatchPriority: 0, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	g, err := r.Match("p", "m", types.KindAnthropicMessages, types.ClientBucketClaudeCodeCLI)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if g.group.ID != "g-pc" {
		t.Errorf("precedence broken: got %s, want g-pc (project+clients most specific)", g.group.ID)
	}
}

func TestRouter_Match_LegacyEmptyMatchesAny(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upstreams := []types.Upstream{
		{ID: "up", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"m"}},
	}
	groups := []store.UpstreamGroupWithMembers{
		{UpstreamGroup: types.UpstreamGroup{ID: "grp", Name: "grp", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp", UpstreamID: "up"}}}},
	}
	routes := []types.Route{
		{ID: "rt", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "grp", MatchPriority: 0, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	for _, c := range types.AllClientBuckets {
		g, err := r.Match("", "m", types.KindAnthropicMessages, c)
		if err != nil {
			t.Errorf("Match(client=%s): %v", c, err)
		}
		if g != nil && g.group.ID != "grp" {
			t.Errorf("Match(client=%s) = %s, want grp", c, g.group.ID)
		}
	}
}

func TestRouter_Match_DeterministicTiebreak(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upstreams := []types.Upstream{
		{ID: "up", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"m"}},
	}
	groups := []store.UpstreamGroupWithMembers{
		{UpstreamGroup: types.UpstreamGroup{ID: "grp-aaa", Name: "aaa", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-aaa", UpstreamID: "up"}}}},
		{UpstreamGroup: types.UpstreamGroup{ID: "grp-bbb", Name: "bbb", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-bbb", UpstreamID: "up"}}}},
	}
	routes := []types.Route{
		{ID: "rt-bbb", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "grp-bbb", MatchPriority: 5, Status: "active"},
		{ID: "rt-aaa", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "grp-aaa", MatchPriority: 5, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	g, err := r.Match("", "m", types.KindAnthropicMessages, types.ClientBucketOther)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if g.group.ID != "grp-aaa" {
		t.Errorf("tiebreak: got %s, want grp-aaa (ID asc wins)", g.group.ID)
	}
}

func TestRouter_MatrixGlobal_EmitsClient(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upstreams := []types.Upstream{
		{ID: "up", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"m"}},
	}
	groups := []store.UpstreamGroupWithMembers{
		{UpstreamGroup: types.UpstreamGroup{ID: "grp", Name: "grp", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp", UpstreamID: "up"}}}},
	}
	routes := []types.Route{
		{ID: "rt", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			Clients: []string{types.ClientBucketClaudeCodeCLI},
			UpstreamGroupID: "grp", MatchPriority: 1, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	cells := r.MatrixGlobal([]string{"m"}, "")
	var found bool
	for _, c := range cells {
		if c.Client == types.ClientBucketClaudeCodeCLI {
			found = true
			if c.RouteID != "rt" {
				t.Errorf("cell.RouteID = %s, want rt", c.RouteID)
			}
			if len(c.Clients) != 1 || c.Clients[0] != types.ClientBucketClaudeCodeCLI {
				t.Errorf("cell.Clients = %v, want [claude-code-cli]", c.Clients)
			}
		}
	}
	if !found {
		t.Errorf("missing winning cell in matrix; got %d cells", len(cells))
	}
}

func TestRouter_MatrixGlobal_FilterByClient(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upstreams := []types.Upstream{
		{ID: "up", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"m"}},
	}
	groups := []store.UpstreamGroupWithMembers{
		{UpstreamGroup: types.UpstreamGroup{ID: "grp", Name: "grp", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp", UpstreamID: "up"}}}},
	}
	routes := []types.Route{
		{ID: "rt", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "grp", MatchPriority: 1, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	cells := r.MatrixGlobal([]string{"m"}, types.ClientBucketClaudeCodeCLI)
	for _, c := range cells {
		if c.Client != types.ClientBucketClaudeCodeCLI {
			t.Errorf("filter leaked: cell.Client = %s", c.Client)
		}
	}
	// 1 client × 1 model × 1 kind = 1 cell.
	if len(cells) != 1 {
		t.Errorf("len(cells) = %d, want 1", len(cells))
	}
}
```

- [ ] **Step 8: Build + run tests**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/proxy/...`
Expected: all green.

- [ ] **Step 9: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/router_engine.go internal/proxy/router_engine_test.go internal/proxy/executor.go
git commit -m "feat(proxy): client routing axis with weighted specificity

Router.Match signature: (projectID, model, kind, client).
matchesGlobalRoute predicate gains a Clients clause; the single shared
predicate keeps Match and MatrixGlobal aligned. The priority-only
first-hit-wins walk becomes a weighted-specificity sort (project 10,
clients 1) with MatchPriority desc as the secondary key and route ID
asc as the deterministic tiebreak.

MatrixCell carries Client + Clients; MatrixGlobal accepts an optional
filterClient for server-side narrowing (consumed by the admin matrix
endpoint in the next task).

Executor populates reqCtx.ClientBucket from ClientBucketFromContext(ctx)
and passes it to Match — no new balance check; the
subscription/extra-usage decision remains in the RateLimit +
ExtraUsageGuard chain, exactly as today.

Tests cover client specificity, full 4-tier precedence, legacy
'empty matches any' compat, deterministic ID asc tiebreak, matrix
client emission, matrix client filter. All 9 existing r.Match call
sites updated.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Pricing resolver — Policy.ComputeCreditsForClient + executor wiring + invariants

**Files:**
- Modify: `internal/types/policy.go` (add `ComputeCreditsForClient`; convert `ComputeCreditsWithDefault` into a thin wrapper)
- Modify: `internal/types/policy_test.go` (4-level resolver tests + backward-compat invariant)
- Modify: `internal/proxy/executor.go` (switch the two subscription pricing call sites to `ComputeCreditsForClient`)
- Modify: `internal/proxy/executor_finalize_test.go` (no-regression invariant tests)

**Interfaces:**
- Consumes: `RateLimitPolicy.ClientModelCreditRates` (Task 4), `reqCtx.ClientBucket` (Task 6).
- Produces:
  ```go
  func (p *RateLimitPolicy) ComputeCreditsForClient(model, client string, catalogDefault *CreditRate, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) float64
  // ComputeCreditsWithDefault becomes a wrapper calling ComputeCreditsForClient with client=""
  ```

The hard rule: **extra-usage billing is unchanged.** This task only touches the two subscription pricing call sites (`executor.go:1090` and `:1328`). `settleExtraUsage` / `computeExtraUsageCostCredits` are NOT modified.

- [ ] **Step 1: Write the failing resolver tests**

Append to `internal/types/policy_test.go`:

```go
func TestComputeCreditsForClient_FallbackOrder(t *testing.T) {
	clientOverride := CreditRate{InputRate: 0.1, OutputRate: 0.5}
	planOverride := CreditRate{InputRate: 1, OutputRate: 5}
	catalogDef := CreditRate{InputRate: 10, OutputRate: 50}
	planDefault := CreditRate{InputRate: 100, OutputRate: 500}

	tests := []struct {
		name           string
		policy         *RateLimitPolicy
		client         string
		catalog        *CreditRate
		expectedInput  float64
		expectedOutput float64
	}{
		{
			name: "client-model override wins (step 1)",
			policy: &RateLimitPolicy{
				ClientModelCreditRates: map[string]map[string]CreditRate{
					"claude-code-cli": {"m": clientOverride},
				},
				ModelCreditRates: map[string]CreditRate{"m": planOverride, "_default": planDefault},
			},
			client: "claude-code-cli", catalog: &catalogDef,
			expectedInput: clientOverride.InputRate, expectedOutput: clientOverride.OutputRate,
		},
		{
			name: "plan model override (step 2 — different client)",
			policy: &RateLimitPolicy{
				ClientModelCreditRates: map[string]map[string]CreditRate{
					"claude-code-cli": {"m": clientOverride},
				},
				ModelCreditRates: map[string]CreditRate{"m": planOverride, "_default": planDefault},
			},
			client: "codex-cli", catalog: &catalogDef,
			expectedInput: planOverride.InputRate, expectedOutput: planOverride.OutputRate,
		},
		{
			name: "plan model override (step 2 — client absent from outer map)",
			policy: &RateLimitPolicy{
				ModelCreditRates: map[string]CreditRate{"m": planOverride, "_default": planDefault},
			},
			client: "claude-code-cli", catalog: &catalogDef,
			expectedInput: planOverride.InputRate, expectedOutput: planOverride.OutputRate,
		},
		{
			name: "plan model override (step 2 — client present but model absent)",
			policy: &RateLimitPolicy{
				ClientModelCreditRates: map[string]map[string]CreditRate{
					"claude-code-cli": {"other-model": clientOverride},
				},
				ModelCreditRates: map[string]CreditRate{"m": planOverride, "_default": planDefault},
			},
			client: "claude-code-cli", catalog: &catalogDef,
			expectedInput: planOverride.InputRate, expectedOutput: planOverride.OutputRate,
		},
		{
			name: "catalog default (step 3 — no plan overrides)",
			policy: &RateLimitPolicy{
				ModelCreditRates: map[string]CreditRate{"_default": planDefault},
			},
			client: "claude-code-cli", catalog: &catalogDef,
			expectedInput: catalogDef.InputRate, expectedOutput: catalogDef.OutputRate,
		},
		{
			name: "plan _default (step 4 — no catalog default)",
			policy: &RateLimitPolicy{
				ModelCreditRates: map[string]CreditRate{"_default": planDefault},
			},
			client: "claude-code-cli", catalog: nil,
			expectedInput: planDefault.InputRate, expectedOutput: planDefault.OutputRate,
		},
		{
			name:           "zero (step 5 — nothing matches)",
			policy:         &RateLimitPolicy{},
			client:         "claude-code-cli", catalog: nil,
			expectedInput: 0, expectedOutput: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.policy.ComputeCreditsForClient("m", tt.client, tt.catalog, 1000, 500, 0, 0)
			want := tt.expectedInput*1000 + tt.expectedOutput*500
			if math.Abs(got-want) > 0.001 {
				t.Errorf("ComputeCreditsForClient = %f, want %f", got, want)
			}
		})
	}
}

func TestComputeCreditsWithDefault_BackwardCompat(t *testing.T) {
	planOverride := CreditRate{InputRate: 1, OutputRate: 5, CacheCreationRate: 0.5, CacheReadRate: 0.1}
	catalogDef := CreditRate{InputRate: 10, OutputRate: 50}
	planDefault := CreditRate{InputRate: 100, OutputRate: 500}

	policy := &RateLimitPolicy{
		ModelCreditRates: map[string]CreditRate{"m": planOverride, "_default": planDefault},
		// ClientModelCreditRates intentionally absent.
	}

	cases := []struct {
		model           string
		in, out, cw, cr int64
		expectedFromLegacy float64
	}{
		{"m", 1000, 500, 200, 100,
			planOverride.InputRate*1000 + planOverride.OutputRate*500 + planOverride.CacheCreationRate*200 + planOverride.CacheReadRate*100},
		{"unknown", 1000, 500, 0, 0,
			catalogDef.InputRate*1000 + catalogDef.OutputRate*500},
	}

	for _, c := range cases {
		want := c.expectedFromLegacy
		gotLegacy := policy.ComputeCreditsWithDefault(c.model, &catalogDef, c.in, c.out, c.cw, c.cr)
		if math.Abs(gotLegacy-want) > 0.001 {
			t.Errorf("ComputeCreditsWithDefault(%s) = %f, want %f", c.model, gotLegacy, want)
		}
		gotNew := policy.ComputeCreditsForClient(c.model, "", &catalogDef, c.in, c.out, c.cw, c.cr)
		if math.Abs(gotNew-want) > 0.001 {
			t.Errorf("ComputeCreditsForClient(%s, \"\") = %f, want %f", c.model, gotNew, want)
		}
		gotAnyClient := policy.ComputeCreditsForClient(c.model, "claude-code-cli", &catalogDef, c.in, c.out, c.cw, c.cr)
		if math.Abs(gotAnyClient-want) > 0.001 {
			t.Errorf("ComputeCreditsForClient(%s, \"claude-code-cli\") with absent client map = %f, want %f", c.model, gotAnyClient, want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /root/coding/modelserver && go test ./internal/types/ -run 'TestComputeCreditsForClient_FallbackOrder|TestComputeCreditsWithDefault_BackwardCompat' -v`
Expected: build error — `ComputeCreditsForClient` undefined.

- [ ] **Step 3: Implement `ComputeCreditsForClient` + rework `ComputeCreditsWithDefault` as a wrapper**

In `internal/types/policy.go`, rework the existing entry points:

```go
// ComputeCredits calculates credits using only the policy's own rate map.
// Prefer ComputeCreditsForClient so the client-specific overlay and
// catalog default can act as fallbacks between the plan override and the
// plan's "_default".
func (p *RateLimitPolicy) ComputeCredits(model string, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) float64 {
	return p.ComputeCreditsForClient(model, "", nil, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens)
}

// ComputeCreditsWithDefault is a thin wrapper around ComputeCreditsForClient
// for callers that don't (yet) thread the client bucket through. Passing
// client="" makes resolver step 1 (per-client override) always miss; the
// remaining steps are identical to the pre-refactor behavior.
func (p *RateLimitPolicy) ComputeCreditsWithDefault(model string, catalogDefault *CreditRate, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) float64 {
	return p.ComputeCreditsForClient(model, "", catalogDefault, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens)
}

// ComputeCreditsForClient resolves a credit rate in the order:
//   1. policy.ClientModelCreditRates[client][model] — per-client per-model override
//   2. policy.ModelCreditRates[model]                — existing plan override
//   3. catalogDefault                                — catalog per-model truth
//   4. policy.ModelCreditRates["_default"]           — plan-wide safety net
//   5. zero rate                                     — no billing
//
// IMPORTANT: this function is for SUBSCRIPTION consumption only.
// Extra-usage billing bypasses it entirely via settleExtraUsage →
// computeExtraUsageCostCredits, which reads catalog DefaultCreditRate
// directly. Per-client overrides have no effect on extra-usage charges
// by design.
func (p *RateLimitPolicy) ComputeCreditsForClient(model, client string, catalogDefault *CreditRate, inputTokens, outputTokens, cacheCreationTokens, cacheReadTokens int64) float64 {
	var rate CreditRate
	var ok bool

	// Step 1: per-client per-model override.
	if client != "" {
		if perClient, found := p.ClientModelCreditRates[client]; found {
			if r, found := perClient[model]; found {
				rate, ok = r, true
			}
		}
	}

	// Step 2: plan model override.
	if !ok {
		if r, found := p.ModelCreditRates[model]; found {
			rate, ok = r, true
		}
	}

	// Step 3: catalog default.
	if !ok && catalogDefault != nil {
		rate, ok = *catalogDefault, true
	}

	// Step 4: plan _default.
	if !ok {
		if r, found := p.ModelCreditRates["_default"]; found {
			rate, ok = r, true
		}
	}

	// Step 5: zero.
	if !ok {
		return 0
	}

	rate = ApplyLongContextCreditRate(rate, inputTokens+cacheCreationTokens+cacheReadTokens)
	return rate.InputRate*float64(inputTokens) +
		rate.OutputRate*float64(outputTokens) +
		rate.CacheCreationRate*float64(cacheCreationTokens) +
		rate.CacheReadRate*float64(cacheReadTokens)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /root/coding/modelserver && go test ./internal/types/ -run 'TestComputeCredits' -v`
Expected: all PASS, including the existing `TestComputeCredits`, `TestComputeCreditsNoRates`, `TestComputeCreditsWithDefault_FallbackOrder` — they go through the wrapper now and must produce identical numbers.

- [ ] **Step 5: Switch the executor subscription pricing call sites**

In `internal/proxy/executor.go`, both subscription pricing calls (around lines 1090 and 1328) currently read:

```go
credits = reqCtx.Policy.ComputeCreditsWithDefault(model, e.catalogDefaultRate(model),
    metrics.InputTokens, metrics.OutputTokens, metrics.CacheCreationTokens, metrics.CacheReadTokens)
```

Update each to thread `reqCtx.ClientBucket` (already stamped in Task 6):

```go
credits = reqCtx.Policy.ComputeCreditsForClient(model, reqCtx.ClientBucket, e.catalogDefaultRate(model),
    metrics.InputTokens, metrics.OutputTokens, metrics.CacheCreationTokens, metrics.CacheReadTokens)
```

(The second site uses `respMetrics` — keep that name; only the function name and the new `client` arg change.)

- [ ] **Step 6: Add no-regression invariant tests**

Append to `internal/proxy/executor_finalize_test.go`:

```go
// TestExecutorFinalize_Subscription_NoClientOverridesIsByteIdentical
// is the pricing-path no-regression invariant. For any plan that does
// NOT define ClientModelCreditRates, the executor's subscription
// pricing credit number must equal what Policy.ComputeCreditsWithDefault
// would have produced before this PR.
func TestExecutorFinalize_Subscription_NoClientOverridesIsByteIdentical(t *testing.T) {
	policy := &types.RateLimitPolicy{
		ModelCreditRates: map[string]types.CreditRate{
			"claude-sonnet-4": {InputRate: 3, OutputRate: 15, CacheCreationRate: 3.75, CacheReadRate: 0.3},
			"_default":        {InputRate: 1, OutputRate: 5},
		},
	}
	catalog := &types.CreditRate{InputRate: 5, OutputRate: 25}
	in, out, cw, cr := int64(1000), int64(500), int64(200), int64(100)

	baseline := policy.ComputeCreditsWithDefault("claude-sonnet-4", catalog, in, out, cw, cr)

	for _, c := range types.AllClientBuckets {
		got := policy.ComputeCreditsForClient("claude-sonnet-4", c, catalog, in, out, cw, cr)
		if math.Abs(got-baseline) > 0.001 {
			t.Errorf("client=%s: got %f, baseline %f — resolver step 2+ regressed", c, got, baseline)
		}
	}
	if got := policy.ComputeCreditsForClient("claude-sonnet-4", "", catalog, in, out, cw, cr); math.Abs(got-baseline) > 0.001 {
		t.Errorf("client=\"\": got %f, baseline %f", got, baseline)
	}
}

// TestExecutorFinalize_ExtraUsage_PricingPathUnchanged asserts the
// extra-usage cost computation is untouched by this PR.
func TestExecutorFinalize_ExtraUsage_PricingPathUnchanged(t *testing.T) {
	m := &types.Model{
		Name: "claude-sonnet-4",
		DefaultCreditRate: &types.CreditRate{
			InputRate: 3, OutputRate: 15, CacheCreationRate: 3.75, CacheReadRate: 0.3,
		},
	}
	usage := types.TokenUsage{InputTokens: 1000, OutputTokens: 500, CacheCreationTokens: 200, CacheReadTokens: 100}

	cost, err := computeExtraUsageCostCredits(m, usage)
	if err != nil {
		t.Fatalf("computeExtraUsageCostCredits: %v", err)
	}
	wantCredits := m.DefaultCreditRate.InputRate*float64(usage.InputTokens) +
		m.DefaultCreditRate.OutputRate*float64(usage.OutputTokens) +
		m.DefaultCreditRate.CacheCreationRate*float64(usage.CacheCreationTokens) +
		m.DefaultCreditRate.CacheReadRate*float64(usage.CacheReadTokens)
	wantInt64 := int64(math.Ceil(wantCredits))
	if cost != wantInt64 {
		t.Errorf("extra-usage cost = %d, want %d (catalog default path must be unchanged)", cost, wantInt64)
	}
}
```

- [ ] **Step 7: Build + run tests**

Run:
```bash
cd /root/coding/modelserver
go build ./...
go test ./internal/types/...
go test ./internal/proxy/...
```
Expected: all green.

- [ ] **Step 8: Commit**

```bash
cd /root/coding/modelserver
git add internal/types/policy.go internal/types/policy_test.go \
        internal/proxy/executor.go internal/proxy/executor_finalize_test.go
git commit -m "feat(types,proxy): per-client subscription pricing + no-regression invariants

Policy.ComputeCreditsForClient resolves credit rates in 5 steps:
  1. client_model_credit_rates[client][model]
  2. model_credit_rates[model]
  3. catalogDefault
  4. model_credit_rates['_default']
  5. zero

ComputeCreditsWithDefault becomes a thin wrapper that calls the new
function with client=\"\". Every existing call site keeps working
unchanged.

Executor's two subscription pricing sites now thread reqCtx.ClientBucket
(stamped in Execute() in the previous task). Per-client overlay only
affects subscription consumption; extra-usage path is unchanged.

Tests cover the 4-level fallback exhaustively, plus invariants:
  - wrapper matches new entry point with client=\"\" and with any
    real client when ClientModelCreditRates is absent
  - executor subscription pricing produces identical numbers across
    all 5 ClientBuckets when ClientModelCreditRates is absent
  - extra-usage cost goes through catalog default rate, untouched

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Admin API — route validation + new GET endpoint + matrix filter + plan allow-list

**Files:**
- Modify: `internal/admin/handle_routing_routes.go` (extend create/update with `clients`; add `handleListClientBuckets`)
- Modify: `internal/admin/handle_routing_matrix.go` (add `?client=` query param; extend `matrixCellOut` with `client` + `clients`)
- Modify: `internal/admin/handle_routing_matrix_test.go` (filter tests)
- Modify: `internal/admin/handle_plans.go` (allow-list accepts `client_model_credit_rates`)
- Modify: `internal/admin/routes.go` (register `GET /routing/clients`)

**Interfaces:**
- Consumes: `types.AllClientBuckets` + `types.IsValidClientBucket` (Task 1); extended `types.Route` (Task 4); extended `Router.MatrixGlobal(models, filterClient)` + `MatrixCell` (Task 6); `types.Plan.ClientModelCreditRates` (Task 4).
- Produces:
  - `POST /api/v1/routing/routes` and `PUT /api/v1/routing/routes/{id}` accept `clients []string`.
  - `GET /api/v1/routing/clients` → `{"data": [...AllClientBuckets...]}`.
  - `GET /api/v1/routing/matrix` accepts `?client=`; each cell carries `client` + `clients`.
  - `PUT /api/v1/plans/{slug}` body's `client_model_credit_rates` field round-trips.

- [ ] **Step 1: Extend `handleCreateRoutingRoute`**

In `internal/admin/handle_routing_routes.go`, add the new field to the body struct (around line 44):

```go
var body struct {
    ProjectID       string            `json:"project_id"`
    ModelNames      []string          `json:"model_names"`
    RequestKinds    []string          `json:"request_kinds"`
    Clients         []string          `json:"clients"`        // NEW
    UpstreamGroupID string            `json:"upstream_group_id"`
    MatchPriority   int               `json:"match_priority"`
    Conditions      map[string]string `json:"conditions"`
    Status          string            `json:"status"`
}
```

After the existing `request_kinds` validation block, add:

```go
for _, c := range body.Clients {
    if !types.IsValidClientBucket(c) {
        writeError(w, http.StatusBadRequest, "bad_request", "invalid client: "+c)
        return
    }
}
```

Update the `route := &types.Route{...}` construction:

```go
route := &types.Route{
    ProjectID:       body.ProjectID,
    ModelNames:      canonical,
    RequestKinds:    body.RequestKinds,
    Clients:         body.Clients,                 // NEW
    UpstreamGroupID: body.UpstreamGroupID,
    MatchPriority:   body.MatchPriority,
    Conditions:      body.Conditions,
    Status:          status,
}
```

- [ ] **Step 2: Extend `handleUpdateRoutingRoute`**

Find the field allow-list loop (around line 110-160) and extend:

```go
for _, field := range []string{"project_id", "model_names", "request_kinds", "clients", "upstream_group_id", "match_priority", "conditions", "status"} {
    if v, ok := body[field]; ok {
        switch field {
        // ... existing cases ...
        case "clients":
            clients, ok := toStringSlice(v)
            if !ok {
                writeError(w, http.StatusBadRequest, "bad_request", "clients must be an array of strings")
                return
            }
            for _, c := range clients {
                if !types.IsValidClientBucket(c) {
                    writeError(w, http.StatusBadRequest, "bad_request", "invalid client: "+c)
                    return
                }
            }
            v = clients
        }
        updates[field] = v
    }
}
```

- [ ] **Step 3: Add the list endpoint**

In `internal/admin/handle_routing_routes.go`, near `handleListRequestKinds`:

```go
func handleListClientBuckets() http.HandlerFunc {
    return func(w http.ResponseWriter, _ *http.Request) {
        writeData(w, http.StatusOK, types.AllClientBuckets)
    }
}
```

In `internal/admin/routes.go`, find the `r.Route("/routing", ...)` block (around line 292) and register:

```go
r.Route("/routing", func(r chi.Router) {
    // ... existing route registrations ...
    r.Get("/request-kinds", handleListRequestKinds())
    r.Get("/clients", handleListClientBuckets())         // NEW
    r.Get("/matrix", handleRoutingMatrix(st, router))
})
```

- [ ] **Step 4: Extend the matrix endpoint**

In `internal/admin/handle_routing_matrix.go`, extend `matrixCellOut`:

```go
type matrixCellOut struct {
    Model             string   `json:"model"`
    Kind              string   `json:"kind"`
    Client            string   `json:"client,omitempty"`     // NEW
    UpstreamGroupID   string   `json:"upstream_group_id"`
    UpstreamGroupName string   `json:"upstream_group_name"`
    RouteID           string   `json:"route_id"`
    MatchPriority     int      `json:"match_priority"`
    Clients           []string `json:"clients,omitempty"`    // NEW
}
```

Extend `handleRoutingMatrixWithLister`:

```go
func handleRoutingMatrixWithLister(
    listModels func(string) ([]types.Model, error),
    router *proxy.Router,
) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        filterClient := r.URL.Query().Get("client")
        if filterClient != "" && !types.IsValidClientBucket(filterClient) {
            writeError(w, http.StatusBadRequest, "bad_request", "invalid client: "+filterClient)
            return
        }

        models, err := listModels(types.ModelStatusActive)
        if err != nil {
            writeError(w, http.StatusInternalServerError, "internal", "failed to list models")
            return
        }

        modelNames := make([]string, 0, len(models))
        for _, m := range models {
            modelNames = append(modelNames, m.Name)
        }
        sort.Strings(modelNames)

        cells := router.MatrixGlobal(modelNames, filterClient)

        names := router.SnapshotGroupNames()
        out := matrixResponse{
            Models: modelNames,
            Kinds:  types.AllRequestKinds,
            Cells:  make([]matrixCellOut, 0, len(cells)),
        }
        for _, c := range cells {
            out.Cells = append(out.Cells, matrixCellOut{
                Model:             c.Model,
                Kind:              c.Kind,
                Client:            c.Client,
                UpstreamGroupID:   c.UpstreamGroupID,
                UpstreamGroupName: names[c.UpstreamGroupID],
                RouteID:           c.RouteID,
                MatchPriority:     c.MatchPriority,
                Clients:           c.Clients,
            })
        }
        writeData(w, http.StatusOK, out)
    }
}
```

- [ ] **Step 5: Add matrix integration tests**

In `internal/admin/handle_routing_matrix_test.go`:

```go
func TestHandleRoutingMatrix_FilterByClient(t *testing.T) {
    h := handleRoutingMatrixWithLister(fakeListModels, fakeRouter)

    req := httptest.NewRequest(http.MethodGet, "/routing/matrix?client=claude-code-cli", nil)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status = %d, want 200", rec.Code)
    }
    var resp struct {
        Data struct {
            Cells []matrixCellOut `json:"cells"`
        } `json:"data"`
    }
    if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
        t.Fatalf("decode: %v", err)
    }
    for _, c := range resp.Data.Cells {
        if c.Client != types.ClientBucketClaudeCodeCLI {
            t.Errorf("filter leaked: cell.Client = %q", c.Client)
        }
    }
}

func TestHandleRoutingMatrix_FilterRejectsInvalid(t *testing.T) {
    h := handleRoutingMatrixWithLister(fakeListModels, fakeRouter)

    req := httptest.NewRequest(http.MethodGet, "/routing/matrix?client=bogus", nil)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)

    if rec.Code != http.StatusBadRequest {
        t.Errorf("invalid client: status = %d, want 400", rec.Code)
    }
}
```

Reuse `fakeListModels` / `fakeRouter` from the existing happy-path test.

- [ ] **Step 6: Extend the plan admin allow-list**

In `internal/admin/handle_plans.go`'s `handleUpdatePlan` (around line 169), add a parallel block to the existing `model_credit_rates` handler:

```go
if v, ok := body["client_model_credit_rates"]; ok {
    m, ok := v.(map[string]interface{})
    if !ok && v != nil {
        writeError(w, http.StatusBadRequest, "bad_request", "client_model_credit_rates must be an object")
        return
    }
    for client := range m {
        if !types.IsValidClientBucket(client) {
            writeError(w, http.StatusBadRequest, "bad_request", "invalid client bucket in client_model_credit_rates: "+client)
            return
        }
    }
    b, err := json.Marshal(v)
    if err != nil {
        writeError(w, http.StatusBadRequest, "bad_request", "client_model_credit_rates marshal failed")
        return
    }
    updates["client_model_credit_rates"] = b
}
```

If `handleCreatePlan` lists `model_credit_rates` in its body struct, add `ClientModelCreditRates` next to it and pass it through to the `Plan` constructor (the store `CreatePlan` from Task 4 already accepts it).

- [ ] **Step 7: Build + run admin tests**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/admin/...`
Expected: green.

- [ ] **Step 8: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_routing_routes.go internal/admin/handle_routing_matrix.go \
        internal/admin/handle_routing_matrix_test.go internal/admin/handle_plans.go \
        internal/admin/routes.go
git commit -m "feat(admin): client admin API

Route create/update accept clients []string; values validated against
types.AllClientBuckets. Empty array is valid (= match any).

New GET /api/v1/routing/clients returns the bucket catalog so the
dashboard can dropdown without compiling the enum into the FE bundle.

Matrix endpoint accepts ?client= query param for server-side filter;
each cell response gains client + clients JSON fields.

Plan upsert allow-list accepts client_model_credit_rates as a top-level
JSON field; top-level keys validated against AllClientBuckets.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Dashboard — Routes page column + dialog selector + Matrix filter

**Files:**
- Modify: `dashboard/src/api/types.ts` (extend `RoutingRoute` + `RoutingMatrixCell`)
- Modify: `dashboard/src/api/upstreams.ts` (add `useClientBuckets`; extend `useRoutingMatrix` to accept optional `{ client? }`)
- Modify: `dashboard/src/pages/admin/RoutesPage.tsx` (one new table column; one new toggle-button row in the Create/Edit dialog)
- Modify: `dashboard/src/pages/admin/RoutesMatrixView.tsx` (one filter dropdown at the top; URL state)

**Interfaces:**
- Consumes: admin endpoints from Task 8 (`GET /routing/clients`, the extended `GET /routing/matrix`, the route create/update accepting `clients`).
- Produces:
  - `useClientBuckets(): UseQueryResult<DataResponse<string[]>>` — mirrors `useRequestKinds`.
  - `useRoutingMatrix(opts?: { client?: string })` — query key includes the filter.
  - `RoutingRoute` TS type grows `clients: string[]`.
  - `RoutingMatrixCell` TS type grows `client?: string`, `clients?: string[]`.
  - Dashboard Routes page shows the new column + selector; Matrix view has Client filter dropdown.

No new test framework — verification is `pnpm exec tsc -b && pnpm build` + manual smoke (Step 5).

- [ ] **Step 1: Extend TS types**

In `dashboard/src/api/types.ts`:

```ts
export interface RoutingRoute {
  id: string;
  project_id?: string;
  model_names: string[];
  request_kinds: string[];
  clients: string[];          // NEW — ClientBucket values; [] = match any
  upstream_group_id: string;
  match_priority: number;
  conditions?: Record<string, string>;
  status: string;
  created_at: string;
  updated_at: string;
}

export interface RoutingMatrixCell {
  model: string;
  kind: string;
  client?: string;             // NEW
  upstream_group_id: string;
  upstream_group_name: string;
  route_id: string;
  match_priority: number;
  clients?: string[];          // NEW
}
```

- [ ] **Step 2: Add `useClientBuckets`; extend `useRoutingMatrix`**

In `dashboard/src/api/upstreams.ts`, add next to `useRequestKinds`:

```ts
export function useClientBuckets() {
  return useQuery({
    queryKey: ["routing-clients"],
    queryFn: () => api.get<DataResponse<string[]>>("/api/v1/routing/clients"),
    staleTime: Infinity,
  });
}
```

Replace `useRoutingMatrix` with the filter-aware version:

```ts
export interface RoutingMatrixOpts {
  client?: string;
}

export function useRoutingMatrix(opts: RoutingMatrixOpts = {}) {
  const client = opts.client ?? "";
  const qs = new URLSearchParams();
  if (client) qs.set("client", client);
  const url = qs.toString()
    ? `/api/v1/routing/matrix?${qs.toString()}`
    : "/api/v1/routing/matrix";
  return useQuery({
    queryKey: ["routing-matrix", client],
    queryFn: () => api.get<DataResponse<RoutingMatrix>>(url),
  });
}
```

If the existing hook uses a different React Query option set (e.g. `keepPreviousData: true`), preserve it.

- [ ] **Step 3: Extend `RoutesPage.tsx`**

(a) Form state (around line 68):

```tsx
const [form, setForm] = useState({
  model_names: [] as string[],
  request_kinds: ["anthropic_messages"] as string[],
  clients: [] as string[],          // NEW
  upstream_group_id: "",
  match_priority: 0,
  status: "active",
  project_id: "",
});
```

(b) Add the table column. Insert between Endpoints (request_kinds) and Upstream Group:

```tsx
{
  header: "Clients",
  cell: (r: RoutingRoute) =>
    r.clients?.length ? (
      <div className="flex flex-wrap gap-1">
        {r.clients.map((c) => (
          <Badge key={c} variant="outline">{c}</Badge>
        ))}
      </div>
    ) : (
      <span className="text-muted-foreground">Any</span>
    ),
},
```

(c) Load the bucket list and add the toggle-button selector row in the Create/Edit dialog (mirrors `request_kinds`):

```tsx
const { data: clientsList } = useClientBuckets();
const clientBuckets = clientsList?.data ?? [];
```

```tsx
<div className="space-y-2">
  <Label>Clients (optional)</Label>
  <div className="flex flex-wrap gap-2">
    {clientBuckets.map((c) => {
      const selected = form.clients.includes(c);
      return (
        <Button
          key={c}
          type="button"
          size="sm"
          variant={selected ? "default" : "outline"}
          onClick={() =>
            setForm((p) => ({
              ...p,
              clients: selected
                ? p.clients.filter((x) => x !== c)
                : [...p.clients, c],
            }))
          }
        >
          {c}
        </Button>
      );
    })}
  </div>
  <p className="text-xs text-muted-foreground">
    When set, the route only matches requests from one of these client
    buckets. Leave empty to match any client.
  </p>
</div>
```

(d) Reset / load helpers:

```tsx
function openCreate() {
  setForm({
    model_names: [],
    request_kinds: ["anthropic_messages"],
    clients: [],          // NEW
    upstream_group_id: "",
    match_priority: 0,
    status: "active",
    project_id: "",
  });
  setEditingId(null);
  setDialogOpen(true);
}

function openEdit(route: RoutingRoute) {
  setForm({
    model_names: [...(route.model_names ?? [])],
    request_kinds: [...(route.request_kinds ?? [])],
    clients: [...(route.clients ?? [])],            // NEW
    upstream_group_id: route.upstream_group_id,
    match_priority: route.match_priority,
    status: route.status,
    project_id: route.project_id ?? "",
  });
  setEditingId(route.id);
  setDialogOpen(true);
}
```

(e) Submit handler — payload includes `clients`. If the existing handler spreads `...form`, this is automatic.

- [ ] **Step 4: Extend `RoutesMatrixView.tsx`**

```tsx
const [params, setParams] = useSearchParams();
const clientFilter = params.get("client") ?? "";

const { data: clientsList } = useClientBuckets();

const { data, isLoading, error } = useRoutingMatrix({
  client: clientFilter,
});
```

Add the filter dropdown above the matrix table:

```tsx
<div className="mb-4">
  <div className="space-y-1">
    <Label className="text-xs">Client</Label>
    <Select
      value={clientFilter || "all"}
      onValueChange={(v) => {
        const next = new URLSearchParams(params);
        if (v === "all") next.delete("client");
        else next.set("client", v);
        setParams(next, { replace: true });
      }}
    >
      <SelectTrigger className="w-[180px]">
        <SelectValue placeholder="All clients" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="all">All clients</SelectItem>
        {(clientsList?.data ?? []).map((c) => (
          <SelectItem key={c} value={c}>{c}</SelectItem>
        ))}
      </SelectContent>
    </Select>
  </div>
</div>
```

- [ ] **Step 5: Type-check, build, manual smoke**

```bash
cd /root/coding/modelserver/dashboard
pnpm exec tsc -b
pnpm build
```

Expected: both green.

**Manual smoke checklist:**

1. `/admin/routes` — the new **Clients** column appears between Endpoints and Upstream Group. Existing routes (pre-migration) render `Any`.
2. Click **+ Add Route**. Pick a model + endpoint + upstream group. Toggle `claude-code-cli`. Save. Row shows `claude-code-cli` badge.
3. Edit that route. The Clients block shows `claude-code-cli` selected. Untoggle, save. Row updates to `Any`.
4. Switch to Matrix tab. New filter dropdown appears at the top, defaults to "All clients". Cells render group badges.
5. Change Client filter to `claude-code-cli`. URL becomes `…?view=matrix&client=claude-code-cli`. Cells redraw.
6. Refresh the page. Dropdown restores selected value.
7. Manually navigate to `?view=matrix&client=bogus`. Backend returns 400; the matrix view shows a friendly error (or at minimum doesn't crash).

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/api/types.ts dashboard/src/api/upstreams.ts \
        dashboard/src/pages/admin/RoutesPage.tsx dashboard/src/pages/admin/RoutesMatrixView.tsx
git commit -m "feat(dashboard): client column + matrix filter

Routes table grows one column (Clients). The Create/Edit dialog gets
one new toggle-button row fed by the useClientBuckets() hook. Default
empty = 'match any', preserving legacy route semantics.

The Matrix tab gains a Client filter dropdown at the top. Filter state
is persisted in the URL (?view=matrix&client=…) so reload + back-button
preserve the slice.

No new dependencies; verified by pnpm tsc -b + pnpm build + manual
smoke list in the plan.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage**

| Spec section / requirement | Task(s) |
|---|---|
| 5 client buckets (claude-code-cli, claude-desktop, codex-cli, codex-desktop reserved, other) | Task 1 |
| `MapClientKindToBucket` projects ClientKind→bucket | Task 1 |
| codex-desktop reserved; deriver returns other | Task 1 (`TestClientBucketCodexDesktop_ReservedReturnsOther`) |
| `routes.clients` TEXT[] column with empty-array default | Task 2 |
| `plans.client_model_credit_rates` JSONB nullable column | Task 3 |
| Both migrations idempotent | Tasks 2 + 3 (explicit idempotence tests) |
| `types.Route` carries `Clients []string` | Task 4 |
| `types.Plan` carries `ClientModelCreditRates` | Task 4 |
| `Plan.ToPolicy` threads `ClientModelCreditRates` through | Task 4 |
| Store load/save round-trips all new columns | Task 4 |
| `ctxClientBucket` populated by trace middleware | Task 5 |
| `ClientBucketFromContext` getter with `ClientBucketOther` default | Task 5 |
| `Router.Match` signature `(projectID, model, kind, client)` | Task 6 |
| `matchesGlobalRoute` shared by Match and MatrixGlobal (no drift) | Task 6 |
| Empty `Clients` = match any | Task 6 (`TestRouter_Match_LegacyEmptyMatchesAny`) |
| Weighted specificity (project 10, clients 1) | Task 6 |
| Tiebreak: MatchPriority desc then ID asc | Task 6 (`TestRouter_Match_DeterministicTiebreak`) |
| 4-tier precedence: (project+clients) > project > (clients global) > plain global | Task 6 (`TestRouter_Match_FullPrecedence`) |
| `MatrixCell` carries `Client`, `Clients` | Task 6 |
| `MatrixGlobal` accepts `filterClient` | Task 6 |
| `Router.Match` error message includes `client` | Task 6 |
| Executor populates `reqCtx.ClientBucket` and passes to Match | Task 6 |
| **NO new balance check at routing time** | Task 6 (executor caller change is one new line of context read) + Global Constraints |
| **NO `billing_mode` in routing layer** (v3 invariant) | Tasks 1, 2, 4, 6, 8, 9 — billing_mode appears nowhere except as a local label in the Executor pricing branch |
| `SubscriptionEligibility` shape unchanged | (verified by absence — no task touches this struct) |
| `Policy.ComputeCreditsForClient` 5-step resolver | Task 7 |
| `ComputeCreditsWithDefault` becomes thin wrapper | Task 7 |
| Executor subscription pricing call sites switched | Task 7 |
| Subscription byte-identity invariant when overrides absent | Task 7 (`TestComputeCreditsWithDefault_BackwardCompat`, `TestExecutorFinalize_Subscription_NoClientOverridesIsByteIdentical`) |
| Extra-usage pricing path completely unchanged | Task 7 (`TestExecutorFinalize_ExtraUsage_PricingPathUnchanged`) |
| Admin route create/update accepts + validates `clients` | Task 8 |
| `GET /api/v1/routing/clients` returns AllClientBuckets | Task 8 |
| Matrix endpoint accepts `?client=` (400 on invalid) | Task 8 |
| Matrix cell response gains `client` + `clients` | Task 8 |
| Plan upsert allow-list accepts `client_model_credit_rates` | Task 8 |
| Routes List table grows Clients column | Task 9 |
| Create/Edit dialog grows clients selector | Task 9 |
| Matrix tab grows Client filter dropdown persisted to URL | Task 9 |
| `useClientBuckets` hook | Task 9 |
| `useRoutingMatrix` accepts optional filter; cache key includes it | Task 9 |

No spec requirement is unimplemented. `billing_mode` is intentionally absent from every layer above the Executor's pricing branch selection.

**2. Placeholder scan**

- No "TBD" / "implement later" / "fill in details" / "add appropriate error handling" / "similar to Task N" / "write tests for the above" patterns.
- Task 5 Step 1 notes "verify the setup lambdas against existing test fixtures" — honest scaffolding, not a placeholder; the production matchers are stable.
- Task 6 Step 6 enumerates the 9 existing `r.Match(...)` lines by line number with verbatim before/after.
- Task 7 Step 5 instructs to read `reqCtx.ClientBucket` populated in Task 6 — explicit cross-task dependency, not vague.
- Task 9 Step 5 has 7 concrete manual-smoke items.

**3. Type consistency**

- `ClientBucket*` constants — declared Task 1, consumed Task 5 (mapping), Task 6 (test default args), Task 8 (admin validator), Task 9 (TS values via `useClientBuckets`).
- `Route.Clients []string` — declared Task 4, consumed Task 4 (store load/save), Task 6 (matcher predicate), Task 8 (admin), Task 9 (TS + dashboard form state).
- `Plan.ClientModelCreditRates map[string]map[string]CreditRate` — declared Task 4, consumed Task 4 (store load/save + ToPolicy), Task 7 (resolver step 1), Task 8 (admin allow-list).
- `Router.Match(projectID, model, kind, client string)` — declared Task 6, consumed Task 6 (executor caller, 9 test sites).
- `MatrixGlobal(models []string, filterClient string)` — declared Task 6, consumed Task 8 (admin handler).
- `MatrixCell.{Client, Clients}` — declared Task 6, consumed Task 8 (`matrixCellOut` JSON struct), Task 9 (`RoutingMatrixCell` TS interface).
- `Policy.ComputeCreditsForClient(model, client string, catalogDefault *CreditRate, …) float64` — declared Task 7, consumed Task 7 (executor sites + tests).
- `RequestContext.ClientBucket string` — declared Task 6, consumed Task 6 (Match arg) and Task 7 (pricing resolver arg).
- `ctxClientBucket contextKey = "client_bucket"` — declared Task 5, consumed Task 5 (`ClientBucketFromContext`) and Task 6 transitively.
- `useClientBuckets()` / `useRoutingMatrix({client})` — declared Task 9, consumed Task 9.
- Migration column names: `clients` (Task 2 → Task 4 → Task 8); `client_model_credit_rates` (Task 3 → Task 4 → Task 8). Names match across SQL, Go struct tags, JSON keys.

No naming drift.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-28-client-aware-routing-pricing.md`.
