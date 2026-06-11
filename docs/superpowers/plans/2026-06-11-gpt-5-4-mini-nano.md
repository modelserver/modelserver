# gpt-5.4-mini and gpt-5.4-nano Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Register `gpt-5.4-mini` and `gpt-5.4-nano` in the model catalog with official OpenAI API prices, seed a calibrated subscription discount into every existing plan and rate-limit policy, and surface both models in the admin utilization regression and the dashboard plan editor.

**Architecture:** One new SQL migration (`045_add_gpt_5_4_mini_nano.sql`) following the `027_add_gpt_5_5.sql` + `032_openai_long_context_pricing.sql` pattern: INSERT catalog rows (official rate), UPDATE every plan/policy that doesn't already define the new keys (subscription rate). Two small code touch-ups mirror those rates into the Go regression seed map and the dashboard `KNOWN_RATES` map. No router, parser, or provider code changes are needed — modelserver dispatches by model name string.

**Tech Stack:** PostgreSQL JSONB, Go (stdlib + pgx), React + TypeScript.

**Spec:** `docs/superpowers/specs/2026-06-11-gpt-5-4-mini-nano-design.md`

---

## Rate reference (used verbatim throughout this plan)

**Catalog rate** (official API price ÷ 7.5):

| model | input | output | cache_creation | cache_read |
|---|---|---|---|---|
| gpt-5.4-mini | 0.033 | 0.267 | 0 | 0.003 |
| gpt-5.4-nano | 0.007 | 0.053 | 0 | 0.001 |

**Plan / policy rate** (catalog × 0.0654, matches gpt-5.5 subscription multiplier):

| model | input | output | cache_creation | cache_read |
|---|---|---|---|---|
| gpt-5.4-mini | 0.0022 | 0.0175 | 0 | 0.0002 |
| gpt-5.4-nano | 0.0005 | 0.0035 | 0 | 0.0001 |

**long_context** (both models, both rate scopes): `{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}`.

---

## Task 1: Write the migration file

**Files:**
- Create: `internal/store/migrations/045_add_gpt_5_4_mini_nano.sql`

- [ ] **Step 1: Write the migration**

Create `internal/store/migrations/045_add_gpt_5_4_mini_nano.sql` with this exact content:

