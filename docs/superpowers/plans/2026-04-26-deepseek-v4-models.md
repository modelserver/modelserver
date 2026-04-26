# DeepSeek V4 Models Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `deepseek-v4-flash` and `deepseek-v4-pro` to the model catalog and to all 9 subscription plans through a single, idempotent SQL migration.

**Architecture:** Pure data migration. Two `INSERT … ON CONFLICT DO UPDATE` rows in `models`, plus one `UPDATE plans … SET model_credit_rates = model_credit_rates || jsonb` that merges two new entries into every plan. No Go code changes — existing `AnthropicTransformer` and `OpenAITransformer` already accept arbitrary `upstream.BaseURL`, so the new models can be served by a deployment-side upstream pointing at `https://api.deepseek.com` (OpenAI protocol) or `https://api.deepseek.com/anthropic` (Anthropic protocol).

**Tech Stack:** PostgreSQL, jsonb, Go embed-based migration runner (`internal/store/store.go:15`).

**Spec:** `docs/superpowers/specs/2026-04-26-deepseek-v4-models-design.md`

---

## File map

- Create: `internal/store/migrations/033_deepseek_v4.sql`

That's it. Nothing else changes.

The migration runner (`internal/store/store.go:74-101`) reads files from `migrations/` in sorted order and applies any whose name comes lexicographically after the latest `schema_migrations` row. `033_*.sql` will be picked up automatically; no Go code, no registration needed.

---

### Task 1: Write the DeepSeek V4 migration

**Files:**
- Create: `/root/coding/modelserver/internal/store/migrations/033_deepseek_v4.sql`

- [ ] **Step 1: Create the migration file**

Write this exact content to `/root/coding/modelserver/internal/store/migrations/033_deepseek_v4.sql`:

```sql
-- 033_deepseek_v4.sql
--
-- Register DeepSeek V4 models (flash + pro) in the catalog and add their
-- subscription rates to every existing plan.
--
-- Catalog rates are derived from DeepSeek's published CNY pricing
-- (https://api-docs.deepseek.com/zh-cn/quick_start/pricing) using:
--     credits_per_token = price_yuan_per_M_tokens / 54.38
-- where 54.38 = credit_price_fen (5438) / 100. cache_creation_rate is 0
-- because DeepSeek does not bill cache creation as a separate event.
--
--   deepseek-v4-flash: input ¥1/M  output ¥2/M  cache_hit ¥0.02/M
--   deepseek-v4-pro:   input ¥3/M  output ¥6/M  cache_hit ¥0.025/M
--
-- The 2.5x v4-pro discount window (through 2026-05-05) is intentionally
-- not modelled here — full price is stored, and users may overpay during
-- that ~9-day window. Discount is not in scope for this migration.
--
-- Subscription rates are catalog x 0.066, matching the gpt-5.5 catalog->
-- subscription compression ratio used in migration 030. All 9 plans get
-- the same numbers (no per-tier compression — same convention as gpt-5.5,
-- gpt-5.x-codex-* in plans).
--
-- Routes and upstreams are intentionally not seeded here; operators add
-- a deepseek upstream + group + route in the admin UI after deployment.
-- Either provider works:
--   provider="anthropic" + base_url="https://api.deepseek.com/anthropic"
--   provider="openai"    + base_url="https://api.deepseek.com"

INSERT INTO models (
    name,
    display_name,
    description,
    aliases,
    default_credit_rate,
    status,
    publisher,
    metadata
)
VALUES
    (
        'deepseek-v4-flash',
        'DeepSeek V4 Flash',
        'DeepSeek V4 Flash — fast non-thinking model, 1M context.',
        '{}',
        '{"input_rate":0.01838,"output_rate":0.03677,"cache_creation_rate":0,"cache_read_rate":0.000368}'::jsonb,
        'active',
        'deepseek',
        '{"context_window":1000000,"category":"chat"}'::jsonb
    ),
    (
        'deepseek-v4-pro',
        'DeepSeek V4 Pro',
        'DeepSeek V4 Pro — thinking-mode capable, 1M context.',
        '{}',
        '{"input_rate":0.05517,"output_rate":0.11034,"cache_creation_rate":0,"cache_read_rate":0.000460}'::jsonb,
        'active',
        'deepseek',
        '{"context_window":1000000,"category":"chat"}'::jsonb
    )
ON CONFLICT (name) DO UPDATE SET
    display_name = EXCLUDED.display_name,
    description = EXCLUDED.description,
    publisher = EXCLUDED.publisher,
    default_credit_rate = EXCLUDED.default_credit_rate,
    metadata = EXCLUDED.metadata,
    status = EXCLUDED.status,
    updated_at = NOW();

-- Merge the two model entries into every plan's model_credit_rates.
-- Single statement, no WHERE — applies to all 9 plans uniformly.
-- The `||` operator on jsonb does a shallow merge that overwrites any
-- existing entries with these names, so this is safe to re-run.
UPDATE plans
SET model_credit_rates = COALESCE(model_credit_rates, '{}'::jsonb) || '{
    "deepseek-v4-flash": {"input_rate":0.001213,"output_rate":0.002427,"cache_creation_rate":0,"cache_read_rate":0.0000243},
    "deepseek-v4-pro":   {"input_rate":0.003641,"output_rate":0.007283,"cache_creation_rate":0,"cache_read_rate":0.0000304}
}'::jsonb,
    updated_at = NOW();
```

