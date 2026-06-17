# gpt-5.x subscription rate rebase (catalog × 0.1) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Re-anchor every gpt-5.x subscription rate to `catalog × 0.1`, backfill the 6 NULL catalog rows for legacy gpt-5.1 / gpt-5.2 variants, and keep admin-facing helpers (`utilizationAnalysisBaseRates` Go map, `DEFAULT_MODEL_CREDIT_RATES` TS map) in sync.

**Architecture:** One SQL migration (`047_gpt_5_x_plan_rebase.sql`) does the heavy lifting: catalog backfill (guarded by `IS NULL`) + plan/policy overwrite (unguarded, this rebase is meant to be authoritative). Two code edits update existing admin helper maps; one existing Go test gets its expected numbers updated. No router / parser / provider changes.

**Tech Stack:** PostgreSQL, jsonb, Go embed-based migration runner (`internal/store/store.go`), React + TypeScript admin dashboard.

**Spec:** `docs/superpowers/specs/2026-06-17-gpt-plan-rebase-design.md`

## Global Constraints

- **Backfill prices** (USD per 1M tokens, sourced from `001_init.sql:248-254`):
  - `gpt-5.1`, `gpt-5.1-codex`, `gpt-5.1-codex-max`: `$1.25 / $10 / $0.125`
  - `gpt-5.1-codex-mini`: `$0.25 / $2 / $0.025`
  - `gpt-5.2`, `gpt-5.2-codex`: `$1.75 / $14 / $0.175`
- **Catalog rate = API price ÷ 7.5** (project-wide convention, see `001_init.sql:240`).
- **Plan / policy rate = catalog × 0.1**, applied uniformly across every plan slug (free, pro, max_2x, max_5x, max_20x, max_40x, max_60x, max_80x, max_100x, max_120x, max_200x, max_240x) and every `rate_limit_policies` row.
- **13 models touched** in plan/policy maps: gpt-5.5, gpt-5.4, gpt-5.4-mini, gpt-5.4-nano, gpt-5.3-codex, gpt-5.2, gpt-5.2-codex, gpt-5.1, gpt-5.1-codex, gpt-5.1-codex-max, gpt-5.1-codex-mini, codex-auto-review. (gpt-5.4 / gpt-5.5 catalog rates are already correct, no backfill.)
- **`long_context` block preserved** on `gpt-5.4-mini` and `gpt-5.4-nano` only: `{threshold_input_tokens: 272000, input_multiplier: 2.0, output_multiplier: 1.5}`. Removed from `gpt-5.5` (live catalog has none; only the gpt-5.4-mini/-nano plan entries currently carry it).
- **All other gpt entries: no `long_context` block.**
- **Catalog backfill uses `IS NULL` guard** (preserves operator-set overrides).
- **Plan/policy UPDATE is unguarded** (intentional — this rebase is authoritative).
- **Out of scope**: gpt-5.4-pro, gpt-5.5-pro, all non-gpt models. The migration must NOT touch them.

### The 13-entry plan-rate matrix (used verbatim in Tasks 1, 3, 4)

| model | input | output | cache_creation | cache_read | long_context |
|---|---|---|---|---|---|
| gpt-5.5 | 0.0667 | 0.4 | 0 | 0.0067 | — |
| gpt-5.4 | 0.0333 | 0.2 | 0 | 0.0033 | — |
| gpt-5.4-mini | 0.0033 | 0.0267 | 0 | 0.0003 | {272000, 2.0, 1.5} |
| gpt-5.4-nano | 0.0007 | 0.0053 | 0 | 0.0001 | {272000, 2.0, 1.5} |
| gpt-5.3-codex | 0.0233 | 0.1867 | 0 | 0.0023 | — |
| gpt-5.2 | 0.0233 | 0.1867 | 0 | 0.0023 | — |
| gpt-5.2-codex | 0.0233 | 0.1867 | 0 | 0.0023 | — |
| gpt-5.1 | 0.0167 | 0.1333 | 0 | 0.0017 | — |
| gpt-5.1-codex | 0.0167 | 0.1333 | 0 | 0.0017 | — |
| gpt-5.1-codex-max | 0.0167 | 0.1333 | 0 | 0.0017 | — |
| gpt-5.1-codex-mini | 0.0033 | 0.0267 | 0 | 0.0003 | — |
| codex-auto-review | 0.0233 | 0.1867 | 0 | 0.0023 | — |

### The 6-entry catalog-backfill matrix (used in Task 1)

| model | input | output | cache_creation | cache_read |
|---|---|---|---|---|
| gpt-5.1 | 0.167 | 1.333 | 0 | 0.017 |
| gpt-5.1-codex | 0.167 | 1.333 | 0 | 0.017 |
| gpt-5.1-codex-max | 0.167 | 1.333 | 0 | 0.017 |
| gpt-5.1-codex-mini | 0.033 | 0.267 | 0 | 0.003 |
| gpt-5.2 | 0.233 | 1.867 | 0 | 0.023 |
| gpt-5.2-codex | 0.233 | 1.867 | 0 | 0.023 |