```sql
-- 045_add_gpt_5_4_mini_nano.sql
--
-- Register gpt-5.4-mini and gpt-5.4-nano in the model catalog and seed their
-- subscription credit rates into every plan and rate-limit policy. Follows
-- the same pattern as 027_add_gpt_5_5.sql (catalog INSERT + per-plan seed)
-- combined with 032_openai_long_context_pricing.sql (policy seed +
-- long_context payload).
--
-- Catalog rate = official OpenAI API price / 7.5 (project-wide conversion,
-- see 001_init.sql:240). This is what computeExtraUsageCostFen bills against,
-- so it MUST equal the official rate:
--
--   gpt-5.4-mini   API: input=$0.25,  cache_read=$0.025,  output=$2.00
--                  cat: input=0.033,  cache_read=0.003,   output=0.267
--   gpt-5.4-nano   API: input=$0.05,  cache_read=$0.005,  output=$0.40
--                  cat: input=0.007,  cache_read=0.001,   output=0.053
--
-- Plan/policy rate = catalog * 0.0654 (the gpt-5.5 subscription multiplier
-- calibrated in 030_update_gpt_5_5_credit_rates.sql against fixed Max 20x
-- capacities — 5h=11M, 7d=83.3M credits). Subscribers see the discounted
-- rate; non-subscribers/extra-usage fall through to the catalog rate.
--
-- long_context (input>272K → input x2, output x1.5) applies to both, matching
-- the family-wide policy already attached to gpt-5.4 / gpt-5.5 / *-pro.
--
-- The NOT (rates ? 'name') guards make the UPDATEs idempotent and preserve
-- any operator-set custom override between deploy and re-run.

-- 1) Catalog rows. ON CONFLICT DO NOTHING so re-runs (or a manual seed prior
--    to deploy) are no-ops, mirroring 027.
INSERT INTO models (name, display_name, description, aliases, default_credit_rate, status, publisher, metadata)
VALUES
    (
        'gpt-5.4-mini',
        'GPT-5.4 Mini',
        'Smaller, faster GPT-5.4 variant for everyday tasks.',
        '{}',
        '{"input_rate":0.033,"output_rate":0.267,"cache_creation_rate":0,"cache_read_rate":0.003,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb,
        'active',
        'openai',
        '{}'::jsonb
    ),
    (
        'gpt-5.4-nano',
        'GPT-5.4 Nano',
        'Smallest GPT-5.4 variant for high-volume, low-cost workloads.',
        '{}',
        '{"input_rate":0.007,"output_rate":0.053,"cache_creation_rate":0,"cache_read_rate":0.001,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb,
        'active',
        'openai',
        '{}'::jsonb
    )
ON CONFLICT (name) DO NOTHING;

-- 2) Seed gpt-5.4-mini into every plan that doesn't already define it.
UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.4-mini}',
        '{"input_rate":0.0022,"output_rate":0.0175,"cache_creation_rate":0,"cache_read_rate":0.0002,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE NOT (model_credit_rates ? 'gpt-5.4-mini');

-- 3) Seed gpt-5.4-nano into every plan that doesn't already define it.
UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.4-nano}',
        '{"input_rate":0.0005,"output_rate":0.0035,"cache_creation_rate":0,"cache_read_rate":0.0001,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE NOT (model_credit_rates ? 'gpt-5.4-nano');

-- 4) Same two seeds against rate_limit_policies so per-policy overrides
--    pick the new models up too.
UPDATE rate_limit_policies
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.4-mini}',
        '{"input_rate":0.0022,"output_rate":0.0175,"cache_creation_rate":0,"cache_read_rate":0.0002,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE NOT (model_credit_rates ? 'gpt-5.4-mini');

UPDATE rate_limit_policies
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.4-nano}',
        '{"input_rate":0.0005,"output_rate":0.0035,"cache_creation_rate":0,"cache_read_rate":0.0001,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE NOT (model_credit_rates ? 'gpt-5.4-nano');
```

- [ ] **Step 2: Commit**

```bash
git add internal/store/migrations/045_add_gpt_5_4_mini_nano.sql
git commit -m "feat(catalog): add gpt-5.4-mini and gpt-5.4-nano

Catalog rates mirror OpenAI gpt-5-mini/nano API prices divided by 7.5.
Per-plan and per-policy subscription rates apply the 0.0654 multiplier
already calibrated for gpt-5.5. Both carry the family-wide long_context
uplift (input x2 / output x1.5 above 272K input tokens)."
```

---

## Task 2: Write the migration test

**Files:**
- Create: `internal/store/migrations_045_test.go`

This task verifies — against a real Postgres — that the migration registered both catalog rows with the expected JSONB and seeded every existing plan and policy.

- [ ] **Step 1: Write the failing test**

Create `internal/store/migrations_045_test.go`:

```go
package store

import (
	"context"
	"testing"
)

// TestMigration045_CatalogRowsPresent asserts that gpt-5.4-mini and
// gpt-5.4-nano were inserted into the models table with the expected
// official-rate JSONB payload (input_rate, output_rate, cache_read_rate,
// long_context).
func TestMigration045_CatalogRowsPresent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name              string
		wantInput         float64
		wantOutput        float64
		wantCacheRead     float64
		wantLCInputMult   float64
		wantLCOutputMult  float64
		wantLCThreshold   int
	}{
		{"gpt-5.4-mini", 0.033, 0.267, 0.003, 2.0, 1.5, 272000},
		{"gpt-5.4-nano", 0.007, 0.053, 0.001, 2.0, 1.5, 272000},
	}

	for _, tc := range cases {
		var input, output, cacheRead, lcIn, lcOut float64
		var lcThresh int
		err := st.pool.QueryRow(ctx, `
			SELECT
			  (default_credit_rate->>'input_rate')::float8,
			  (default_credit_rate->>'output_rate')::float8,
			  (default_credit_rate->>'cache_read_rate')::float8,
			  (default_credit_rate->'long_context'->>'input_multiplier')::float8,
			  (default_credit_rate->'long_context'->>'output_multiplier')::float8,
			  (default_credit_rate->'long_context'->>'threshold_input_tokens')::int
			FROM models WHERE name = $1`, tc.name).
			Scan(&input, &output, &cacheRead, &lcIn, &lcOut, &lcThresh)
		if err != nil {
			t.Fatalf("%s: query catalog: %v", tc.name, err)
		}
		if input != tc.wantInput || output != tc.wantOutput || cacheRead != tc.wantCacheRead {
			t.Fatalf("%s catalog rates: input=%v output=%v cache_read=%v; want %v/%v/%v",
				tc.name, input, output, cacheRead, tc.wantInput, tc.wantOutput, tc.wantCacheRead)
		}
		if lcIn != tc.wantLCInputMult || lcOut != tc.wantLCOutputMult || lcThresh != tc.wantLCThreshold {
			t.Fatalf("%s long_context: in_mult=%v out_mult=%v thresh=%v; want %v/%v/%v",
				tc.name, lcIn, lcOut, lcThresh,
				tc.wantLCInputMult, tc.wantLCOutputMult, tc.wantLCThreshold)
		}
	}
}

// TestMigration045_PlansSeeded asserts that every pre-existing plan now has
// both gpt-5.4-mini and gpt-5.4-nano keys in model_credit_rates with the
// expected plan-rate values (0.0654 * catalog).
func TestMigration045_PlansSeeded(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cases := []struct {
		key        string
		wantInput  float64
		wantOutput float64
	}{
		{"gpt-5.4-mini", 0.0022, 0.0175},
		{"gpt-5.4-nano", 0.0005, 0.0035},
	}

	for _, tc := range cases {
		// Every plan should have the key (NOT (rates ? key) guard means any
		// plan touched by the migration set it).
		var missing int
		if err := st.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM plans WHERE NOT (model_credit_rates ? $1)`, tc.key).
			Scan(&missing); err != nil {
			t.Fatalf("count missing %s: %v", tc.key, err)
		}
		if missing != 0 {
			t.Fatalf("%d plan(s) missing %s after migration", missing, tc.key)
		}

		// Spot-check the rate values on the well-known 'pro' plan.
		var input, output float64
		err := st.pool.QueryRow(ctx, `
			SELECT
			  (model_credit_rates->$1->>'input_rate')::float8,
			  (model_credit_rates->$1->>'output_rate')::float8
			FROM plans WHERE slug = 'pro'`, tc.key).
			Scan(&input, &output)
		if err != nil {
			t.Fatalf("query pro plan %s: %v", tc.key, err)
		}
		if input != tc.wantInput || output != tc.wantOutput {
			t.Fatalf("pro plan %s: input=%v output=%v; want %v/%v",
				tc.key, input, output, tc.wantInput, tc.wantOutput)
		}
	}
}