- [ ] **Step 2: Verify the file has no obvious syntax/JSON issues**

The migration embeds JSON literals inside SQL. Run a quick external JSON parse on each `::jsonb` literal to catch typos.

Run:
```bash
cd /root/coding/modelserver && python3 - <<'PY'
import json
literals = [
    '{"input_rate":0.01838,"output_rate":0.03677,"cache_creation_rate":0,"cache_read_rate":0.000368}',
    '{"input_rate":0.05517,"output_rate":0.11034,"cache_creation_rate":0,"cache_read_rate":0.000460}',
    '{"context_window":1000000,"category":"chat"}',
    '{"deepseek-v4-flash": {"input_rate":0.001213,"output_rate":0.002427,"cache_creation_rate":0,"cache_read_rate":0.0000243}, "deepseek-v4-pro": {"input_rate":0.003641,"output_rate":0.007283,"cache_creation_rate":0,"cache_read_rate":0.0000304}}',
]
for s in literals:
    json.loads(s)
print("ok: all 4 JSON literals parse")
PY
```

Expected: `ok: all 4 JSON literals parse`

- [ ] **Step 3: Verify the migration is picked up by the embed-based loader**

Run: `cd /root/coding/modelserver && go test ./internal/store/... -count=1 -run TestMigrationsEmbed`
Expected: `ok` — the test reads `migrations/` from the embed FS and asserts files are sorted; `033_deepseek_v4.sql` will appear after `032_*` and pass the order check.

- [ ] **Step 4: Verify the whole project still builds**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: exit 0.

- [ ] **Step 5: Verify nothing else regressed**

Run: `cd /root/coding/modelserver && go test ./... -count=1`
Expected: all packages either `ok` or `[no test files]`.

- [ ] **Step 6: Commit**

```bash
git add internal/store/migrations/033_deepseek_v4.sql
git commit -m "feat(billing): seed deepseek-v4-flash and deepseek-v4-pro models"
```

---

## Self-review checklist (run after writing the plan)

- **Spec coverage**: spec calls for one migration (catalog rows + plan merges). Task 1 produces exactly that. ✓
- **No code changes**: spec confirms transformers already work via `upstream.BaseURL`. Plan adds no Go files. ✓
- **Idempotent**: `ON CONFLICT … DO UPDATE` on `models` and `||` jsonb merge on `plans` make re-running the migration safe (matches existing pattern from 030/031). ✓
- **Operational steps not in plan**: spec calls out upstream/group/route as post-deploy admin UI work, not migration. Plan correctly omits. ✓
- **Numbers consistent**: catalog/subscription rates are the same in spec and plan. ✓
- **No placeholders / TBDs**. ✓

---

## Post-deployment verification (manual, by operator)

Not part of plan execution — listed for the deployer:

1. After deploying, hit `GET /api/v1/models` and confirm both models appear with the expected `default_credit_rate`.
2. Hit `GET /api/v1/plans/{anyPlanID}` and confirm `model_credit_rates` includes both new entries.
3. In the admin UI: create a deepseek upstream (OpenAI or Anthropic protocol — both work), wrap it in an upstream group, add a route binding `[deepseek-v4-flash, deepseek-v4-pro]` → that group.
4. Smoke-test a streaming and a non-streaming call via the chosen protocol; confirm `requests` table records non-zero `input_tokens`/`output_tokens`/`credits_consumed`.
5. If using the Anthropic-compat endpoint, watch for SSE / cache-token field name mismatches noted in the spec's "Risks" section.