---

## File map

- **Create** `internal/store/migrations/047_gpt_5_x_plan_rebase.sql` (catalog backfill + plan/policy rebase)
- **Create** `internal/store/migrations_047_test.go` (catalog backfill assertions + plan/policy rebase assertions)
- **Modify** `internal/admin/handle_utilization_analysis.go` (rewrite the gpt entries in `utilizationAnalysisBaseRates`)
- **Modify** `internal/admin/handle_utilization_analysis_test.go` (update `TestUtilizationAnalysisBaseRates_GPT55SubscriptionDiscount` expected numbers; drop `LongContext` assertion since the new gpt-5.5 plan entry has none)
- **Modify** `dashboard/src/pages/admin/PlansPage.tsx` (overwrite existing gpt-5.5/-mini/-nano entries; add the other 9)

---

## Task 1: Write the migration `047_gpt_5_x_plan_rebase.sql`

**Files:**
- Create: `internal/store/migrations/047_gpt_5_x_plan_rebase.sql`

**Interfaces:**
- Consumes: existing `models`, `plans`, `rate_limit_policies` schemas; existing gpt-5.x catalog rows for gpt-5.3-codex / gpt-5.4 / gpt-5.4-mini / gpt-5.4-nano / gpt-5.5 / codex-auto-review (already populated).
- Produces: 6 newly-populated catalog rows (the gpt-5.1 / 5.2 family); every `plans.model_credit_rates` and `rate_limit_policies.model_credit_rates` now has the 13 listed entries set to plan rates from the matrix.

- [ ] **Step 1: Sanity-check JSON literals**

Run:
```bash
cd /root/coding/modelserver && python3 - <<'PY'
import json
literals = [
    # catalog backfill payloads
    '{"input_rate":0.167,"output_rate":1.333,"cache_creation_rate":0,"cache_read_rate":0.017}',
    '{"input_rate":0.033,"output_rate":0.267,"cache_creation_rate":0,"cache_read_rate":0.003}',
    '{"input_rate":0.233,"output_rate":1.867,"cache_creation_rate":0,"cache_read_rate":0.023}',
    # plan-rate bulk payload (13 entries; multi-line jsonb literal)
    '''{
        "gpt-5.5":            {"input_rate":0.0667,"output_rate":0.4,"cache_creation_rate":0,"cache_read_rate":0.0067},
        "gpt-5.4":            {"input_rate":0.0333,"output_rate":0.2,"cache_creation_rate":0,"cache_read_rate":0.0033},
        "gpt-5.4-mini":       {"input_rate":0.0033,"output_rate":0.0267,"cache_creation_rate":0,"cache_read_rate":0.0003,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}},
        "gpt-5.4-nano":       {"input_rate":0.0007,"output_rate":0.0053,"cache_creation_rate":0,"cache_read_rate":0.0001,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}},
        "gpt-5.3-codex":      {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
        "gpt-5.2":            {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
        "gpt-5.2-codex":      {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
        "gpt-5.1":            {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
        "gpt-5.1-codex":      {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
        "gpt-5.1-codex-max":  {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
        "gpt-5.1-codex-mini": {"input_rate":0.0033,"output_rate":0.0267,"cache_creation_rate":0,"cache_read_rate":0.0003},
        "codex-auto-review":  {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023}
    }''',
]
for s in literals:
    obj = json.loads(s)
print("ok: all 4 JSON literals parse")
PY
```
Expected: `ok: all 4 JSON literals parse`.

- [ ] **Step 2: Create the migration file**

Write this exact content to `/root/coding/modelserver/internal/store/migrations/047_gpt_5_x_plan_rebase.sql`:

