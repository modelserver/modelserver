# gpt-5.x subscription rate rebase (catalog × 0.1)

**Date:** 2026-06-17
**Status:** Approved, ready for plan

## Goal

Re-anchor every gpt-5.x subscription rate to **catalog × 0.1**, and at
the same time backfill the missing catalog rows so the formula is
self-consistent across the codebase. After this migration, every gpt-5.x
plan entry can be re-derived from its catalog row with a single
multiplier — replacing the current grab-bag of three different
calibration eras (0.066, 1.0, 0.132, pinned).

This is the second of two related changes. The first
(`2026-06-17-glm-5.2-model-design.md`, migration 046) adds glm-5.2; this
spec covers the gpt rebase only.

## Why now

Live API audit (codeapi.cs.ac.cn, 2026-06-17) shows the current state is
inconsistent and surprising:

| model | catalog input | plan input | effective ratio |
|---|---|---|---|
| gpt-5.5 | 0.667 | 0.044 | 0.066 |
| gpt-5.4 | 0.333 | 0.044 | 0.132 |
| gpt-5.4-mini | 0.033 | 0.0022 | 0.067 |
| gpt-5.4-nano | 0.007 | 0.0005 | 0.071 |
| gpt-5.3-codex | 0.233 | 0.233 | **1.0** |
| gpt-5.2 / 5.2-codex | NULL | 0.233 | – |
| gpt-5.1 / -codex / -codex-max | NULL | 0.167 | – |
| gpt-5.1-codex-mini | NULL | 0.033 | – |
| codex-auto-review | 0.233 | 0.044 | 0.19 (pinned to gpt-5.5) |

Three problems:
1. **gpt-5.3-codex** still bills at API price inside subscriptions
   (ratio 1.0) — far more expensive than its peers per dollar spent.
2. **gpt-5.1-* / gpt-5.2-* catalog rates are NULL**, so the extra-usage
   path under-bills these models (it silently treats them as free), and
   the per-project savings stat (`internal/billing/savings.go`) skips
   them entirely.
3. **Different ratios across the family** make it hard to predict burn
   rate when admins look at the price difference between two models.

## Scope

In scope:
- One database migration (`047_gpt_5_x_plan_rebase.sql`).
- Backfill `default_credit_rate` for 6 models (gpt-5.1, gpt-5.1-codex,
  gpt-5.1-codex-max, gpt-5.1-codex-mini, gpt-5.2, gpt-5.2-codex).
- Overwrite `model_credit_rates['<name>']` in every `plans` row for 12
  gpt models (full list below).
- Same overwrite in every `rate_limit_policies` row.
- Update `internal/admin/handle_utilization_analysis.go`
  `utilizationAnalysisBaseRates` to the new plan numbers.
- Update `dashboard/src/pages/admin/PlansPage.tsx`
  `DEFAULT_MODEL_CREDIT_RATES` to the new plan numbers.

Out of scope:
- **gpt-5.4-pro / gpt-5.5-pro**: these have catalog rates ($4/$24) but
  no plan entry (they currently fall back to `_default` = 0.4 / 2.0).
  Adding them is a separate question — for now they continue to use the
  fallback.
- **gpt-5.5-codex**, **gpt-5.6 / 5.7 / etc.**: don't exist in catalog
  today, no action.
- **claude-* / gemini-* / deepseek-* / glm-*** models: untouched.
- **`_default` entry**: untouched (Claude Sonnet equivalent).
- **Pricing for legacy gpt-5.1 / gpt-5.1-codex** (NULL today, no
  publicly-listed OpenAI history): we use the same numbers as
  `gpt-5.1-codex-max` ($1.25/$10/$0.125). Documented in the migration
  header.

## Catalog backfill (USD ÷ 7.5)

API prices taken from migration `001_init.sql:248-254` notes:

