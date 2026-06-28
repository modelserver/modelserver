# Client-Aware Routing + Pricing — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Thread two new dimensions through the proxy: a per-request `client_bucket ∈ {claude-code-cli, claude-desktop, codex-cli, codex-desktop, other}` derived from the existing `ClientKind*` enum, and a per-request `billing_mode ∈ {subscription, extra_usage}` derived from the already-existing `reqCtx.IsExtraUsage`. Routes can optionally be scoped by both. Plans can override the credit rate per `(client, model)` for subscription consumption. Extra-usage pricing is unchanged.

**Architecture:** Two layers, one PR. Routing layer extends `Route` with `clients []string` + `billing_modes []string` (empty = match any) and `Router.Match` with a weighted-specificity tiebreak (project 100, clients 10, billing_modes 1; then `match_priority desc`; then `id asc`). Pricing layer extends `Plan` with `client_model_credit_rates map[client]map[model]CreditRate` and adds `Policy.ComputeCreditsForClient` that resolves per-client → per-model → catalog default → plan `_default`. **No new middleware, no new balance check** — `billing_mode` is just `IsExtraUsage` rendered as a string. Backward-compat invariant: plans without the new field produce identical credit counts to today; routes with empty arrays match any client/mode.

**Tech Stack:** Go 1.x, `pgx` (`pool.Begin` / `tx.Exec`), `JSONB`, stdlib `testing`. React 19, `@tanstack/react-query` v5, Tailwind v4. Two SQL migrations (056, 057). No new dependencies.

## Global Constraints

- **Spec:** `docs/superpowers/specs/2026-06-28-client-aware-routing-pricing-design.md` — re-read before each task.
- **`billing_mode` IS NOT a new balance check.** It is `reqCtx.IsExtraUsage` rendered as a string in the Executor, one line before `router.Match`. Do NOT add middleware that queries subscription balance. Do NOT change `SubscriptionEligibilityMiddleware`'s `SubscriptionEligibility{Eligible, Reason}` output shape.
- **Extra-usage pricing is unchanged.** Do NOT touch `computeExtraUsageCostCredits` or `settleExtraUsage`. Per-client overrides apply ONLY to the subscription pricing path (`executor.go:1090, :1328`).
- **Backward-compat invariants:**
  - A plan WITHOUT `client_model_credit_rates` MUST produce identical credit counts to today (resolver falls through to existing `ModelCreditRates[model]` at step 2).
  - A route with empty `clients` AND empty `billing_modes` MUST match every request (= today's behavior).
- **Client bucket values:** exactly `claude-code-cli`, `claude-desktop`, `codex-cli`, `codex-desktop`, `other`. Constants in `internal/types/client_bucket.go`.
- **Billing mode values:** exactly `subscription`, `extra_usage`. Constants in `internal/types/billing_mode.go`.
- **`ClientBucketCodexDesktop` is reserved.** `MapClientKindToBucket` returns `other` for any input today — codex-desktop will be wired when the product ships an identifiable client.
- **Specificity weights:** project=100, clients=10, billing_modes=1. Final tiebreak `ID asc`. Same code path used by `Match` AND `MatrixGlobal` via the shared `matchesGlobalRoute` predicate.
- **Migrations:** numbered 056 (routes columns) and 057 (plans column). `IF NOT EXISTS` guarded. No down step.
- **No frontend test framework** — dashboard tasks verify via `pnpm exec tsc -b && pnpm build` + manual smoke.
- **Commit message footer:** every commit ends with
  ```
  Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
  ```

## File Structure

**Backend — create:**
- `internal/types/client_bucket.go` — 5 constants + `MapClientKindToBucket` + `IsValidClientBucket` + `AllClientBuckets`.
- `internal/types/client_bucket_test.go`
- `internal/types/billing_mode.go` — 2 constants + `IsValidBillingMode` + `AllBillingModes`.
- `internal/types/billing_mode_test.go`
- `internal/store/migrations/056_route_client_billing.sql`
- `internal/store/migrations_056_test.go`
- `internal/store/migrations/057_plan_client_credit_rates.sql`
- `internal/store/migrations_057_test.go`

**Backend — modify:**
- `internal/types/route.go` — add `Clients []string` + `BillingModes []string` fields.
- `internal/types/plan.go` — add `ClientModelCreditRates map[string]map[string]CreditRate` + thread through `ToPolicy`.
- `internal/types/policy.go` — add `ClientModelCreditRates` field + `ComputeCreditsForClient(model, client, catalogDefault, …)` resolver; keep `ComputeCreditsWithDefault` as a thin wrapper.
- `internal/types/policy_test.go` — add resolver tests + backward-compat invariant test.
- `internal/store/routes.go` — extend `routeSelectCols`, `CreateRoute`, `scanRoute`, `handleUpdateRoutingRoute` field allow-list with the two new columns.
- `internal/store/plans.go` — extend `CreatePlan`, `GetPlanByID`, `GetPlanBySlug`, `ListPlans`, `ListPlansPaginated`, `ListPlansForProject`, `scanPlans`, `unmarshalPlanJSON` with the new column position.
- `internal/proxy/router_engine.go` — extend `Router.Match` signature; extend `matchesGlobalRoute`; replace priority-only sort with weighted-specificity sort; extend `MatrixGlobal` shape + add optional `client` / `billingMode` filter params; extend `MatrixCell` with `Clients []string` and `BillingModes []string`.
- `internal/proxy/router_engine_test.go` — update existing call sites; add new precedence / tiebreak / matrix-filter tests.
- `internal/proxy/trace_middleware.go` — write `ctxClientBucket` next to existing `ctxClientKind`; export `ClientBucketFromContext`.
- `internal/proxy/trace_middleware_test.go` — assert bucket is populated for every `ClientKind*`.
- `internal/proxy/executor.go` — compute `client + billingMode` one line before `router.Match`; switch the two subscription pricing call sites to `ComputeCreditsForClient(model, client, …)`.
- `internal/proxy/executor_finalize_test.go` — add no-regression invariant tests (subscription credit count identical when `ClientModelCreditRates` is absent; extra-usage credit count identical period).
- `internal/admin/handle_routing_routes.go` — accept + validate the two new fields on create/update; ensure GET-by-id and list paths return them (automatic via the struct change).
- `internal/admin/handle_routing_matrix.go` — accept `?client=`, `?billing_mode=` query params; include `clients` / `billing_modes` in each cell; pass filters into `MatrixGlobal`.
- `internal/admin/handle_routing_matrix_test.go` — extend with filter cases.
- `internal/admin/routes.go` — register two new GET endpoints: `/routing/clients` and `/routing/billing-modes`.

**Frontend — modify:**
- `dashboard/src/api/types.ts` — extend `RoutingRoute` + `RoutingMatrixCell` with new fields.
- `dashboard/src/api/upstreams.ts` — add `useClientBuckets()` and `useBillingModes()` hooks; extend `useRoutingMatrix` to accept optional `{ client?, billingMode? }`.
- `dashboard/src/pages/admin/RoutesPage.tsx` — two new table columns; two new multi-select controls in the Create/Edit dialog.
- `dashboard/src/pages/admin/RoutesMatrixView.tsx` — two filter dropdowns at the top; cells subscript with mode-specificity hint.

---

### Task 1: ClientBucket + BillingMode type primitives

**Files:**
- Create: `internal/types/client_bucket.go`
- Create: `internal/types/client_bucket_test.go`
- Create: `internal/types/billing_mode.go`
- Create: `internal/types/billing_mode_test.go`

**Interfaces:**
- Consumes: existing `ClientKind*` constants in `internal/types/extra_usage.go`.
- Produces:
  ```go
  // client_bucket.go
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

  // billing_mode.go
  type BillingMode = string
  const (
      BillingModeSubscription BillingMode = "subscription"
      BillingModeExtraUsage   BillingMode = "extra_usage"
  )
  var AllBillingModes []string
  func IsValidBillingMode(s string) bool
  ```

Pure additions. No callers yet. Task 5 wires `MapClientKindToBucket` into the trace middleware; Tasks 6-7 wire `BillingMode` constants into the executor.

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
	// Today no client_kind maps to codex-desktop — the bucket is reserved
	// for a future product. Confirm the mapping function does not return it.
	for _, k := range []string{ClientKindClaudeCode, ClientKindClaudeDesktop,
		ClientKindCodex, ClientKindOpenCode, ClientKindOpenClaw, ClientKindUnknown} {
		if got := MapClientKindToBucket(k); got == ClientBucketCodexDesktop {
			t.Errorf("ClientKind %q unexpectedly maps to codex-desktop", k)
		}
	}
}
```

Create `internal/types/billing_mode_test.go`:

```go
package types