```sql
-- 047_gpt_5_x_plan_rebase.sql
--
-- Re-anchor every gpt-5.x subscription rate to catalog * 0.1, and at the
-- same time backfill the 6 NULL catalog rows for legacy gpt-5.1 / gpt-5.2
-- variants. After this migration, every gpt-5.x plan entry can be
-- re-derived from its catalog row by multiplying by 0.1 — replacing the
-- mix of three previous calibration eras (0.066, 1.0, 0.132, gpt-5.5-pin).
--
-- See docs/superpowers/specs/2026-06-17-gpt-plan-rebase-design.md for
-- full rationale, burn-rate impact analysis, and risks.
--
-- ---------------------------------------------------------------------
-- Part 1: Catalog backfill (only updates NULL rows)
-- ---------------------------------------------------------------------
-- 6 legacy gpt models lost their default_credit_rate when first added
-- (migration 035 deliberately skipped them). Rate = official OpenAI API
-- price (per 1M tokens) / 7.5 (project-wide conversion).
--
--   gpt-5.1, gpt-5.1-codex, gpt-5.1-codex-max:  $1.25 / $10  / $0.125
--   gpt-5.1-codex-mini:                          $0.25 / $2   / $0.025
--   gpt-5.2, gpt-5.2-codex:                      $1.75 / $14  / $0.175
--
-- IS NULL guard: re-runs are safe and any operator-set rate wins, same
-- convention as 035.
--
-- Note: gpt-5.1 / gpt-5.1-codex base prices are not separately published
-- by OpenAI; we use the same numbers as gpt-5.1-codex-max ($1.25/$10/$0.125)
-- as a documented "best guess". Easy to override with a follow-up
-- migration if the published price differs.

UPDATE models
SET default_credit_rate = '{"input_rate":0.167,"output_rate":1.333,"cache_creation_rate":0,"cache_read_rate":0.017}'::jsonb,
    updated_at = NOW()
WHERE name IN ('gpt-5.1', 'gpt-5.1-codex', 'gpt-5.1-codex-max')
  AND default_credit_rate IS NULL;

UPDATE models
SET default_credit_rate = '{"input_rate":0.033,"output_rate":0.267,"cache_creation_rate":0,"cache_read_rate":0.003}'::jsonb,
    updated_at = NOW()
WHERE name = 'gpt-5.1-codex-mini'
  AND default_credit_rate IS NULL;

UPDATE models
SET default_credit_rate = '{"input_rate":0.233,"output_rate":1.867,"cache_creation_rate":0,"cache_read_rate":0.023}'::jsonb,
    updated_at = NOW()
WHERE name IN ('gpt-5.2', 'gpt-5.2-codex')
  AND default_credit_rate IS NULL;

-- ---------------------------------------------------------------------
-- Part 2: Plan rebase (unguarded — this is authoritative)
-- ---------------------------------------------------------------------
-- Shallow-merge the 13 entries into every plan's model_credit_rates.
-- The || operator overwrites any existing entries with these names but
-- leaves other model entries (claude-*, deepseek-*, glm-5.2 from 046,
-- _default) intact.
--
-- This is NOT idempotent in the "preserve operator overrides" sense —
-- re-running re-overwrites the 13 entries with the migration's values.
-- That's intentional; operators who want to keep a custom override must
-- apply it AFTER this migration runs, not before.

UPDATE plans
SET model_credit_rates = COALESCE(model_credit_rates, '{}'::jsonb) || '{
    "gpt-5.5":            {"input_rate":0.0667,"output_rate":0.4,"cache_creation_rate":0,"cache_read_rate":0.0067},
    "gpt-5.4":            {"input_rate":0.0333,"output_rate":0.2,"cache_creation_rate":0,"cache_read_rate":0.0033},
    "gpt-5.4-mini":       {"input_rate":0.0033,"output_rate":0.0267,"cache_creation_rate":0,"cache_read_rate":0.0003,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}},
    "gpt-5.4-nano":       {"input_rate":0.0007,"output_rate":0.0053,"cache_creation_rate":0,"cache_read_rate":0.0001,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}},
    "gpt-5.3-codex":      {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.2":            {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.2-codex":      {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.1":            {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex":      {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex-max":  {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex-mini": {"input_rate":0.0033,"output_rate":0.0267,"cache_creation_rate":0,"cache_read_rate":0.0003},
    "codex-auto-review":  {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023}
}'::jsonb,
    updated_at = NOW();

-- ---------------------------------------------------------------------
-- Part 3: Same against rate_limit_policies
-- ---------------------------------------------------------------------

UPDATE rate_limit_policies
SET model_credit_rates = COALESCE(model_credit_rates, '{}'::jsonb) || '{
    "gpt-5.5":            {"input_rate":0.0667,"output_rate":0.4,"cache_creation_rate":0,"cache_read_rate":0.0067},
    "gpt-5.4":            {"input_rate":0.0333,"output_rate":0.2,"cache_creation_rate":0,"cache_read_rate":0.0033},
    "gpt-5.4-mini":       {"input_rate":0.0033,"output_rate":0.0267,"cache_creation_rate":0,"cache_read_rate":0.0003,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}},
    "gpt-5.4-nano":       {"input_rate":0.0007,"output_rate":0.0053,"cache_creation_rate":0,"cache_read_rate":0.0001,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}},
    "gpt-5.3-codex":      {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.2":            {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.2-codex":      {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.1":            {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex":      {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex-max":  {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex-mini": {"input_rate":0.0033,"output_rate":0.0267,"cache_creation_rate":0,"cache_read_rate":0.0003},
    "codex-auto-review":  {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023}
}'::jsonb,
    updated_at = NOW();
```

- [ ] **Step 3: Verify the embed-FS loader sees it in sorted order**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/store/... -run TestMigrationsEmbed -count=1
```
Expected: `ok` — `047_gpt_5_x_plan_rebase.sql` lands after `046_*` (from the glm-5.2 plan) lexically and passes the sort check. If you're executing this plan before the glm-5.2 plan, just expect it to land after `045_*`.

- [ ] **Step 4: Build the whole project**

Run:
```bash
cd /root/coding/modelserver && go build ./...
```
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/047_gpt_5_x_plan_rebase.sql
git commit -m "feat(billing): rebase gpt-5.x plan rates to catalog x 0.1, backfill legacy catalog"
```

