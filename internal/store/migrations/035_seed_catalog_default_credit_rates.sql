-- 035_seed_catalog_default_credit_rates.sql
--
-- Backfill `models.default_credit_rate` for catalog rows that were left
-- NULL when the model was first added. This column is the input to the
-- per-project savings stat (internal/billing/savings.go::
-- ComputeCostBreakdown), which silently skips any model whose default
-- rate is nil. With most Claude models nil in production, the stat was
-- understating API-standard cost by ~95% for projects whose usage is
-- predominantly Claude — making paid plans look like a net loss to the
-- user.
--
-- Rates are taken from the equivalent plan-level model_credit_rates
-- defined in 001_init.sql / 013_add_max_80x_plan.sql / 015_add_opus_4_7_pricing.sql,
-- which are the system-internal "API standard price" values for each
-- model under the subscription formula (credit_rate = API_price / 7.5).
--
-- Idempotent: every UPDATE is guarded by `default_credit_rate IS NULL`,
-- so re-running this migration (or running it after an admin manually
-- set a rate via the UI) leaves existing rows alone.

-- Anthropic Claude — Opus tier ($5 input / $25 output / $5 cache_creation / cache_read free)
UPDATE models SET default_credit_rate = '{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0}'::jsonb, updated_at = NOW()
 WHERE name IN ('claude-opus-4-5', 'claude-opus-4-5-20251101', 'claude-opus-4-6', 'claude-opus-4-7')
   AND default_credit_rate IS NULL;

-- Anthropic Claude — Sonnet tier ($3 input / $15 output / $3 cache_creation / cache_read free)
UPDATE models SET default_credit_rate = '{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0}'::jsonb, updated_at = NOW()
 WHERE name = 'claude-sonnet-4-6'
   AND default_credit_rate IS NULL;

-- Anthropic Claude — Haiku tier ($1 input / $5 output / $1 cache_creation / cache_read free)
UPDATE models SET default_credit_rate = '{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0}'::jsonb, updated_at = NOW()
 WHERE name IN ('claude-haiku-4-5', 'claude-haiku-4-5-20251001')
   AND default_credit_rate IS NULL;

-- OpenAI GPT-5.3-codex — derived from plan rates in 001_init.sql.
UPDATE models SET default_credit_rate = '{"input_rate":0.233,"output_rate":1.867,"cache_creation_rate":0,"cache_read_rate":0.023}'::jsonb, updated_at = NOW()
 WHERE name = 'gpt-5.3-codex'
   AND default_credit_rate IS NULL;

-- Models intentionally left without a default rate by this migration:
--   gemini-3-*, glm-*, kimi-*, qwen3*, minimax-*, gpt-5.1*, gpt-5.2*,
--   gpt-image-2 (image-only model with default_image_credit_rate),
--   kimi-k2-thinking
-- These either (a) have negligible usage in production today, (b) have
-- a non-public price the operator should set via the admin UI, or (c)
-- only ship image rates (handled by default_image_credit_rate, not this
-- column). Each can be added in a follow-up migration as needed.
