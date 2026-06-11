# Add gpt-5.4-mini and gpt-5.4-nano support

**Date:** 2026-06-11
**Status:** Approved, ready for plan

## Goal

Register two new OpenAI catalog entries — `gpt-5.4-mini` and `gpt-5.4-nano` — so
they (a) show up in the admin model picker, (b) bill correctly through the
extra-usage path at the official API rate, and (c) carry a calibrated
subscription discount on every existing plan (mirroring the gpt-5.5 setup).

The change is rate-only and catalog-only: no router, parser, or provider code
needs to be touched. Modelserver routes by model name string, so once the
catalog entry exists and an upstream's `supported_models` is updated by an
admin, traffic flows automatically.

## Rate calculations

API prices (per 1M tokens) match OpenAI's `gpt-5-mini` and `gpt-5-nano`:

| model | input | cached_input | output |
|---|---|---|---|
| gpt-5.4-mini | $0.25 | $0.025 | $2.00 |
| gpt-5.4-nano | $0.05 | $0.005 | $0.40 |

**Catalog rate = API price ÷ 7.5** (the project-wide conversion, documented in
`001_init.sql:240`). This is what `computeExtraUsageCostFen` reads, so it must
equal the official rate to bill extra-usage correctly:

| model | input | output | cache_creation | cache_read |
|---|---|---|---|---|
| gpt-5.4-mini | 0.033 | 0.267 | 0 | 0.003 |
| gpt-5.4-nano | 0.007 | 0.053 | 0 | 0.001 |

**Plan rate = catalog × 0.0654** (the calibrated subscription multiplier from
`030_update_gpt_5_5_credit_rates.sql`, matching gpt-5.5). This is what
subscription consumers see when burning down their 5h/7d windows. Rounded to
the same precision style as the existing gpt-5.5 row:

| model | input | output | cache_creation | cache_read |
|---|---|---|---|---|
| gpt-5.4-mini | 0.0022 | 0.0175 | 0 | 0.0002 |
| gpt-5.4-nano | 0.0005 | 0.0035 | 0 | 0.0001 |

Spot-check (consistency with gpt-5.5):
- gpt-5.5 plan input 0.044 ÷ catalog 0.667 = 0.0660 ≈ multiplier
- gpt-5.4-mini plan input 0.0022 ÷ catalog 0.033 = 0.0667 — same range ✓

**long_context multiplier** applies to both: `{threshold_input_tokens: 272000,
input_multiplier: 2.0, output_multiplier: 1.5}`, consistent with gpt-5.4 /
gpt-5.5 / *-pro per `032_openai_long_context_pricing.sql`.

## File changes

### 1. New migration `internal/store/migrations/045_add_gpt_5_4_mini_nano.sql`

Pattern: combine `027_add_gpt_5_5.sql` (catalog INSERT + per-plan seed) with
the `rate_limit_policies` update from `032_openai_long_context_pricing.sql`.

- `INSERT INTO models ... ON CONFLICT (name) DO NOTHING` for both rows. Each
  catalog row carries the full official rate including `long_context`,
  publisher `openai`, status `active`, empty aliases/metadata.
- `UPDATE plans SET model_credit_rates = jsonb_set(..., '{gpt-5.4-mini}',
  <plan_rate_json>, true) WHERE NOT (model_credit_rates ? 'gpt-5.4-mini')` —
  idempotent and preserves any operator-set override. Same for `-nano`.
- Same two `UPDATE` patterns against `rate_limit_policies` so per-policy
  custom rates pick the new models up too.

All four `UPDATE`s set `updated_at = NOW()`.

### 2. `internal/admin/handle_utilization_analysis.go`

Add two entries to `utilizationAnalysisBaseRates` using the **plan rate** (this
table is consumed by the OLS regression for subscription-window utilization,
not the API-price path). Include `LongContext`. Place them next to the other
`gpt-5.*` rows in `gpt-5.x` descending order.

### 3. `dashboard/src/pages/admin/PlansPage.tsx`

Add two entries to `KNOWN_RATES` using the **plan rate** (admins editing a
plan will get these prefilled when adding the model). Include `long_context`
with the same shape as `gpt-5.5`.

## Out of scope

- **Router/parser/provider code** — model name routing is dynamic.
- **Upstream `supported_models`** — operator does this in admin UI after
  deploy, same as gpt-5.5 launch.
- **044 (1.2× plan price bump)** — `price_per_period` is fen, independent of
  these credit rates. No interaction.
- **GPT-5.4-mini-pro / GPT-5.4-nano-pro variants** — not requested; can be
  added later if OpenAI publishes them.

## Idempotency and rollback

- Migration is gated by `schema_migrations` (runs once per DB).
- `ON CONFLICT DO NOTHING` on the catalog INSERT — re-runs are no-ops.
- `NOT (model_credit_rates ? 'gpt-5.4-mini')` guards on UPDATEs — preserves
  any operator-set custom rate set between deploy and re-run.
- Forward-only; no down migration needed. To remove, an operator would write a
  follow-up migration deleting the catalog row + stripping the keys from
  `plans.model_credit_rates` and `rate_limit_policies.model_credit_rates`.

## JSON payload (verbatim, for reference)

**Catalog `default_credit_rate`:**

```json
gpt-5.4-mini: {"input_rate":0.033,"output_rate":0.267,"cache_creation_rate":0,"cache_read_rate":0.003,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}
gpt-5.4-nano: {"input_rate":0.007,"output_rate":0.053,"cache_creation_rate":0,"cache_read_rate":0.001,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}
```

**Plan / policy `model_credit_rates` entry:**

```json
gpt-5.4-mini: {"input_rate":0.0022,"output_rate":0.0175,"cache_creation_rate":0,"cache_read_rate":0.0002,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}
gpt-5.4-nano: {"input_rate":0.0005,"output_rate":0.0035,"cache_creation_rate":0,"cache_read_rate":0.0001,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}
```

## Verification checklist

After implementation:

1. `cd internal/store && go test ./...` — migration tests pass (especially any
   migration-ordering test that loads the full chain).
2. `cd internal/admin && go test ./...` — `handle_utilization_analysis` tests
   still pass; if they enumerate base rates, add the new models to fixtures.
3. `cd dashboard && npm run typecheck && npm run lint` — TS additions compile.
4. Manually apply migration to a scratch DB, then `SELECT name,
   default_credit_rate FROM models WHERE name LIKE 'gpt-5.4-%';` to confirm
   both rows present with the expected JSONB.
5. Manually verify one plan: `SELECT slug, model_credit_rates->'gpt-5.4-mini'
   FROM plans WHERE slug = 'pro';` — should match the plan-rate JSON above.