---

## Task 2: Write integration test `migrations_047_test.go`

**Files:**
- Create: `internal/store/migrations_047_test.go`

**Interfaces:**
- Consumes: `openTestStore(t)` (skips when `TEST_DATABASE_URL` unset).
- Produces: four tests — catalog backfill assertions, plan rebase assertions, policy rebase assertions, and a non-touch assertion (claude-* / deepseek-* / `_default` entries must remain unchanged).

- [ ] **Step 1: Write the test file**

Write this exact content to `/root/coding/modelserver/internal/store/migrations_047_test.go`:

```go
package store

import (
	"context"
	"testing"
)

// Plan-rate matrix that must appear in every plan / policy after 047.
// Catalog is 10x these values for the gpt entries (the migration's whole point).
var migration047PlanRates = map[string]struct {
	Input, Output, CacheRead float64
	HasLongContext           bool
}{
	"gpt-5.5":            {0.0667, 0.4, 0.0067, false},
	"gpt-5.4":            {0.0333, 0.2, 0.0033, false},
	"gpt-5.4-mini":       {0.0033, 0.0267, 0.0003, true},
	"gpt-5.4-nano":       {0.0007, 0.0053, 0.0001, true},
	"gpt-5.3-codex":      {0.0233, 0.1867, 0.0023, false},
	"gpt-5.2":            {0.0233, 0.1867, 0.0023, false},
	"gpt-5.2-codex":      {0.0233, 0.1867, 0.0023, false},
	"gpt-5.1":            {0.0167, 0.1333, 0.0017, false},
	"gpt-5.1-codex":      {0.0167, 0.1333, 0.0017, false},
	"gpt-5.1-codex-max":  {0.0167, 0.1333, 0.0017, false},
	"gpt-5.1-codex-mini": {0.0033, 0.0267, 0.0003, false},
	"codex-auto-review":  {0.0233, 0.1867, 0.0023, false},
}

// TestMigration047_CatalogBackfilled asserts that the 6 NULL catalog rows
// for legacy gpt-5.1 / gpt-5.2 variants are now populated with the
// expected USD/7.5 rates.
func TestMigration047_CatalogBackfilled(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name                                 string
		wantInput, wantOutput, wantCacheRead float64
	}{
		{"gpt-5.1", 0.167, 1.333, 0.017},
		{"gpt-5.1-codex", 0.167, 1.333, 0.017},
		{"gpt-5.1-codex-max", 0.167, 1.333, 0.017},
		{"gpt-5.1-codex-mini", 0.033, 0.267, 0.003},
		{"gpt-5.2", 0.233, 1.867, 0.023},
		{"gpt-5.2-codex", 0.233, 1.867, 0.023},
	}

	for _, tc := range cases {
		var input, output, cacheRead float64
		err := st.pool.QueryRow(ctx, `
			SELECT
			  (default_credit_rate->>'input_rate')::float8,
			  (default_credit_rate->>'output_rate')::float8,
			  (default_credit_rate->>'cache_read_rate')::float8
			FROM models WHERE name = $1`, tc.name).
			Scan(&input, &output, &cacheRead)
		if err != nil {
			t.Fatalf("%s: query catalog: %v", tc.name, err)
		}
		if input != tc.wantInput || output != tc.wantOutput || cacheRead != tc.wantCacheRead {
			t.Fatalf("%s catalog: input=%v output=%v cache_read=%v; want %v/%v/%v",
				tc.name, input, output, cacheRead, tc.wantInput, tc.wantOutput, tc.wantCacheRead)
		}
	}
}

// TestMigration047_PlansRebased asserts every plan now has the 13 gpt
// entries set to the plan-rate matrix.
func TestMigration047_PlansRebased(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for name, want := range migration047PlanRates {
		// Every plan must define the key.
		var missing int
		if err := st.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM plans WHERE NOT (model_credit_rates ? $1)`, name).
			Scan(&missing); err != nil {
			t.Fatalf("count missing %s in plans: %v", name, err)
		}
		if missing != 0 {
			t.Fatalf("%d plan(s) missing %s after migration 047", missing, name)
		}

		// Spot-check rates on the 'pro' plan.
		var input, output, cacheRead float64
		err := st.pool.QueryRow(ctx, `
			SELECT
			  (model_credit_rates->$1->>'input_rate')::float8,
			  (model_credit_rates->$1->>'output_rate')::float8,
			  (model_credit_rates->$1->>'cache_read_rate')::float8
			FROM plans WHERE slug = 'pro'`, name).
			Scan(&input, &output, &cacheRead)
		if err != nil {
			t.Fatalf("pro plan %s query: %v", name, err)
		}
		if input != want.Input || output != want.Output || cacheRead != want.CacheRead {
			t.Fatalf("pro plan %s: input=%v output=%v cache_read=%v; want %v/%v/%v",
				name, input, output, cacheRead, want.Input, want.Output, want.CacheRead)
		}

		// long_context presence check.
		var hasLC bool
		if err := st.pool.QueryRow(ctx, `
			SELECT model_credit_rates->$1 ? 'long_context' FROM plans WHERE slug = 'pro'`, name).
			Scan(&hasLC); err != nil {
			t.Fatalf("pro plan %s long_context check: %v", name, err)
		}
		if hasLC != want.HasLongContext {
			t.Fatalf("pro plan %s long_context present = %v, want %v", name, hasLC, want.HasLongContext)
		}
	}
}