| model | API input | API output | API cache_read | catalog input | output | cache_read |
|---|---|---|---|---|---|---|
| gpt-5.1 | $1.25 | $10 | $0.125 | 0.167 | 1.333 | 0.017 |
| gpt-5.1-codex | $1.25 | $10 | $0.125 | 0.167 | 1.333 | 0.017 |
| gpt-5.1-codex-max | $1.25 | $10 | $0.125 | 0.167 | 1.333 | 0.017 |
| gpt-5.1-codex-mini | $0.25 | $2 | $0.025 | 0.033 | 0.267 | 0.003 |
| gpt-5.2 | $1.75 | $14 | $0.175 | 0.233 | 1.867 | 0.023 |
| gpt-5.2-codex | $1.75 | $14 | $0.175 | 0.233 | 1.867 | 0.023 |

Backfill uses `UPDATE models SET default_credit_rate = … WHERE name = …
AND default_credit_rate IS NULL` — guarded so any operator-set value
between deploy and re-run wins (same idempotency convention as 035).

## Plan rate matrix (catalog × 0.1)

Applies uniformly to every plan slug (free, pro, max_2x, max_5x,
max_20x, max_40x, max_60x, max_80x, max_100x, max_120x, max_200x,
max_240x) and every `rate_limit_policies` row.

| model | input | output | cache_creation | cache_read | long_context |
|---|---|---|---|---|---|
| gpt-5.5 | 0.0667 | 0.4 | 0 | 0.0067 | – |
| gpt-5.4 | 0.0333 | 0.2 | 0 | 0.0033 | – |
| gpt-5.4-mini | 0.0033 | 0.0267 | 0 | 0.0003 | **{272000, 2.0, 1.5}** |
| gpt-5.4-nano | 0.0007 | 0.0053 | 0 | 0.0001 | **{272000, 2.0, 1.5}** |
| gpt-5.3-codex | 0.0233 | 0.1867 | 0 | 0.0023 | – |
| gpt-5.2 | 0.0233 | 0.1867 | 0 | 0.0023 | – |
| gpt-5.2-codex | 0.0233 | 0.1867 | 0 | 0.0023 | – |
| gpt-5.1 | 0.0167 | 0.1333 | 0 | 0.0017 | – |
| gpt-5.1-codex | 0.0167 | 0.1333 | 0 | 0.0017 | – |
| gpt-5.1-codex-max | 0.0167 | 0.1333 | 0 | 0.0017 | – |
| gpt-5.1-codex-mini | 0.0033 | 0.0267 | 0 | 0.0003 | – |
| codex-auto-review | 0.0233 | 0.1867 | 0 | 0.0023 | – |

### Subscriber burn-rate impact

| model | old plan input | new plan input | burn change |
|---|---|---|---|
| gpt-5.5 | 0.044 | 0.0667 | **+52%** cost |
| gpt-5.4 | 0.044 | 0.0333 | -24% cost |
| gpt-5.4-mini | 0.0022 | 0.0033 | **+50%** cost |
| gpt-5.4-nano | 0.0005 | 0.0007 | **+40%** cost |
| gpt-5.3-codex | 0.233 | 0.0233 | **-90%** cost |
| gpt-5.2 | 0.233 | 0.0233 | -90% cost |
| gpt-5.2-codex | 0.233 | 0.0233 | -90% cost |
| gpt-5.1-codex-max | 0.167 | 0.0167 | -90% cost |
| gpt-5.1-codex-mini | 0.033 | 0.0033 | -90% cost |
| codex-auto-review | 0.044 | 0.0233 | -47% cost (was pinned) |

Direction is correct on net: codex (the previously-1.0-ratio family)
gets meaningfully cheaper inside subscriptions, gpt-5.5 — the current
loss leader — moves up toward parity with the rest of the family.

## Migration shape

`internal/store/migrations/047_gpt_5_x_plan_rebase.sql`:

```
-- 1) Catalog backfill (only updates NULL rows)
UPDATE models SET default_credit_rate = '{...}'::jsonb, updated_at = NOW()
 WHERE name = 'gpt-5.1' AND default_credit_rate IS NULL;
-- … 5 more, one per backfilled model …

-- 2) Plan rebase: shallow-merge the 12 new entries into every plan's
--    model_credit_rates. Unconditional (no NOT-? guard) because the
--    whole point is to overwrite the existing entries.
UPDATE plans
   SET model_credit_rates = COALESCE(model_credit_rates, '{}'::jsonb) || '{
       "gpt-5.5":            {…},
       "gpt-5.4":            {…},
       "gpt-5.4-mini":       {…long_context block…},
       …all 12…
   }'::jsonb,
       updated_at = NOW();

-- 3) Same against rate_limit_policies
UPDATE rate_limit_policies
   SET model_credit_rates = COALESCE(model_credit_rates, '{}'::jsonb) || '{
       …same 12 entries…
   }'::jsonb,
       updated_at = NOW();
```

`||` shallow-merge overwrites any existing entries with the listed
names but leaves other model entries (claude-*, deepseek-*, glm-5.2 from
046, `_default`) intact. Re-running the migration is safe but
re-overwrites — that's intentional, the catalog backfill guard is the
only "preserve operator value" path here. Plan/policy rebase is meant
to be authoritative.

## Code changes

### `internal/admin/handle_utilization_analysis.go`

Replace the existing gpt-5.x entries in `utilizationAnalysisBaseRates`
with the new plan numbers. Add the 6 backfilled models. Result:

```go
"gpt-5.5":            {InputRate: 0.0667, OutputRate: 0.4, CacheCreationRate: 0, CacheReadRate: 0.0067},
"gpt-5.4":            {InputRate: 0.0333, OutputRate: 0.2, CacheCreationRate: 0, CacheReadRate: 0.0033},
"gpt-5.4-mini":       {InputRate: 0.0033, OutputRate: 0.0267, CacheCreationRate: 0, CacheReadRate: 0.0003, LongContext: &types.LongContextCreditRate{ThresholdInputTokens: 272000, InputMultiplier: 2.0, OutputMultiplier: 1.5}},
"gpt-5.4-nano":       {InputRate: 0.0007, OutputRate: 0.0053, CacheCreationRate: 0, CacheReadRate: 0.0001, LongContext: &types.LongContextCreditRate{ThresholdInputTokens: 272000, InputMultiplier: 2.0, OutputMultiplier: 1.5}},
"gpt-5.3-codex":      {InputRate: 0.0233, OutputRate: 0.1867, CacheCreationRate: 0, CacheReadRate: 0.0023},
"codex-auto-review":  {InputRate: 0.0233, OutputRate: 0.1867, CacheCreationRate: 0, CacheReadRate: 0.0023},
"gpt-5.2":            {InputRate: 0.0233, OutputRate: 0.1867, CacheCreationRate: 0, CacheReadRate: 0.0023},
"gpt-5.2-codex":      {InputRate: 0.0233, OutputRate: 0.1867, CacheCreationRate: 0, CacheReadRate: 0.0023},
"gpt-5.1":            {InputRate: 0.0167, OutputRate: 0.1333, CacheCreationRate: 0, CacheReadRate: 0.0017},
"gpt-5.1-codex":      {InputRate: 0.0167, OutputRate: 0.1333, CacheCreationRate: 0, CacheReadRate: 0.0017},
"gpt-5.1-codex-max":  {InputRate: 0.0167, OutputRate: 0.1333, CacheCreationRate: 0, CacheReadRate: 0.0017},
"gpt-5.1-codex-mini": {InputRate: 0.0033, OutputRate: 0.0267, CacheCreationRate: 0, CacheReadRate: 0.0003},
```

The existing `TestUtilizationAnalysisBaseRates_GPT55SubscriptionDiscount`
test asserts specific numbers — update the expected values along with
the table, OR delete the test if its premise (the gpt-5.5-specific
discount ratio) no longer makes sense in the rebased world. We'll
update the expected numbers and keep the test as a regression guard.