// TestMigration045_PoliciesSeeded mirrors TestMigration045_PlansSeeded but
// against rate_limit_policies. Skips silently if no policies exist yet
// (fresh installs have none — the seed is for live deploys with custom
// policies attached to projects).
func TestMigration045_PoliciesSeeded(t *testing.T) {
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

	for _, key := range []string{"gpt-5.4-mini", "gpt-5.4-nano"} {
		var missing int
		if err := st.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM rate_limit_policies WHERE NOT (model_credit_rates ? $1)`, key).
			Scan(&missing); err != nil {
			t.Fatalf("count missing policy %s: %v", key, err)
		}
		if missing != 0 {
			t.Fatalf("%d policy/policies missing %s after migration", missing, key)
		}
	}
}
```

- [ ] **Step 2: Run the migration test to verify it passes**

Run:

```bash
cd /root/coding/modelserver
go test ./internal/store/ -run TestMigration045 -v
```

Expected (if `TEST_DATABASE_URL` is set): all three subtests `PASS`. If `TEST_DATABASE_URL` is unset, tests `SKIP` — that is acceptable here because the same `openTestStore` skip semantic protects the full `go test ./...` run on CI without a DB.

- [ ] **Step 3: Run the full store test package**

Run:

```bash
go test ./internal/store/ -v 2>&1 | tail -40
```

Expected: no regressions in other migration tests (036, 043).

- [ ] **Step 4: Commit**

```bash
git add internal/store/migrations_045_test.go
git commit -m "test(migrations): verify 045 catalog + plan + policy seeds

Three integration tests against TEST_DATABASE_URL: catalog rows present
with official rate and long_context, every plan has both keys with the
expected discounted rate, every existing policy is similarly seeded."
```

---

## Task 3: Update the utilization-analysis base rates map

**Files:**
- Modify: `internal/admin/handle_utilization_analysis.go`

This map is the OLS regression's seed when fitting subscription-window utilization. It must contain entries for any model that appears in `ModelBreakdown` snapshots, otherwise the regression silently drops those rows.

- [ ] **Step 1: Read the current map for context**

Look at `internal/admin/handle_utilization_analysis.go` lines 45-63 — the `utilizationAnalysisBaseRates` block. The new entries go between the existing `gpt-5.5` entry and `gpt-5.4`, preserving descending-version order.

- [ ] **Step 2: Add the two entries**

Modify `internal/admin/handle_utilization_analysis.go` by inserting the following block immediately after the closing `}}` of the `gpt-5.5` entry (i.e. between current lines 55 and 56):

```go
	"gpt-5.4-mini": {InputRate: 0.0022, OutputRate: 0.0175, CacheCreationRate: 0, CacheReadRate: 0.0002, LongContext: &types.LongContextCreditRate{
		ThresholdInputTokens: 272000,
		InputMultiplier:      2.0,
		OutputMultiplier:     1.5,
	}},
	"gpt-5.4-nano": {InputRate: 0.0005, OutputRate: 0.0035, CacheCreationRate: 0, CacheReadRate: 0.0001, LongContext: &types.LongContextCreditRate{
		ThresholdInputTokens: 272000,
		InputMultiplier:      2.0,
		OutputMultiplier:     1.5,
	}},
```

The final block (lines 45-65 of the new file) should read:

```go
var utilizationAnalysisBaseRates = map[string]types.CreditRate{
	"claude-opus-4-7":           {InputRate: 0.667, OutputRate: 3.333, CacheCreationRate: 0.667, CacheReadRate: 0},
	"claude-opus-4-6":           {InputRate: 0.667, OutputRate: 3.333, CacheCreationRate: 0.667, CacheReadRate: 0},
	"claude-sonnet-4-6":         {InputRate: 0.4, OutputRate: 2.0, CacheCreationRate: 0.4, CacheReadRate: 0},
	"claude-haiku-4-5":          {InputRate: 0.133, OutputRate: 0.667, CacheCreationRate: 0.133, CacheReadRate: 0},
	"claude-haiku-4-5-20251001": {InputRate: 0.133, OutputRate: 0.667, CacheCreationRate: 0.133, CacheReadRate: 0},
	"gpt-5.5": {InputRate: 0.044, OutputRate: 0.261, CacheCreationRate: 0, CacheReadRate: 0.0044, LongContext: &types.LongContextCreditRate{
		ThresholdInputTokens: 272000,
		InputMultiplier:      2.0,
		OutputMultiplier:     1.5,
	}},
	"gpt-5.4-mini": {InputRate: 0.0022, OutputRate: 0.0175, CacheCreationRate: 0, CacheReadRate: 0.0002, LongContext: &types.LongContextCreditRate{
		ThresholdInputTokens: 272000,
		InputMultiplier:      2.0,
		OutputMultiplier:     1.5,
	}},
	"gpt-5.4-nano": {InputRate: 0.0005, OutputRate: 0.0035, CacheCreationRate: 0, CacheReadRate: 0.0001, LongContext: &types.LongContextCreditRate{
		ThresholdInputTokens: 272000,
		InputMultiplier:      2.0,
		OutputMultiplier:     1.5,
	}},
	"gpt-5.4":            {InputRate: 0.333, OutputRate: 2.0, CacheCreationRate: 0, CacheReadRate: 0.033},
	"gpt-5.3-codex":      {InputRate: 0.233, OutputRate: 1.867, CacheCreationRate: 0, CacheReadRate: 0.023},
	"codex-auto-review":  {InputRate: 0.233, OutputRate: 1.867, CacheCreationRate: 0, CacheReadRate: 0.023},
	"gpt-5.2-codex":      {InputRate: 0.233, OutputRate: 1.867, CacheCreationRate: 0, CacheReadRate: 0.023},
	"gpt-5.2":            {InputRate: 0.233, OutputRate: 1.867, CacheCreationRate: 0, CacheReadRate: 0.023},
	"gpt-5.1-codex-max":  {InputRate: 0.167, OutputRate: 1.333, CacheCreationRate: 0, CacheReadRate: 0.017},
	"gpt-5.1-codex-mini": {InputRate: 0.033, OutputRate: 0.267, CacheCreationRate: 0, CacheReadRate: 0.003},
}
```

- [ ] **Step 3: Build and test the admin package**

Run:

```bash
cd /root/coding/modelserver
go build ./internal/admin/
go test ./internal/admin/ -v -run TestSuggestRates 2>&1 | tail -30
```

Expected: build succeeds, the two existing `TestSuggestRatesForFixedLimit*` tests still `PASS`. They don't enumerate the map (they use literal `gpt-5.5` fixtures), so no fixture changes are required.

- [ ] **Step 4: Commit**

```bash
git add internal/admin/handle_utilization_analysis.go
git commit -m "feat(admin): include gpt-5.4-mini/nano in utilization base rates

Mirrors the per-plan subscription rate seeded in migration 045 so the
OLS regression can fit utilization snapshots that mention these models."
```

---

## Task 4: Update the dashboard `KNOWN_RATES` map

**Files:**
- Modify: `dashboard/src/pages/admin/PlansPage.tsx`

This map prefills the rate editor when an admin adds the model to a plan. The constant name in the current file is `DEFAULT_MODEL_CREDIT_RATES` (not `KNOWN_RATES` — the spec naming was loose).

- [ ] **Step 1: Read the current map for context**

Look at `dashboard/src/pages/admin/PlansPage.tsx` lines 42-90 — the `DEFAULT_MODEL_CREDIT_RATES` block. The new entries go immediately after the `gpt-5.5` entry (current lines 73-83), before `_default`.

- [ ] **Step 2: Add the two entries**

Insert the following block between the closing `},` of the `gpt-5.5` entry and the opening `_default:` (i.e. between current lines 83 and 84):

```ts
  "gpt-5.4-mini": {
    input_rate: 0.0022,
    output_rate: 0.0175,
    cache_creation_rate: 0,
    cache_read_rate: 0.0002,
    long_context: {
      threshold_input_tokens: 272000,
      input_multiplier: 2,
      output_multiplier: 1.5,
    },
  },
  "gpt-5.4-nano": {
    input_rate: 0.0005,
    output_rate: 0.0035,
    cache_creation_rate: 0,
    cache_read_rate: 0.0001,
    long_context: {
      threshold_input_tokens: 272000,
      input_multiplier: 2,
      output_multiplier: 1.5,
    },
  },
```

- [ ] **Step 3: Typecheck the dashboard**

Run:

```bash
cd /root/coding/modelserver/dashboard
npm run typecheck 2>&1 | tail -20
```

Expected: no errors. (`long_context` on `CreditRate` is already typed because the `gpt-5.5` entry above uses it.)

If the project has a lint step:

```bash
npm run lint 2>&1 | tail -20
```

Expected: no new errors.

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/pages/admin/PlansPage.tsx
git commit -m "feat(dashboard): seed gpt-5.4-mini/nano in plan rate editor

Prefills the admin plan editor's per-model rate form with the same
subscription rates migration 045 seeds into the DB, so manually-edited
plans show consistent defaults."
```

---

## Task 5: Manual end-to-end verification

This task is human-driven — it confirms the migration applies cleanly to a real-shape DB and that the catalog/plan rows match the spec.

- [ ] **Step 1: Apply the migration to a scratch DB**

```bash
# In your usual local test database (with TEST_DATABASE_URL pointing at it):
cd /root/coding/modelserver
go test ./internal/store/ -run TestMigration045 -v
```

Expected output (excerpt):

```
=== RUN   TestMigration045_CatalogRowsPresent
--- PASS: TestMigration045_CatalogRowsPresent
=== RUN   TestMigration045_PlansSeeded
--- PASS: TestMigration045_PlansSeeded
=== RUN   TestMigration045_PoliciesSeeded
--- PASS: TestMigration045_PoliciesSeeded   (or SKIP on fresh installs)
```

- [ ] **Step 2: Catalog spot-check via psql**

```bash
psql "$TEST_DATABASE_URL" -c "SELECT name, default_credit_rate FROM models WHERE name LIKE 'gpt-5.4-%' ORDER BY name;"
```

Expected: rows for `gpt-5.4`, `gpt-5.4-mini`, `gpt-5.4-nano`, `gpt-5.4-pro`. The mini/nano JSONB matches the values in the rate reference table at the top of this plan.

- [ ] **Step 3: Plan spot-check via psql**

```bash
psql "$TEST_DATABASE_URL" -c "SELECT slug, model_credit_rates->'gpt-5.4-mini' AS mini, model_credit_rates->'gpt-5.4-nano' AS nano FROM plans WHERE slug IN ('free','pro','max_20x') ORDER BY tier_level;"
```

Expected: every row has non-null `mini` and `nano` columns matching the plan-rate JSON (input 0.0022 / output 0.0175 for mini; input 0.0005 / output 0.0035 for nano).

- [ ] **Step 4: Idempotency check**

Re-run the migration loader (just open a fresh `Store`):

```bash
go test ./internal/store/ -run TestMigration045 -v -count=2
```

Expected: still `PASS`. The `ON CONFLICT DO NOTHING` and `NOT (rates ? key)` guards make every statement a no-op on the second run.

- [ ] **Step 5: Verify no regressions in the broader test suite**

```bash
cd /root/coding/modelserver
go test ./... 2>&1 | tail -30
```

Expected: all previously-passing tests still pass. Tests that hit the DB will skip if `TEST_DATABASE_URL` is unset; that's fine.

- [ ] **Step 6: Push the branch and open the PR**

```bash
git push -u origin feat/gpt-5-4-mini-nano
gh pr create --base main --title "feat: add gpt-5.4-mini and gpt-5.4-nano" --body "$(cat <<'EOF'
Adds two new OpenAI catalog entries to be deployed in the next release.

## What

- **gpt-5.4-mini** — catalog rate from API \$0.25/\$2 per 1M (input/output); subscription rate = catalog × 0.0654 (same multiplier as gpt-5.5).
- **gpt-5.4-nano** — catalog rate from API \$0.05/\$0.40 per 1M; same multiplier.

Both carry the family-wide \`long_context\` uplift (input ×2, output ×1.5 above 272K input tokens).

## Where

- \`internal/store/migrations/045_add_gpt_5_4_mini_nano.sql\` — catalog INSERT + plan/policy seeds, idempotent.
- \`internal/admin/handle_utilization_analysis.go\` — base rate map updated for the OLS regression.
- \`dashboard/src/pages/admin/PlansPage.tsx\` — admin plan editor prefill.

## After deploy

Admin still needs to add both models to the OpenAI upstream's \`supported_models\` before traffic flows (same as gpt-5.5 launch).

Spec: \`docs/superpowers/specs/2026-06-11-gpt-5-4-mini-nano-design.md\`

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR opens against `main`. Note the trailing reminder about `supported_models` so the operator knows there's a manual step post-deploy.

---

## Post-deploy operator checklist (NOT part of this plan, for reference only)

After the PR merges and the next deploy completes, an operator must:

1. In the admin UI, open the OpenAI-family upstream's "Supported Models" editor.
2. Add `gpt-5.4-mini` and `gpt-5.4-nano` to its `supported_models` list.
3. Verify a test request to each model routes correctly and bills at the catalog rate (extra-usage) or plan rate (subscription).

This step is intentionally manual — modelserver's policy is to never auto-route a newly-cataloged model until an operator has confirmed the upstream can serve it.