// TestMigration047_PoliciesRebased mirrors the plans check against
// rate_limit_policies. Skips silently if no policies exist.
func TestMigration047_PoliciesRebased(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var totalPolicies int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM rate_limit_policies`).
		Scan(&totalPolicies); err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if totalPolicies == 0 {
		t.Skip("no rate_limit_policies rows to verify (fresh install)")
	}

	for name, want := range migration047PlanRates {
		var missing int
		if err := st.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM rate_limit_policies WHERE NOT (model_credit_rates ? $1)`, name).
			Scan(&missing); err != nil {
			t.Fatalf("count missing %s in policies: %v", name, err)
		}
		if missing != 0 {
			t.Fatalf("%d polic(y/ies) missing %s after migration 047", missing, name)
		}
		_ = want // shape parity with plans test
	}
}

// TestMigration047_DoesNotTouchNonGPT asserts the rebase did not stomp
// on claude-*, deepseek-*, glm-* or the _default fallback.
func TestMigration047_DoesNotTouchNonGPT(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Claude opus rate on the 'pro' plan should still be its 001_init value.
	var input, output float64
	err := st.pool.QueryRow(ctx, `
		SELECT
		  (model_credit_rates->'claude-opus-4-7'->>'input_rate')::float8,
		  (model_credit_rates->'claude-opus-4-7'->>'output_rate')::float8
		FROM plans WHERE slug = 'pro'`).
		Scan(&input, &output)
	if err != nil {
		t.Fatalf("query pro plan claude-opus-4-7: %v", err)
	}
	if input != 0.667 || output != 3.333 {
		t.Fatalf("pro plan claude-opus-4-7 changed: input=%v output=%v; want 0.667/3.333", input, output)
	}

	// _default entry should still be the Claude-Sonnet-equivalent fallback.
	var defInput, defOutput float64
	err = st.pool.QueryRow(ctx, `
		SELECT
		  (model_credit_rates->'_default'->>'input_rate')::float8,
		  (model_credit_rates->'_default'->>'output_rate')::float8
		FROM plans WHERE slug = 'pro'`).
		Scan(&defInput, &defOutput)
	if err != nil {
		t.Fatalf("query pro plan _default: %v", err)
	}
	if defInput != 0.4 || defOutput != 2.0 {
		t.Fatalf("pro plan _default changed: input=%v output=%v; want 0.4/2.0", defInput, defOutput)
	}
}
```

- [ ] **Step 2: Compile-check (will skip without TEST_DATABASE_URL)**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/store/... -run TestMigration047 -count=1
```
Expected: 4 `--- SKIP` lines (no `TEST_DATABASE_URL`) and `ok`. With a real test DB attached, expect 4 `--- PASS`.

- [ ] **Step 3: Commit**

```bash
git add internal/store/migrations_047_test.go
git commit -m "test(migrations): assert 047 catalog backfill + plan/policy rebase"
```

---

## Task 3: Rewrite the gpt entries in `utilizationAnalysisBaseRates`

**Files:**
- Modify: `internal/admin/handle_utilization_analysis.go` (the `utilizationAnalysisBaseRates` var, currently lines 45–73)

**Interfaces:**
- Consumes: `types.CreditRate`, `types.LongContextCreditRate`.
- Produces: the gpt-5.x entries (and `codex-auto-review`) all match the plan-rate matrix; claude entries untouched; new gpt-5.1 / gpt-5.1-codex / gpt-5.2 entries added.

- [ ] **Step 1: Replace the var block**

Open `/root/coding/modelserver/internal/admin/handle_utilization_analysis.go`. Find the entire `utilizationAnalysisBaseRates` var (currently lines 45–73). Replace its body — preserving the leading `var utilizationAnalysisBaseRates = map[string]types.CreditRate{` and the trailing `}` — with the following content:

```go
var utilizationAnalysisBaseRates = map[string]types.CreditRate{
	"claude-opus-4-7":           {InputRate: 0.667, OutputRate: 3.333, CacheCreationRate: 0.667, CacheReadRate: 0},
	"claude-opus-4-6":           {InputRate: 0.667, OutputRate: 3.333, CacheCreationRate: 0.667, CacheReadRate: 0},
	"claude-sonnet-4-6":         {InputRate: 0.4, OutputRate: 2.0, CacheCreationRate: 0.4, CacheReadRate: 0},
	"claude-haiku-4-5":          {InputRate: 0.133, OutputRate: 0.667, CacheCreationRate: 0.133, CacheReadRate: 0},
	"claude-haiku-4-5-20251001": {InputRate: 0.133, OutputRate: 0.667, CacheCreationRate: 0.133, CacheReadRate: 0},
	"gpt-5.5":                   {InputRate: 0.0667, OutputRate: 0.4, CacheCreationRate: 0, CacheReadRate: 0.0067},
	"gpt-5.4":                   {InputRate: 0.0333, OutputRate: 0.2, CacheCreationRate: 0, CacheReadRate: 0.0033},
	"gpt-5.4-mini": {InputRate: 0.0033, OutputRate: 0.0267, CacheCreationRate: 0, CacheReadRate: 0.0003, LongContext: &types.LongContextCreditRate{
		ThresholdInputTokens: 272000,
		InputMultiplier:      2.0,
		OutputMultiplier:     1.5,
	}},
	"gpt-5.4-nano": {InputRate: 0.0007, OutputRate: 0.0053, CacheCreationRate: 0, CacheReadRate: 0.0001, LongContext: &types.LongContextCreditRate{
		ThresholdInputTokens: 272000,
		InputMultiplier:      2.0,
		OutputMultiplier:     1.5,
	}},
	"gpt-5.3-codex":      {InputRate: 0.0233, OutputRate: 0.1867, CacheCreationRate: 0, CacheReadRate: 0.0023},
	"codex-auto-review":  {InputRate: 0.0233, OutputRate: 0.1867, CacheCreationRate: 0, CacheReadRate: 0.0023},
	"gpt-5.2-codex":      {InputRate: 0.0233, OutputRate: 0.1867, CacheCreationRate: 0, CacheReadRate: 0.0023},
	"gpt-5.2":            {InputRate: 0.0233, OutputRate: 0.1867, CacheCreationRate: 0, CacheReadRate: 0.0023},
	"gpt-5.1":            {InputRate: 0.0167, OutputRate: 0.1333, CacheCreationRate: 0, CacheReadRate: 0.0017},
	"gpt-5.1-codex":      {InputRate: 0.0167, OutputRate: 0.1333, CacheCreationRate: 0, CacheReadRate: 0.0017},
	"gpt-5.1-codex-max":  {InputRate: 0.0167, OutputRate: 0.1333, CacheCreationRate: 0, CacheReadRate: 0.0017},
	"gpt-5.1-codex-mini": {InputRate: 0.0033, OutputRate: 0.0267, CacheCreationRate: 0, CacheReadRate: 0.0003},
}
```