### `dashboard/src/pages/admin/PlansPage.tsx`

Update the existing `gpt-5.5`, `gpt-5.4-mini`, `gpt-5.4-nano` entries
in `DEFAULT_MODEL_CREDIT_RATES`. Add new entries for `gpt-5.4`,
`gpt-5.3-codex`, `gpt-5.2`, `gpt-5.2-codex`, `gpt-5.1`,
`gpt-5.1-codex`, `gpt-5.1-codex-max`, `gpt-5.1-codex-mini`,
`codex-auto-review`. (12 total, matching the migration.)

## Idempotency and rollback

- Migration is gated by `schema_migrations` (runs once per DB).
- Catalog backfill UPDATEs are guarded by `IS NULL` — safe and
  preserves operator overrides.
- Plan / policy rebase is **unguarded** (intentional). Re-running
  re-overwrites the 12 entries with the migration's values. Operators
  who want to keep a custom override must apply it after this migration
  runs, not before.
- Forward-only. Rollback = write 048 with whatever values the operator
  wants. The original numbers are recoverable from git (this spec
  documents them in the "Subscriber burn-rate impact" table).

## Risks

- **Subscriber surprise** — gpt-5.5 / gpt-5.4-mini / gpt-5.4-nano users
  burn ~50% faster after this. Subscribers who model their burn from
  the published price will notice. Communication / changelog responsibility
  sits outside this code change.
- **codex-auto-review reverts off its 038 pin.** Original 038 pinned
  it to gpt-5.5's lower rate as a "code review is low-value bursty
  workload" subsidy. With the new × 0.1 rebase, auto-review is
  cheaper than it was at the 038 pin (0.0233 < 0.044 input). Subsidy
  intent preserved, just via a different mechanism.
- **gpt-5.1 catalog prices are educated guesses** (used gpt-5.1-codex-max
  numbers). If OpenAI's published gpt-5.1 base price differs, the
  catalog row is wrong for extra-usage billing. Worst case: ship a
  one-line follow-up migration to fix.
- **Pro variants stay on _default.** gpt-5.4-pro / gpt-5.5-pro have
  catalog rates ($4/$24) but no plan entry — they fall back to
  `_default` (0.4 / 2.0). That's a separate, larger discussion (rare
  models, expensive, "should subscribers be able to use these at all?").
  Out of scope for this migration.

## Verification

Post-deploy, all manual:

1. `SELECT name, default_credit_rate FROM models WHERE name LIKE 'gpt-5.1%' OR name LIKE 'gpt-5.2%';` — no NULL rows remain.
2. `SELECT slug, jsonb_pretty(model_credit_rates) FROM plans WHERE slug = 'max_100x';` — eyeball that the 12 entries match the matrix.
3. `SELECT name, jsonb_pretty(model_credit_rates) FROM rate_limit_policies LIMIT 1;` — same shape.
4. Spot-check via `GET /api/v1/plans/{anyPlanID}` over the admin token — same 12 rates appear.
5. Smoke-test a `gpt-5.3-codex` request under a Pro subscription and confirm `credits_consumed` is ~10× smaller per request than before.

## Bundling with glm-5.2 (046)

These two migrations are intentionally separate so each has a clean
self-contained diff and a clean rollback story. Order is 046 → 047
because lexicographic; they don't touch each other's rows.

## Sources

- Live API state (2026-06-17): https://codeapi.cs.ac.cn/api/v1/plans, /api/v1/models
- `internal/store/migrations/001_init.sql:240-254` (historical OpenAI prices)
- `internal/store/migrations/030_update_gpt_5_5_credit_rates.sql` (original calibration this rebase replaces)
- `internal/store/migrations/038_add_codex_auto_review.sql` (auto-review pin we are reverting from)
- `internal/store/migrations/045_add_gpt_5_4_mini_nano.sql` (pattern reference)