import "testing"

func TestIsValidBillingMode(t *testing.T) {
	for _, m := range AllBillingModes {
		if !IsValidBillingMode(m) {
			t.Errorf("IsValidBillingMode(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"", "sub", "extra-usage", "subscription "} {
		if IsValidBillingMode(m) {
			t.Errorf("IsValidBillingMode(%q) = true, want false", m)
		}
	}
}

func TestAllBillingModes_ContainsTwo(t *testing.T) {
	if got := len(AllBillingModes); got != 2 {
		t.Errorf("len(AllBillingModes) = %d, want 2", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail with "undefined"**

Run: `cd /root/coding/modelserver && go test ./internal/types/ -run 'TestMapClientKindToBucket|TestIsValidClientBucket|TestAllClientBuckets|TestClientBucketCodexDesktop|TestIsValidBillingMode|TestAllBillingModes' -v`
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

- [ ] **Step 4: Implement `billing_mode.go`**

Create `internal/types/billing_mode.go`:

```go
package types

// BillingMode tags whether a request consumes the project's subscription
// or its extra-usage balance. The value is derived from reqCtx.IsExtraUsage
// in the Executor (one line before router.Match) — it is NOT computed by
// a middleware and does NOT involve a balance check at routing time. The
// authoritative balance gating lives in RateLimitMiddleware +
// ExtraUsageGuardMiddleware, exactly as today.
type BillingMode = string

const (
	BillingModeSubscription BillingMode = "subscription"
	BillingModeExtraUsage   BillingMode = "extra_usage"
)

// AllBillingModes enumerates every BillingMode constant. Used by admin
// input validation and the dashboard dropdown.
var AllBillingModes = []string{
	BillingModeSubscription,
	BillingModeExtraUsage,
}

// IsValidBillingMode reports whether s is one of the two mode values.
func IsValidBillingMode(s string) bool {
	for _, m := range AllBillingModes {
		if m == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /root/coding/modelserver && go test ./internal/types/ -run 'TestMapClientKindToBucket|TestIsValidClientBucket|TestAllClientBuckets|TestClientBucketCodexDesktop|TestIsValidBillingMode|TestAllBillingModes' -v`
Expected: all PASS.

- [ ] **Step 6: Run full types package to confirm no regressions**

Run: `cd /root/coding/modelserver && go test ./internal/types/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd /root/coding/modelserver
git add internal/types/client_bucket.go internal/types/client_bucket_test.go \
        internal/types/billing_mode.go internal/types/billing_mode_test.go
git commit -m "feat(types): ClientBucket (5) + BillingMode (2) primitives

ClientBucket is a five-value projection of the existing ClientKind*
enum used for routing and per-client pricing. MapClientKindToBucket
collapses claude-desktop, codex-cli, codex-desktop, other onto the
bucket axis; codex-desktop is reserved and always returns other today.

BillingMode is the subscription | extra_usage label that the routing
table can target. It is rendered from reqCtx.IsExtraUsage in the
Executor — this commit only defines the constants; later commits wire
them in.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Migration 056 — routes.clients + routes.billing_modes

**Files:**
- Create: `internal/store/migrations/056_route_client_billing.sql`
- Create: `internal/store/migrations_056_test.go`

**Interfaces:**
- Consumes: existing `routes` table schema (`internal/store/migrations/001_init.sql` + later).
- Produces: two new columns `routes.clients TEXT[]` and `routes.billing_modes TEXT[]`, both `NOT NULL DEFAULT '{}'`. Subsequent tasks (`types.Route` struct change, store load/save updates, `Router.Match` extension) consume these columns.

Pre-existing migration max is `055_revoke_orphaned_api_keys.sql` (merged via PR #65). 056 is the next number.

- [ ] **Step 1: Write the SQL**

Create `internal/store/migrations/056_route_client_billing.sql`:

```sql
-- 056_route_client_billing.sql
--
-- Add two routing dimensions to the routes table:
--
--   clients        — when populated, only requests whose derived
--                    ClientBucket (5 values: claude-code-cli,
--                    claude-desktop, codex-cli, codex-desktop, other)
--                    is in this list match the route.
--   billing_modes  — when populated, only requests whose billing_mode
--                    (subscription | extra_usage) is in this list match
--                    the route.
--
-- Empty array means "match any value", preserving today's behavior for
-- every existing route. Migration is therefore safe to deploy ahead of
-- the matcher upgrade — old routes simply continue to match every
-- request as they do today.
--
-- Idempotent via IF NOT EXISTS. No down step.

ALTER TABLE routes
    ADD COLUMN IF NOT EXISTS clients       TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS billing_modes TEXT[] NOT NULL DEFAULT '{}';
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

// TestMigration056_AddsRouteColumnsWithEmptyDefault asserts the migration
// adds clients + billing_modes as TEXT[] NOT NULL DEFAULT '{}', leaves
// existing rows with empty arrays, and round-trips populated values.
func TestMigration056_AddsRouteColumnsWithEmptyDefault(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Seed an upstream group so the FK on routes.upstream_group_id is satisfied.
	var groupID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO upstream_groups (name, lb_policy, status)
		VALUES ('mig056-test', 'weighted_random', 'active')
		RETURNING id`).Scan(&groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}

	// Insert a route using ONLY the pre-056 columns. The new columns
	// must accept the row via their defaults.
	var oldRouteID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO routes (model_names, request_kinds, upstream_group_id, match_priority, status)
		VALUES (ARRAY['claude-sonnet'], ARRAY[$1::text], $2, 10, 'active')
		RETURNING id`, types.KindAnthropicMessages, groupID).Scan(&oldRouteID); err != nil {
		t.Fatalf("insert old-style route: %v", err)
	}

	// Read it back; the two new columns must be present as empty arrays.
	var clients, modes []string
	if err := st.pool.QueryRow(ctx,
		`SELECT clients, billing_modes FROM routes WHERE id = $1`, oldRouteID).
		Scan(&clients, &modes); err != nil {
		t.Fatalf("select new columns: %v", err)
	}
	if len(clients) != 0 {
		t.Errorf("default clients = %v, want []", clients)
	}
	if len(modes) != 0 {
		t.Errorf("default billing_modes = %v, want []", modes)
	}

	// Insert a route WITH the new columns populated.
	var newRouteID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO routes (model_names, request_kinds, upstream_group_id, match_priority, status, clients, billing_modes)
		VALUES (ARRAY['claude-sonnet'], ARRAY[$1::text], $2, 20, 'active',
		        ARRAY['claude-code-cli','claude-desktop'], ARRAY['subscription'])
		RETURNING id`, types.KindAnthropicMessages, groupID).Scan(&newRouteID); err != nil {
		t.Fatalf("insert new-style route: %v", err)
	}
	if err := st.pool.QueryRow(ctx,
		`SELECT clients, billing_modes FROM routes WHERE id = $1`, newRouteID).
		Scan(&clients, &modes); err != nil {
		t.Fatalf("select populated columns: %v", err)
	}
	wantClients := []string{"claude-code-cli", "claude-desktop"}
	wantModes := []string{"subscription"}
	if !equalStringSlices(clients, wantClients) {
		t.Errorf("populated clients = %v, want %v", clients, wantClients)
	}
	if !equalStringSlices(modes, wantModes) {
		t.Errorf("populated billing_modes = %v, want %v", modes, wantModes)
	}
}

// TestMigration056_Idempotent asserts re-running the migration is a no-op.
func TestMigration056_Idempotent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	_, err := st.pool.Exec(ctx, `
		ALTER TABLE routes
		    ADD COLUMN IF NOT EXISTS clients       TEXT[] NOT NULL DEFAULT '{}',
		    ADD COLUMN IF NOT EXISTS billing_modes TEXT[] NOT NULL DEFAULT '{}'`)
	if err != nil {
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

If `equalStringSlices` already exists in the package (it might, from prior migration tests), drop the local copy and reuse the existing one.

- [ ] **Step 3: Run the migration test**

Run: `cd /root/coding/modelserver && TEST_DATABASE_URL=$TEST_DATABASE_URL go test ./internal/store/ -run TestMigration056 -v`

Without `TEST_DATABASE_URL` set the test prints SKIP — acceptable for local quick checks; CI exercises it. With the var set, expected: both PASS.

- [ ] **Step 4: Run the full store package**

Run: `cd /root/coding/modelserver && go test ./internal/store/...`
Expected: PASS (skips fine; no test failures).

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/store/migrations/056_route_client_billing.sql internal/store/migrations_056_test.go
git commit -m "feat(store): migration 056 — routes.clients + routes.billing_modes

Adds two TEXT[] columns to routes with empty-array defaults. Existing
routes carry empty arrays and continue to match every request (= today's
behavior). Subsequent tasks wire the matcher to read them.

Idempotent via IF NOT EXISTS; no down step.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Migration 057 — plans.client_model_credit_rates

**Files:**
- Create: `internal/store/migrations/057_plan_client_credit_rates.sql`
- Create: `internal/store/migrations_057_test.go`

**Interfaces:**
- Consumes: existing `plans` table schema. The plan row already carries a sibling `model_credit_rates JSONB` column (verified at `internal/store/migrations/001_init.sql:95` and `:184`); 057 adds the parallel client-keyed column.
- Produces: new column `plans.client_model_credit_rates JSONB` (nullable). Subsequent tasks (`types.Plan` field + `internal/store/plans.go` upsert/select extension + `Plan.ToPolicy` carryover + `Policy.ComputeCreditsForClient` resolver) consume this column.

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
-- Default NULL on existing rows. NULL is treated as "no overrides" by
-- the resolver. Idempotent via IF NOT EXISTS. No down step.

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

// TestMigration057_AddsPlanClientRatesColumnNullByDefault asserts the
// migration adds client_model_credit_rates as a nullable JSONB, leaves
// existing rows with NULL, and round-trips a populated JSON object.
func TestMigration057_AddsPlanClientRatesColumnNullByDefault(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Seed a plan using ONLY the pre-057 columns. The new column must
	// accept the row via its NULL default.
	var planID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO plans (name, slug, display_name, description, tier_level,
		    price_cny_fen, period_months, is_active)
		VALUES ('mig057-test', 'mig057-test', 'Migration 057 Test', '', 0,
		        0, 1, FALSE)
		RETURNING id`).Scan(&planID); err != nil {
		t.Fatalf("seed old-style plan: %v", err)
	}

	// Read the new column back; expect NULL.
	var raw []byte
	if err := st.pool.QueryRow(ctx,
		`SELECT client_model_credit_rates FROM plans WHERE id = $1`, planID).
		Scan(&raw); err != nil {
		t.Fatalf("select new column: %v", err)
	}
	if raw != nil {
		t.Errorf("default client_model_credit_rates = %q, want NULL", raw)
	}

	// Populate with a realistic shape and assert it round-trips.
	want := map[string]map[string]map[string]float64{
		"claude-code-cli": {
			"claude-sonnet-4": {"input_rate": 3, "output_rate": 15},
		},
		"codex-cli": {
			"gpt-5": {"input_rate": 0.5, "output_rate": 4},
		},
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
		gm, ok := got[client]
		if !ok {
			t.Errorf("got missing client %q", client)
			continue
		}
		for model, rates := range models {
			gr, ok := gm[model]
			if !ok {
				t.Errorf("got[%q] missing model %q", client, model)
				continue
			}
			for field, v := range rates {
				if gr[field] != v {
					t.Errorf("got[%q][%q][%q] = %v, want %v",
						client, model, field, gr[field], v)
				}
			}
		}
	}
}

// TestMigration057_Idempotent asserts re-running the migration is a no-op.
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
Expected: PASS (skips fine).

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

Extra-usage requests bypass this column entirely — they bill at the
catalog default rate via computeExtraUsageCostCredits.

Idempotent via IF NOT EXISTS; no down step.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: types.Route + types.Plan + store load/save wiring

**Files:**
- Modify: `internal/types/route.go` (add `Clients`, `BillingModes` fields)
- Modify: `internal/types/plan.go` (add `ClientModelCreditRates` field + thread through `ToPolicy`)
- Modify: `internal/types/policy.go` (add `ClientModelCreditRates` field on `RateLimitPolicy`)
- Modify: `internal/store/routes.go` (extend `routeSelectCols`, `CreateRoute`, `scanRoute`, `GetRouteByID`)
- Modify: `internal/store/plans.go` (extend `CreatePlan`, `GetPlanByID`, `GetPlanBySlug`, `ListPlans`, `ListPlansPaginated`, `ListPlansForProject`, `scanPlans`, `unmarshalPlanJSON`)

**Interfaces:**
- Consumes: migration 056 + 057 (Tasks 2-3).
- Produces:
  ```go
  // route.go
  type Route struct {
      // ... existing ...
      Clients      []string `json:"clients"`
      BillingModes []string `json:"billing_modes"`
  }

  // plan.go
  type Plan struct {
      // ... existing ...
      ClientModelCreditRates map[string]map[string]CreditRate `json:"client_model_credit_rates,omitempty"`
  }

  // policy.go
  type RateLimitPolicy struct {
      // ... existing ...
      ClientModelCreditRates map[string]map[string]CreditRate `json:"client_model_credit_rates,omitempty"`
  }
  ```
  No behavior change yet: `Router.Match` still ignores the new route fields; `Policy.ComputeCreditsWithDefault` still ignores the new rate map. Tasks 6 + 7 wire them in.

This task is the data plane: every CRUD path round-trips the new fields, every existing test continues to pass. The new fields stay invisible to consumers until Tasks 6-7 light them up.

- [ ] **Step 1: Extend `types.Route`**

In `internal/types/route.go`, change the struct to:

```go
package types

import "time"

// Route maps a set of canonical model names to an upstream group
// (nginx: location block). The route matches a request when its
// canonical model name (post-alias-resolution) appears in ModelNames,
// the request kind appears in RequestKinds, the client bucket appears
// in Clients (or Clients is empty = match any), and the billing mode
// appears in BillingModes (or BillingModes is empty = match any).
// Ordering among competing routes is given by weighted specificity
// then MatchPriority — see internal/proxy/router_engine.go.
type Route struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id,omitempty"` // "" = global route
	ModelNames      []string          `json:"model_names"`          // Canonical model names only (no aliases, no globs)
	RequestKinds    []string          `json:"request_kinds"`        // Wire-level endpoint kinds; values from internal/types/request_kind.go
	Clients         []string          `json:"clients"`              // ClientBucket values; empty = match any. See internal/types/client_bucket.go.
	BillingModes    []string          `json:"billing_modes"`        // BillingMode values; empty = match any. See internal/types/billing_mode.go.
	UpstreamGroupID string            `json:"upstream_group_id"`
	MatchPriority   int               `json:"match_priority"`
	Conditions      map[string]string `json:"conditions,omitempty"`
	Status          string            `json:"status"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}
```

- [ ] **Step 2: Extend `types.Plan` + `Plan.ToPolicy`**

In `internal/types/plan.go`:

Add the field to the struct (preserve all other fields):

```go
type Plan struct {
	// ... all existing fields, unchanged ...
	ModelCreditRates       map[string]CreditRate            `json:"model_credit_rates,omitempty"`
	ClientModelCreditRates map[string]map[string]CreditRate `json:"client_model_credit_rates,omitempty"`
	// ... other existing fields ...
}
```

Extend `ToPolicy` to thread the new field:

```go
func (p *Plan) ToPolicy(projectID string, subscriptionStartsAt *time.Time) *RateLimitPolicy {
	rules := make([]CreditRule, len(p.CreditRules))
	copy(rules, p.CreditRules)
	if subscriptionStartsAt != nil {
		for i := range rules {
			if rules[i].WindowType == WindowTypeFixed {
				t := *subscriptionStartsAt
				rules[i].AnchorTime = &t
			}
		}
	}
	return &RateLimitPolicy{
		ID:                     "plan:" + p.ID,
		ProjectID:              projectID,
		Name:                   p.Name,
		CreditRules:            rules,
		ModelCreditRates:       p.ModelCreditRates,
		ClientModelCreditRates: p.ClientModelCreditRates, // NEW
		ClassicRules:           p.ClassicRules,
	}
}
```

- [ ] **Step 3: Extend `RateLimitPolicy`**

In `internal/types/policy.go`, add the field to the struct (preserve every other field):

```go
type RateLimitPolicy struct {
	// ... all existing fields, unchanged ...
	ModelCreditRates       map[string]CreditRate            `json:"model_credit_rates,omitempty"`
	ClientModelCreditRates map[string]map[string]CreditRate `json:"client_model_credit_rates,omitempty"`
	// ... other existing fields ...
}
```

- [ ] **Step 4: Build + run types tests**

Run: `cd /root/coding/modelserver && go build ./internal/types/... && go test ./internal/types/...`
Expected: green. Existing `TestComputeCredits` etc. continue to pass because the new field is unused by today's resolver.

- [ ] **Step 5: Extend `internal/store/routes.go`**

Edit the `routeSelectCols` constant and the three call sites that scan / insert routes. The new column order appends `clients, billing_modes` AFTER the existing columns so SELECT-list / Scan-list / INSERT-list all stay in lockstep.

Replace the const at the top of the file:

```go
const routeSelectCols = `id, COALESCE(project_id::text, ''), model_names, request_kinds,
	upstream_group_id, match_priority, conditions, status, created_at, updated_at,
	clients, billing_modes`
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
	billingModes := r.BillingModes
	if billingModes == nil {
		billingModes = []string{}
	}
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO routes (project_id, model_names, request_kinds, upstream_group_id,
		    match_priority, conditions, status, clients, billing_modes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`,
		nullString(r.ProjectID), modelNames, requestKinds, r.UpstreamGroupID,
		r.MatchPriority, conditionsJSON, r.Status, clients, billingModes,
	).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}
```

Update `GetRouteByID` Scan argument list (append the two new pointers — order must match `routeSelectCols`):

```go
func (s *Store) GetRouteByID(id string) (*types.Route, error) {
	r := &types.Route{}
	var conditionsRaw []byte
	err := s.pool.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT %s FROM routes WHERE id = $1`, routeSelectCols), id,
	).Scan(&r.ID, &r.ProjectID, &r.ModelNames, &r.RequestKinds, &r.UpstreamGroupID,
		&r.MatchPriority, &conditionsRaw, &r.Status, &r.CreatedAt, &r.UpdatedAt,
		&r.Clients, &r.BillingModes)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get route: %w", err)
	}
	r.Conditions = unmarshalConditions(conditionsRaw)
	return r, nil
}
```

Update `scanRoute`:

```go
func scanRoute(rows pgx.Rows) (*types.Route, error) {
	r := &types.Route{}
	var conditionsRaw []byte
	if err := rows.Scan(&r.ID, &r.ProjectID, &r.ModelNames, &r.RequestKinds,
		&r.UpstreamGroupID, &r.MatchPriority, &conditionsRaw, &r.Status,
		&r.CreatedAt, &r.UpdatedAt, &r.Clients, &r.BillingModes); err != nil {
		return nil, err
	}
	r.Conditions = unmarshalConditions(conditionsRaw)
	return r, nil
}
```

`ListRoutes`, `ListRoutesPaginated`, and `ListRoutesForProject` all use `routeSelectCols` + `scanRoute` so they need no further edits.

The admin update path (`handleUpdateRoutingRoute` in `internal/admin/handle_routing_routes.go`) maintains a `for _, field := range []string{"project_id", "model_names", "request_kinds", "upstream_group_id", "match_priority", "conditions", "status"} { ... }` allow-list — Task 8 extends that with `"clients", "billing_modes"`. This task does NOT touch the admin handler; the new fields are read-only on the round-trip via the store layer change alone, which is enough to keep `go build ./...` green.

- [ ] **Step 6: Extend `internal/store/plans.go`**

The plan SELECT list is **inlined verbatim in five places** (`GetPlanByID`, `GetPlanBySlug`, `ListPlans`, `ListPlansPaginated`, `ListPlansForProject`). Extending without breaking them requires adding the new column to every SELECT + every Scan + the INSERT.

Add the column at a stable position in every SELECT list, e.g. immediately after `model_credit_rates` (apply to every site):

```go
// BEFORE:
SELECT id, name, slug, display_name, description, tier_level, group_tag,
    price_cny_fen, price_usd_cents, period_months, credit_rules, model_credit_rates,
    classic_rules, is_active, created_at, updated_at
FROM plans ...

// AFTER:
SELECT id, name, slug, display_name, description, tier_level, group_tag,
    price_cny_fen, price_usd_cents, period_months, credit_rules, model_credit_rates,
    client_model_credit_rates,
    classic_rules, is_active, created_at, updated_at
FROM plans ...
```

`scanPlans` and the individual `Scan` calls grow one more `[]byte` pointer for the new column. Add a local `var clientRates []byte` alongside the existing `var creditRules, rates, classic []byte`, and pass `&clientRates` into the Scan in the matching position.

Extend `unmarshalPlanJSON` to accept the new bytes and decode into `p.ClientModelCreditRates`:

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

Update every caller of `unmarshalPlanJSON` to pass the new arg in the same position.

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

The admin update path (`internal/admin/handle_plans.go`'s field allow-list) is touched in Task 8; this task does NOT modify it. `UpdatePlan` is generic (`buildUpdateQuery`) so it accepts whatever map the admin handler passes — no change needed here.

- [ ] **Step 7: Build + run store tests**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/store/...`
Expected: all green (including the migration tests from Tasks 2 + 3 if `TEST_DATABASE_URL` is set; skip otherwise). Existing plan / route tests must continue to PASS — the new fields are zero-value on every prior fixture, which round-trips fine.

- [ ] **Step 8: Commit**

```bash
cd /root/coding/modelserver
git add internal/types/route.go internal/types/plan.go internal/types/policy.go \
        internal/store/routes.go internal/store/plans.go
git commit -m "feat(types,store): plumb Clients/BillingModes/ClientModelCreditRates

Route gains two []string fields; Plan and RateLimitPolicy gain a
two-level map[client][model]CreditRate field. Store load/save round-trip
the new columns added in migrations 056+057.

No behavior change yet: Router.Match still ignores the new route fields
and Policy.ComputeCreditsWithDefault still ignores the new rate map.
Tasks 6 + 7 wire them in.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Trace middleware — `ctxClientBucket` plumbing

**Files:**
- Modify: `internal/proxy/trace_middleware.go` (declare new context key, write it in `TraceMiddleware`, expose getter)
- Modify: `internal/proxy/trace_middleware_test.go` (assert bucket populated for every `ClientKind*`)

**Interfaces:**
- Consumes: `types.MapClientKindToBucket` (Task 1); existing `deriveClientKind` and `ctxClientKind` in the same file.
- Produces:
  ```go
  // trace_middleware.go
  const ctxClientBucket contextKey = "client_bucket"
  func ClientBucketFromContext(ctx context.Context) string  // returns types.ClientBucketOther for any miss
  ```
  Task 6's `router.Match` and Task 7's pricing resolver both call `ClientBucketFromContext(ctx)` to read the bucket from the request context.

Tiny single-purpose task. Reading the bucket from context is the only way Tasks 6-7 obtain it; this task is the bridge.

- [ ] **Step 1: Write the failing test**

Open `internal/proxy/trace_middleware_test.go` and locate any existing `TestDeriveClientKind*` or `TestTraceMiddleware*` test for shape reference. Append:

```go
func TestTraceMiddleware_WritesClientBucket(t *testing.T) {
	// Build a chain that just inspects the context after TraceMiddleware ran.
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

// TestClientBucketFromContext_Default asserts the getter returns
// ClientBucketOther when no bucket was written to the context (defensive
// default for callers that run outside the trace middleware).
func TestClientBucketFromContext_Default(t *testing.T) {
	if got := ClientBucketFromContext(context.Background()); got != types.ClientBucketOther {
		t.Errorf("ClientBucketFromContext(empty) = %q, want %q", got, types.ClientBucketOther)
	}
}
```

The exact request shapes that pass each branch of `deriveClientKind` may differ slightly from what's shown above — verify by reading the corresponding existing tests in `trace_middleware_test.go` (search for `claude-cli`, `claude/`, `session_id`, `opencode/`, etc.) and adapt the `setup` lambdas to match the production matchers exactly. The test's value lies in covering every `ClientKind*` output; the request shaping is secondary.

If the test file lacks the imports `io`, `strings`, `httptest`, `slog`, `context`, or `config`, add them.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run 'TestTraceMiddleware_WritesClientBucket|TestClientBucketFromContext_Default' -v`
Expected: build error — `ctxClientBucket` and `ClientBucketFromContext` are undefined.

- [ ] **Step 3: Add the context key + getter + writer**

In `internal/proxy/trace_middleware.go`:

Find the existing `ctxClientKind` constant declaration (around line 22) and add `ctxClientBucket` right after it:

```go
const (
	ctxTraceID              contextKey = "trace_id"
	ctxTraceSource          contextKey = "trace_source"
	ctxClientKind           contextKey = "client_kind"
	ctxClientBucket         contextKey = "client_bucket" // NEW
	ctxClaudeAgentSDKSource contextKey = "claude_agent_sdk_source"
)
```

(Exact list shape may differ — keep all existing keys verbatim and append the new one.)

Find `ClientKindFromContext` (around line 108) and add `ClientBucketFromContext` immediately below it:

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

Find the existing line inside `TraceMiddleware`'s handler (around line 161) that writes `ctxClientKind`:

```go
kind, sdkSource := deriveClientKind(r, traceCfg)
ctx = context.WithValue(ctx, ctxClientKind, kind)
```

Add the bucket write immediately after it:

```go
kind, sdkSource := deriveClientKind(r, traceCfg)
ctx = context.WithValue(ctx, ctxClientKind, kind)
ctx = context.WithValue(ctx, ctxClientBucket, types.MapClientKindToBucket(kind))
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run 'TestTraceMiddleware_WritesClientBucket|TestClientBucketFromContext_Default' -v`
Expected: all PASS.

- [ ] **Step 5: Run the full proxy package**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/...`
Expected: PASS — existing tests are unaffected (no production code path reads `ctxClientBucket` yet; Tasks 6-7 wire those readers in).

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/trace_middleware.go internal/proxy/trace_middleware_test.go
git commit -m "feat(proxy): TraceMiddleware writes ctxClientBucket alongside ctxClientKind

Adds the 5-value bucket projection of the existing client_kind onto the
request context, plus ClientBucketFromContext getter that returns
ClientBucketOther for any miss. The bucket is computed via the pure
types.MapClientKindToBucket mapping introduced in the type primitives
task.

No production code path reads the new context value yet — Task 6
(router.Match) and Task 7 (Policy.ComputeCreditsForClient) wire those
consumers in.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Router refactor — Match signature + weighted specificity + MatrixGlobal extension

**Files:**
- Modify: `internal/proxy/router_engine.go` (Match signature; matchesGlobalRoute predicate; specificity sort; MatrixCell struct; MatrixGlobal signature + body)
- Modify: `internal/proxy/router_engine_test.go` (update 9 existing `r.Match(...)` call sites; rewrite `TestRouter_MatrixGlobal`; add 7 new tests)
- Modify: `internal/proxy/executor.go` (one-line caller update at L253)

**Interfaces:**
- Consumes: `types.MapClientKindToBucket` (Task 1), `ClientBucketFromContext` (Task 5), `types.Route.Clients` + `BillingModes` (Task 4), `types.BillingMode*` constants (Task 1).
- Produces:
  ```go
  // router_engine.go
  func (r *Router) Match(projectID, model, kind, client, billingMode string) (*resolvedGroup, error)

  type MatrixCell struct {
      Model           string
      Kind            string
      Client          string   // NEW — empty string when winning route's Clients is empty
      BillingMode     string   // NEW — empty string when winning route's BillingModes is empty
      UpstreamGroupID string
      UpstreamGroupName string  // hydrated by handler from SnapshotGroupNames; spec/plan added in routing-matrix PR — keep current field set
      RouteID         string
      MatchPriority   int
      Clients         []string // NEW — verbatim from the winning route (informational)
      BillingModes    []string // NEW — verbatim from the winning route (informational)
  }

  func (r *Router) MatrixGlobal(models []string, filterClient, filterBillingMode string) []MatrixCell
  ```
  `executor.go:253` calls the new signature with `ClientBucketFromContext(r.Context())` and a `billingMode` derived from `reqCtx.IsExtraUsage`. The admin matrix endpoint (Task 8) calls `MatrixGlobal` with the filters from query params.

This is the single biggest task in the plan. It's intentionally one commit because the signature change ripples through ~10 call sites; staging it across multiple commits leaves a window of red builds with no shipping value.

#### Algorithm recap

Match collects every route that matches `(projectID, model, kind, client, billingMode)` (project-scoped or global), scores each by weighted specificity, and picks the best with a deterministic tiebreak:

```
spec = (project_id_specific ? 100 : 0)
     + (clients_specific    ?  10 : 0)
     + (billing_modes_specific ?  1 : 0)
sort: spec desc, MatchPriority desc, ID asc
take head
```

Weights 100/10/1 are lexicographic: project trumps everything; client beats mode; mode is tiebreak among same-project same-client routes. Within a single spec bucket, `MatchPriority desc` decides; final tiebreak is `ID asc` so distributed nodes produce identical answers.

`matchesGlobalRoute` is the **shared** predicate used by both Match and MatrixGlobal — drift between them was a defect fixed in the prior route-matrix PR. This task preserves the shared-predicate invariant. The two new clauses (`Clients`, `BillingModes`) are added to the predicate; both callers benefit automatically.

The current two-pass structure (project-pass then global-pass) collapses into one: we still scan every route once, but the weighted score plus the project-pass-first scan order make the right candidate win without an explicit pass-ordered fall-through.

- [ ] **Step 1: Extend `matchesGlobalRoute` predicate**

In `internal/proxy/router_engine.go`, find the existing `matchesGlobalRoute` (around line 221) and add the two new clauses at the end. The function's contract — "exact projectID equality" — stays the same.

```go
// matchesGlobalRoute reports whether the route is a candidate for the
// given (projectID, model, kind, client, billingMode) tuple. Both Match
// and MatrixGlobal must use this so they cannot drift apart. If you
// teach this function to evaluate route.Conditions or any new criterion,
// both consumers benefit automatically.
func matchesGlobalRoute(route types.Route, projectID, model, kind, client, billingMode string) bool {
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
	if len(route.BillingModes) > 0 && !slices.Contains(route.BillingModes, billingMode) {
		return false
	}
	return true
}
```

- [ ] **Step 2: Replace `Router.Match` body with the scored algorithm**

Replace the existing `Match` (around line 240) with:

```go
// Match finds the upstream group for a request, scoring all eligible
// routes by weighted specificity (project 100, clients 10, billing_modes 1)
// then MatchPriority desc with ID asc as the final deterministic
// tiebreak. The shared matchesGlobalRoute predicate guarantees Match and
// MatrixGlobal stay aligned on what "eligible" means.
//
// Specificity weights are lexicographic by construction: project-scoped
// routes always beat global ones; among same-project routes,
// client-specific beats client-agnostic; among same-project same-client
// routes, mode-specific beats mode-agnostic; MatchPriority breaks
// further ties; ID asc is the deterministic floor.
func (r *Router) Match(projectID, model, kind, client, billingMode string) (*resolvedGroup, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	type candidate struct {
		route *types.Route
		spec  int
	}
	var best *candidate

	consider := func(route *types.Route) {
		// Skip routes pointing at a missing group — mirrors the prior
		// "if g, ok := r.groups[...]; ok" fall-through behavior.
		if _, ok := r.groups[route.UpstreamGroupID]; !ok {
			return
		}
		spec := 0
		if route.ProjectID != "" {
			spec += 100
		}
		if len(route.Clients) > 0 {
			spec += 10
		}
		if len(route.BillingModes) > 0 {
			spec += 1
		}
		if best == nil {
			best = &candidate{route: route, spec: spec}
			return
		}
		// Tiebreak: spec desc, MatchPriority desc, ID asc.
		if spec > best.spec ||
			(spec == best.spec && route.MatchPriority > best.route.MatchPriority) ||
			(spec == best.spec && route.MatchPriority == best.route.MatchPriority && route.ID < best.route.ID) {
			best = &candidate{route: route, spec: spec}
		}
	}

	for i := range r.routes {
		route := &r.routes[i]
		// Project-scoped pass.
		if matchesGlobalRoute(*route, projectID, model, kind, client, billingMode) {
			consider(route)
			continue
		}
		// Global fallback pass.
		if matchesGlobalRoute(*route, "", model, kind, client, billingMode) {
			consider(route)
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no route configured for model %s on endpoint %s (client=%s billing_mode=%s)",
			model, kind, client, billingMode)
	}
	return r.groups[best.route.UpstreamGroupID], nil
}
```

The error message now includes `client` and `billing_mode` so over-narrow route definitions ("I only have a `[subscription]` route — why does my extra-usage request 404?") are diagnosable from the message alone.

The old code held `r.routes` pre-sorted by `MatchPriority desc` (set in `buildMaps`). With the scored algorithm the pre-sort is no longer load-bearing for Match, but it remains useful for `ListRoutes` admin endpoints and is harmless to keep. Do NOT remove the sort in `buildMaps`.

- [ ] **Step 3: Extend `MatrixCell` struct**

Find the `MatrixCell` declaration (around line 463) and add the two new informational fields:

```go
// MatrixCell is one winning (model, kind, client, billing_mode) -> upstream
// group resolution, computed by MatrixGlobal. It is the same logical
// answer Match returns for a (projectID="", model, kind, client,
// billing_mode) tuple, but emitted as data so the admin UI can render
// the full matrix in one fetch.
type MatrixCell struct {
	Model           string
	Kind            string
	Client          string   // bucket the cell was resolved for; "" when no filter applied and not a per-client slice
	BillingMode     string   // mode the cell was resolved for; "" when no filter applied and not a per-mode slice
	UpstreamGroupID string
	RouteID         string
	MatchPriority   int
	Clients         []string // verbatim from the winning route — informational for UI badges
	BillingModes    []string // verbatim from the winning route — informational for UI badges
}
```

Note: the existing admin handler (`internal/admin/handle_routing_matrix.go`) wraps `MatrixCell` in `matrixCellOut` with snake_case JSON tags and hydrates `upstream_group_name` separately — Task 8 extends `matrixCellOut` with `clients`, `billing_modes`, `client`, `billing_mode`. This task only changes the Go-level struct; JSON shape changes are Task 8.

- [ ] **Step 4: Rewrite `MatrixGlobal`**

Find the existing `MatrixGlobal` (around line 484) and replace with:

```go
// MatrixGlobal walks every (model, kind, client, billingMode) 4-tuple
// over the supplied models, the full AllRequestKinds set, the 5
// ClientBuckets, and the 2 BillingModes, returning one MatrixCell for
// each tuple that resolves under the global-route branch of Match
// (projectID == ""). Unrouted tuples are omitted (sparse result).
//
// filterClient: when non-empty, restrict the client axis to that single
// bucket; the returned cells carry the filter value in their Client
// field. Empty means "iterate over all 5 buckets and leave Client empty
// on cells whose winning route is client-agnostic".
//
// filterBillingMode: analogous, restricting the billing_mode axis.
//
// Rules MUST mirror Match exactly:
//   - matchesGlobalRoute predicate (shared)
//   - weighted specificity scoring (clients 10, billing_modes 1; project
//     does NOT apply here since this is the global pass)
//   - missing group → skip and keep walking
func (r *Router) MatrixGlobal(models []string, filterClient, filterBillingMode string) []MatrixCell {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(models) == 0 {
		return nil
	}

	clients := types.AllClientBuckets
	if filterClient != "" {
		clients = []string{filterClient}
	}
	modes := types.AllBillingModes
	if filterBillingMode != "" {
		modes = []string{filterBillingMode}
	}

	out := make([]MatrixCell, 0, len(models)*len(types.AllRequestKinds))
	for _, model := range models {
		for _, kind := range types.AllRequestKinds {
			for _, client := range clients {
				for _, mode := range modes {
					var best *types.Route
					var bestSpec int
					for i := range r.routes {
						route := &r.routes[i]
						if !matchesGlobalRoute(*route, "", model, kind, client, mode) {
							continue
						}
						if _, ok := r.groups[route.UpstreamGroupID]; !ok {
							continue
						}
						spec := 0
						if len(route.Clients) > 0 {
							spec += 10
						}
						if len(route.BillingModes) > 0 {
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
						BillingMode:     mode,
						UpstreamGroupID: best.UpstreamGroupID,
						RouteID:         best.ID,
						MatchPriority:   best.MatchPriority,
						Clients:         best.Clients,
						BillingModes:    best.BillingModes,
					})
				}
			}
		}
	}
	return out
}
```

Coverage / size note: without filters, the iteration is `models × 8 kinds × 5 clients × 2 modes`. For 100 active models that's 8 000 inner loops; each scans `len(r.routes)` candidates. With typical operator route counts (≤ 50) the total work is < 500k comparisons — single-digit milliseconds, well within the existing route-matrix endpoint's response time budget.

- [ ] **Step 5: Update the executor caller**

In `internal/proxy/executor.go`, find the `router.Match` call at line ~253:

```go
// BEFORE:
group, err := e.router.Match(reqCtx.ProjectID, reqCtx.Model, reqCtx.RequestKind)
```

Replace with:

```go
// AFTER:
billingMode := types.BillingModeSubscription
if reqCtx.IsExtraUsage {
	billingMode = types.BillingModeExtraUsage
}
client := ClientBucketFromContext(r.Context())
group, err := e.router.Match(reqCtx.ProjectID, reqCtx.Model, reqCtx.RequestKind, client, billingMode)
```

The two new local variables make the routing decision auditable: anyone reading the function sees exactly what dimensions feed the matcher.

`reqCtx.IsExtraUsage` is already stamped on the request context earlier in `Execute()` (currently at executor.go:240) by reading `ExtraUsageContextFromContext`. The flag is authoritative — `RateLimitMiddleware` + `ExtraUsageGuardMiddleware` already decided whether this request is subscription or extra-usage before we get here. **Do NOT add any balance check at this site.**

- [ ] **Step 6: Update all 9 existing `r.Match(...)` call sites in `router_engine_test.go`**

Search and update — every site needs two new args. The trivial way is to add `types.ClientBucketOther` for the client and `types.BillingModeSubscription` for the mode at every existing call, which preserves today's "everyone matches everything" behavior on the legacy test fixtures (which have empty `Clients` and `BillingModes`).

Lines to update (verify against the file before editing — line numbers drift as tests get added):

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

For each, append `, types.ClientBucketOther, types.BillingModeSubscription` before the closing paren. Example:

```go
// BEFORE
g, err := r.Match("", "claude-sonnet", types.KindAnthropicMessages)

// AFTER
g, err := r.Match("", "claude-sonnet", types.KindAnthropicMessages,
	types.ClientBucketOther, types.BillingModeSubscription)
```

Also update the existing `TestRouter_MatrixGlobal` (around line 460) — `MatrixGlobal` now takes three args: `(models, filterClient, filterBillingMode)`. Pass `""` for both filter args to preserve today's behavior:

```go
cells := r.MatrixGlobal(modelNames, "", "")
```

Add assertions on the new `Client` and `BillingMode` cell fields where appropriate (every cell from the unfiltered call should have a populated `Client ∈ AllClientBuckets` and `BillingMode ∈ AllBillingModes`).

- [ ] **Step 7: Add new precedence + tiebreak + matrix-filter tests**

Append to `internal/proxy/router_engine_test.go`:

```go
// TestRouter_Match_ClientSpecificity asserts a route with Clients=[X] beats
// an otherwise-equal route with empty Clients for an X-client request, and
// loses for a Y-client request.
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
			Clients: []string{types.ClientBucketClaudeCodeCLI}, BillingModes: nil,
			UpstreamGroupID: "grp-cc", MatchPriority: 1, Status: "active"},
		{ID: "rt-any", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			Clients: nil, BillingModes: nil,
			UpstreamGroupID: "grp-any", MatchPriority: 100, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	// claude-code request → specific route wins despite lower priority.
	g, err := r.Match("", "m", types.KindAnthropicMessages,
		types.ClientBucketClaudeCodeCLI, types.BillingModeSubscription)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if g.group.ID != "grp-cc" {
		t.Errorf("client-specific lost: group = %s, want grp-cc", g.group.ID)
	}

	// other-client request → falls back to client-agnostic route.
	g, err = r.Match("", "m", types.KindAnthropicMessages,
		types.ClientBucketOther, types.BillingModeSubscription)
	if err != nil {
		t.Fatalf("Match (other): %v", err)
	}
	if g.group.ID != "grp-any" {
		t.Errorf("client-agnostic fallback: group = %s, want grp-any", g.group.ID)
	}
}

// TestRouter_Match_BillingModeSpecificity asserts the analogous rule for billing modes.
func TestRouter_Match_BillingModeSpecificity(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	upstreams := []types.Upstream{
		{ID: "up-a", Provider: types.ProviderAnthropic, Status: types.UpstreamStatusActive, Weight: 1, SupportedModels: []string{"m"}},
	}
	groups := []store.UpstreamGroupWithMembers{
		{UpstreamGroup: types.UpstreamGroup{ID: "grp-sub", Name: "subscription-pool", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-sub", UpstreamID: "up-a"}}}},
		{UpstreamGroup: types.UpstreamGroup{ID: "grp-any", Name: "any-pool", LBPolicy: types.LBPolicyWeightedRandom, Status: "active"},
			Members: []store.UpstreamGroupMemberDetail{{UpstreamGroupMember: types.UpstreamGroupMember{UpstreamGroupID: "grp-any", UpstreamID: "up-a"}}}},
	}
	routes := []types.Route{
		{ID: "rt-sub", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			BillingModes: []string{types.BillingModeSubscription},
			UpstreamGroupID: "grp-sub", MatchPriority: 1, Status: "active"},
		{ID: "rt-any", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "grp-any", MatchPriority: 100, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	g, _ := r.Match("", "m", types.KindAnthropicMessages,
		types.ClientBucketOther, types.BillingModeSubscription)
	if g.group.ID != "grp-sub" {
		t.Errorf("mode-specific lost: %s, want grp-sub", g.group.ID)
	}
	g, _ = r.Match("", "m", types.KindAnthropicMessages,
		types.ClientBucketOther, types.BillingModeExtraUsage)
	if g.group.ID != "grp-any" {
		t.Errorf("mode-agnostic fallback: %s, want grp-any", g.group.ID)
	}
}

// TestRouter_Match_FullPrecedence covers the spec's full specificity stack:
//   (project + clients + modes) > (project + clients) > (project) >
//   (clients + modes global) > (plain global)
// All five routes match the request; the most-specific must win regardless
// of MatchPriority ordering.
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
		mkGroup("g-plain"), mkGroup("g-cm"), mkGroup("g-proj"), mkGroup("g-pc"), mkGroup("g-pcm"),
	}
	routes := []types.Route{
		// All 5 match (project p, model m, kind anthropic_messages, client cc, mode sub).
		{ID: "rt-plain", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "g-plain", MatchPriority: 1000, Status: "active"},
		{ID: "rt-cm", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			Clients: []string{types.ClientBucketClaudeCodeCLI}, BillingModes: []string{types.BillingModeSubscription},
			UpstreamGroupID: "g-cm", MatchPriority: 999, Status: "active"},
		{ID: "rt-proj", ProjectID: "p", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "g-proj", MatchPriority: 0, Status: "active"},
		{ID: "rt-pc", ProjectID: "p", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			Clients: []string{types.ClientBucketClaudeCodeCLI},
			UpstreamGroupID: "g-pc", MatchPriority: 0, Status: "active"},
		{ID: "rt-pcm", ProjectID: "p", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			Clients: []string{types.ClientBucketClaudeCodeCLI}, BillingModes: []string{types.BillingModeSubscription},
			UpstreamGroupID: "g-pcm", MatchPriority: 0, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	g, err := r.Match("p", "m", types.KindAnthropicMessages,
		types.ClientBucketClaudeCodeCLI, types.BillingModeSubscription)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if g.group.ID != "g-pcm" {
		t.Errorf("precedence broken: got %s, want g-pcm (project+clients+modes most specific)", g.group.ID)
	}
}

// TestRouter_Match_LegacyEmptyMatchesAny asserts pre-migration routes
// (empty Clients + empty BillingModes) match every (client, mode)
// combination, preserving today's "match any" semantics.
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
		for _, m := range types.AllBillingModes {
			g, err := r.Match("", "m", types.KindAnthropicMessages, c, m)
			if err != nil {
				t.Errorf("Match(client=%s, mode=%s): %v", c, m, err)
			}
			if g != nil && g.group.ID != "grp" {
				t.Errorf("Match(client=%s, mode=%s) = %s, want grp", c, m, g.group.ID)
			}
		}
	}
}

// TestRouter_Match_DeterministicTiebreak asserts that two routes with
// identical specificity AND identical MatchPriority resolve by ID asc
// (lexicographic). Stable across nodes.
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
	// Two routes with identical spec (both global, both client-agnostic, both mode-agnostic)
	// and identical MatchPriority. IDs chosen so "rt-aaa" < "rt-bbb" lexicographically.
	routes := []types.Route{
		{ID: "rt-bbb", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "grp-bbb", MatchPriority: 5, Status: "active"},
		{ID: "rt-aaa", ProjectID: "", ModelNames: []string{"m"}, RequestKinds: []string{types.KindAnthropicMessages},
			UpstreamGroupID: "grp-aaa", MatchPriority: 5, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	g, err := r.Match("", "m", types.KindAnthropicMessages,
		types.ClientBucketOther, types.BillingModeSubscription)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if g.group.ID != "grp-aaa" {
		t.Errorf("tiebreak: got %s, want grp-aaa (ID asc wins)", g.group.ID)
	}
}

// TestRouter_MatrixGlobal_EmitsNewDimensions asserts every cell carries the
// new Client/BillingMode/Clients/BillingModes fields.
func TestRouter_MatrixGlobal_EmitsNewDimensions(t *testing.T) {
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
			Clients: []string{types.ClientBucketClaudeCodeCLI}, BillingModes: []string{types.BillingModeSubscription},
			UpstreamGroupID: "grp", MatchPriority: 1, Status: "active"},
	}
	r := NewRouter(upstreams, groups, routes, []byte{}, logger, time.Minute, nil, nil, nil)

	cells := r.MatrixGlobal([]string{"m"}, "", "")
	// Only the (claude-code-cli, subscription) cell should be populated.
	var found bool
	for _, c := range cells {
		if c.Client == types.ClientBucketClaudeCodeCLI && c.BillingMode == types.BillingModeSubscription {
			found = true
			if c.RouteID != "rt" {
				t.Errorf("cell.RouteID = %s, want rt", c.RouteID)
			}
			if !equalStrings(c.Clients, []string{types.ClientBucketClaudeCodeCLI}) {
				t.Errorf("cell.Clients = %v, want [claude-code-cli]", c.Clients)
			}
			if !equalStrings(c.BillingModes, []string{types.BillingModeSubscription}) {
				t.Errorf("cell.BillingModes = %v, want [subscription]", c.BillingModes)
			}
		}
	}
	if !found {
		t.Errorf("missing winning cell in matrix; got %d cells", len(cells))
	}
}

// TestRouter_MatrixGlobal_FilterByClient narrows the iteration to one client.
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

	cells := r.MatrixGlobal([]string{"m"}, types.ClientBucketClaudeCodeCLI, "")
	for _, c := range cells {
		if c.Client != types.ClientBucketClaudeCodeCLI {
			t.Errorf("filter leaked: cell.Client = %s", c.Client)
		}
	}
	// With a single route that matches every client, the filter should
	// reduce 5*2*1*1 = 10 cells (without filter) down to 1*2*1*1 = 2.
	if len(cells) != 2 {
		t.Errorf("len(cells) = %d, want 2 (one client × two modes × one model × one kind)", len(cells))
	}
}

// TestRouter_MatrixGlobal_FilterByBillingMode narrows the iteration to one mode.
func TestRouter_MatrixGlobal_FilterByBillingMode(t *testing.T) {
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

	cells := r.MatrixGlobal([]string{"m"}, "", types.BillingModeExtraUsage)
	for _, c := range cells {
		if c.BillingMode != types.BillingModeExtraUsage {
			t.Errorf("filter leaked: cell.BillingMode = %s", c.BillingMode)
		}
	}
	// 5 clients × 1 mode × 1 model × 1 kind = 5.
	if len(cells) != 5 {
		t.Errorf("len(cells) = %d, want 5", len(cells))
	}
}

// equalStrings is a small slice-equality helper used by the new tests.
// If the file already defines one (it likely does, used by other matrix
// tests), drop this and reuse.
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

- [ ] **Step 8: Build + run tests**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/proxy/...`

Expected: all green. The updated executor caller, the rewritten router, and the migrated tests all consistent.

- [ ] **Step 9: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/router_engine.go internal/proxy/router_engine_test.go internal/proxy/executor.go
git commit -m "feat(proxy): client+billing_mode routing axis with weighted specificity

Router.Match signature: (projectID, model, kind, client, billingMode).
matchesGlobalRoute predicate gains Clients/BillingModes clauses; the
single shared predicate keeps Match and MatrixGlobal aligned. The
priority-only first-hit-wins walk becomes a weighted-specificity sort
(project 100, clients 10, billing_modes 1) with MatchPriority desc as
the secondary key and route ID asc as the deterministic tiebreak.

MatrixCell carries Client + BillingMode + Clients + BillingModes;
MatrixGlobal accepts optional filterClient + filterBillingMode for
server-side narrowing (consumed by the admin matrix endpoint in the
next task).

Executor derives client = ClientBucketFromContext(ctx) and
billingMode = ternary(reqCtx.IsExtraUsage) one line before Match — no
new balance check. The authoritative subscription/extra-usage decision
remains in the RateLimit + ExtraUsageGuard chain, exactly as today.

Tests cover client specificity, mode specificity, full 5-tier
precedence, legacy 'empty matches any' compat, deterministic ID asc
tiebreak, matrix new-dimension emission, matrix client filter, matrix
mode filter. All 9 existing r.Match call sites updated.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

> **End of installment 4 (Task 6).** The router now resolves on the 5-tuple. Pricing layer (installment 5) is the next functional change; admin + dashboard wiring (installments 6-7) make the UI catch up.
>
> Remaining installments:
> - **Installment 5 (Task 7):** `Policy.ComputeCreditsForClient` resolver + Executor pricing call-site update + no-regression invariant tests.
> - **Installment 6 (Task 8):** admin API validation for new route fields + `GET /routing/clients` + `GET /routing/billing-modes` + matrix endpoint filter params + plan admin allow-list update.
> - **Installment 7 (Task 9):** dashboard Routes page columns + Create/Edit dialog selectors + Matrix tab filter dropdowns + manual smoke checklist.
> - **Final installment:** plan self-review section + execution handoff.