Notable: `gpt-5.5` no longer carries a `LongContext` block (the existing entry did; the new plan-rate value matches what's stored in plans, which has none). The existing `TestUtilizationAnalysisBaseRates_GPT55SubscriptionDiscount` will fail until Task 4 updates it.

- [ ] **Step 2: Verify only the test we expect to fail is failing**

Run:
```bash
cd /root/coding/modelserver && go build ./... && go test ./internal/admin/... -count=1
```
Expected: build succeeds; one test fails — `TestUtilizationAnalysisBaseRates_GPT55SubscriptionDiscount` — with messages like `InputRate = 0.0667, want subscription discount 0.044` and/or `LongContext is nil`. All other tests pass. Do NOT proceed past this point if a different test is also failing.

- [ ] **Step 3: Commit (yes, with one red test — Task 4 fixes it next)**

```bash
git add internal/admin/handle_utilization_analysis.go
git commit -m "feat(admin): rebase gpt-5.x utilization base rates to catalog x 0.1"
```

---

## Task 4: Update `TestUtilizationAnalysisBaseRates_GPT55SubscriptionDiscount`

**Files:**
- Modify: `internal/admin/handle_utilization_analysis_test.go` (the test, currently lines 65–84)

**Interfaces:**
- Consumes: `utilizationAnalysisBaseRates` updated in Task 3.
- Produces: a test that asserts gpt-5.5's new plan-rate values (0.0667 / 0.4 / 0.0067) and asserts the entry no longer carries a `LongContext` block.

- [ ] **Step 1: Rewrite the test**

Open `/root/coding/modelserver/internal/admin/handle_utilization_analysis_test.go`. Find `TestUtilizationAnalysisBaseRates_GPT55SubscriptionDiscount` (currently lines 65–84). Replace the entire function with:

```go
func TestUtilizationAnalysisBaseRates_GPT55SubscriptionDiscount(t *testing.T) {
	rate := utilizationAnalysisBaseRates["gpt-5.5"]
	if math.Abs(rate.InputRate-0.0667) > 0.000001 {
		t.Fatalf("InputRate = %v, want subscription discount 0.0667 (catalog x 0.1)", rate.InputRate)
	}
	if math.Abs(rate.OutputRate-0.4) > 0.000001 {
		t.Fatalf("OutputRate = %v, want subscription discount 0.4 (catalog x 0.1)", rate.OutputRate)
	}
	if math.Abs(rate.CacheReadRate-0.0067) > 0.000001 {
		t.Fatalf("CacheReadRate = %v, want subscription discount 0.0067 (catalog x 0.1)", rate.CacheReadRate)
	}
	// Migration 047 strips the long_context block from gpt-5.5 (the
	// rebased plan entry has none, so the OLS base rate has none either).
	if rate.LongContext != nil {
		t.Fatalf("LongContext = %+v, want nil after 047 rebase", rate.LongContext)
	}
}
```

- [ ] **Step 2: Run admin tests, all should pass now**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/admin/... -count=1
```
Expected: all `--- PASS`, `ok  github.com/modelserver/modelserver/internal/admin`.

- [ ] **Step 3: Commit**

```bash
git add internal/admin/handle_utilization_analysis_test.go
git commit -m "test(admin): update gpt-5.5 base-rate test for 047 rebase"
```

---

## Task 5: Update `DEFAULT_MODEL_CREDIT_RATES` in the dashboard

**Files:**
- Modify: `dashboard/src/pages/admin/PlansPage.tsx` (the `DEFAULT_MODEL_CREDIT_RATES` const, currently lines 42–112)

**Interfaces:**
- Consumes: `CreditRate` type (already imported).
- Produces: 13 gpt entries (including codex-auto-review) all aligned with the plan-rate matrix.

- [ ] **Step 1: Replace the gpt portion of the const**

Open `/root/coding/modelserver/dashboard/src/pages/admin/PlansPage.tsx`. Find the existing `gpt-5.5` block (line 73), the `gpt-5.4-mini` block (line 84), and the `gpt-5.4-nano` block (line 95). Replace those three blocks together with the following 13-entry block, keeping the surrounding claude entries above and the `_default` block below intact:

```ts
  "gpt-5.5": {
    input_rate: 0.0667,
    output_rate: 0.4,
    cache_creation_rate: 0,
    cache_read_rate: 0.0067,
  },
  "gpt-5.4": {
    input_rate: 0.0333,
    output_rate: 0.2,
    cache_creation_rate: 0,
    cache_read_rate: 0.0033,
  },
  "gpt-5.4-mini": {
    input_rate: 0.0033,
    output_rate: 0.0267,
    cache_creation_rate: 0,
    cache_read_rate: 0.0003,
    long_context: {
      threshold_input_tokens: 272000,
      input_multiplier: 2,
      output_multiplier: 1.5,
    },
  },
  "gpt-5.4-nano": {
    input_rate: 0.0007,
    output_rate: 0.0053,
    cache_creation_rate: 0,
    cache_read_rate: 0.0001,
    long_context: {
      threshold_input_tokens: 272000,
      input_multiplier: 2,
      output_multiplier: 1.5,
    },
  },
  "gpt-5.3-codex": {
    input_rate: 0.0233,
    output_rate: 0.1867,
    cache_creation_rate: 0,
    cache_read_rate: 0.0023,
  },
  "gpt-5.2": {
    input_rate: 0.0233,
    output_rate: 0.1867,
    cache_creation_rate: 0,
    cache_read_rate: 0.0023,
  },
  "gpt-5.2-codex": {
    input_rate: 0.0233,
    output_rate: 0.1867,
    cache_creation_rate: 0,
    cache_read_rate: 0.0023,
  },
  "gpt-5.1": {
    input_rate: 0.0167,
    output_rate: 0.1333,
    cache_creation_rate: 0,
    cache_read_rate: 0.0017,
  },
  "gpt-5.1-codex": {
    input_rate: 0.0167,
    output_rate: 0.1333,
    cache_creation_rate: 0,
    cache_read_rate: 0.0017,
  },
  "gpt-5.1-codex-max": {
    input_rate: 0.0167,
    output_rate: 0.1333,
    cache_creation_rate: 0,
    cache_read_rate: 0.0017,
  },
  "gpt-5.1-codex-mini": {
    input_rate: 0.0033,
    output_rate: 0.0267,
    cache_creation_rate: 0,
    cache_read_rate: 0.0003,
  },
  "codex-auto-review": {
    input_rate: 0.0233,
    output_rate: 0.1867,
    cache_creation_rate: 0,
    cache_read_rate: 0.0023,
  },
```

The relevant region after the edit should look like:

```ts
  "claude-haiku-4-5-20251001": {
    input_rate: 0.133,
    output_rate: 0.667,
    cache_creation_rate: 0.133,
    cache_read_rate: 0,
  },
  "gpt-5.5": { ... },
  "gpt-5.4": { ... },
  "gpt-5.4-mini": { ... long_context ... },
  "gpt-5.4-nano": { ... long_context ... },
  "gpt-5.3-codex": { ... },
  "gpt-5.2": { ... },
  "gpt-5.2-codex": { ... },
  "gpt-5.1": { ... },
  "gpt-5.1-codex": { ... },
  "gpt-5.1-codex-max": { ... },
  "gpt-5.1-codex-mini": { ... },
  "codex-auto-review": { ... },
  _default: { ... },
};
```

(If you've already executed the glm-5.2 plan, leave its `"glm-5.2"` entry in place between `codex-auto-review` and `_default`.)

- [ ] **Step 2: Run typecheck and lint**

Run:
```bash
cd /root/coding/modelserver/dashboard && npm run typecheck && npm run lint
```
Expected: both exit 0.

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/pages/admin/PlansPage.tsx
git commit -m "feat(dashboard): rebase gpt-5.x default plan rates to catalog x 0.1"
```

---

## Task 6: Whole-project regression

**Files:** none modified — verification only.

- [ ] **Step 1: Build + test everything**

Run:
```bash
cd /root/coding/modelserver && go build ./... && go test ./... -count=1
```
Expected: all packages either `ok` or `[no test files]`. The 4 new `TestMigration047_*` tests will `SKIP` without `TEST_DATABASE_URL`.

If anything fails, fix it before moving on. Do not commit failures.

- [ ] **Step 2: Confirm the five commits land in order**

Run:
```bash
git log --oneline -5
```
Expected (top to bottom):
```
xxxxxxx feat(dashboard): rebase gpt-5.x default plan rates to catalog x 0.1
xxxxxxx test(admin): update gpt-5.5 base-rate test for 047 rebase
xxxxxxx feat(admin): rebase gpt-5.x utilization base rates to catalog x 0.1
xxxxxxx test(migrations): assert 047 catalog backfill + plan/policy rebase
xxxxxxx feat(billing): rebase gpt-5.x plan rates to catalog x 0.1, backfill legacy catalog
```

---

## Post-deployment verification (manual, by operator)

Not part of plan execution — listed for the deployer:

1. After deploy, `SELECT name, default_credit_rate FROM models WHERE name LIKE 'gpt-5.1%' OR name LIKE 'gpt-5.2%';` — no NULL rows remain among the 6 backfilled names.
2. `SELECT slug, jsonb_pretty(model_credit_rates) FROM plans WHERE slug = 'max_100x';` — eyeball that the 13 gpt entries match the plan-rate matrix in the Global Constraints block.
3. `SELECT name, jsonb_pretty(model_credit_rates) FROM rate_limit_policies LIMIT 1;` — same.
4. Smoke-test a `gpt-5.3-codex` request under a Pro subscription. Confirm `credits_consumed` is roughly 10× smaller per request than before (the 1.0-ratio → 0.1-ratio drop).
5. Smoke-test a `gpt-5.5` request — burn should be ~50% higher than before (0.044 → 0.0667 input rate). Communicate to subscribers ahead of deploy.

---

## Self-review

- **Spec coverage**:
  - Spec "Scope" item 1 (`047_*.sql`) → Task 1 ✓
  - Spec "Scope" item 2 (backfill 6 catalog rows) → Task 1, Part 1 ✓
  - Spec "Scope" item 3 (overwrite gpt entries in every plan) → Task 1, Part 2 ✓
  - Spec "Scope" item 4 (same against rate_limit_policies) → Task 1, Part 3 ✓
  - Spec "Scope" item 5 (`utilizationAnalysisBaseRates` update) → Task 3 ✓
  - Spec "Scope" item 6 (`DEFAULT_MODEL_CREDIT_RATES` update) → Task 5 ✓
  - Spec "Code changes / test note" (`TestUtilizationAnalysisBaseRates_GPT55SubscriptionDiscount` needs new numbers) → Task 4 ✓
  - Spec "Verification" steps 1–3 (DB SQL probes) → Task 2 integration test + Task 6 build/regression ✓
  - Spec "Out of scope" guards (no -pro variants, no non-gpt models) → Task 2 `TestMigration047_DoesNotTouchNonGPT` ✓
- **Numbers match across files**: Tasks 1, 2, 3, 5 all draw from the Global Constraints matrix verbatim. Task 4's expected numbers match the gpt-5.5 row of that matrix.
- **No placeholders / TBDs**.
- **Type consistency**: Go uses `types.CreditRate` + `types.LongContextCreditRate` (consistent with existing entries). TS uses `CreditRate` interface with `input_rate` / `output_rate` / `cache_creation_rate` / `cache_read_rate` / `long_context` (matches existing entries). SQL field names (`input_rate`, `output_rate`, `cache_creation_rate`, `cache_read_rate`, `long_context`, `threshold_input_tokens`, `input_multiplier`, `output_multiplier`) match what `internal/types/model.go` serializes.
- **Migration ordering**: 047 comes after 046 lexically. Either plan can be executed first independently — the migrations don't touch each other's rows.
- **Red-then-green commit in Task 3**: this is intentional; Task 3 changes the values in `utilizationAnalysisBaseRates` (which breaks one existing test) and Task 4 immediately fixes that test. Each commit is independently meaningful and reviewable; the brief "broken between" state lives in one git rev and is fixed in the very next. If your project policy forbids landing any failing test in any commit, squash Task 3 + Task 4 into one commit instead.
