-- 042_add_opus_4_8.sql
--
-- Register claude-opus-4-8 in the model catalog and seed its credit rate
-- into every existing plan's model_credit_rates map.
--
-- Anthropic keeps the Opus tier at $5 input / $25 output per MTok with
-- $5 cache_creation; the project convention (see 015_add_opus_4_7_pricing.sql
-- and 035_seed_catalog_default_credit_rates.sql) treats cache_read as free
-- for the Opus tier. Under the subscription formula (API_price / 7.5):
--   input_rate          = 5 / 7.5  = 0.667
--   output_rate         = 25 / 7.5 = 3.333
--   cache_creation_rate = 5 / 7.5  = 0.667
--   cache_read_rate     = 0
-- These rates match every prior Opus row (4-5, 4-6, 4-7).
--
-- Routes and upstreams are intentionally not seeded — operators wire the
-- model into the relevant anthropic / claudecode / bedrock / vertex-anthropic
-- upstream group via the admin UI after deployment. The proxy layer already
-- forwards arbitrary Anthropic model names transparently, so once this row
-- exists the model is selectable in the routing UI and has a fallback
-- default rate for billing/savings paths.

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
VALUES (
    'claude-opus-4-8',
    'Claude Opus 4.8',
    'Anthropic Claude Opus 4.8 — top-tier Claude 4.x model for the most demanding reasoning and coding workloads.',
    '{}',
    '{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0}'::jsonb,
    'active',
    'anthropic',
    '{"category":"chat"}'::jsonb
)
ON CONFLICT (name) DO UPDATE SET
    display_name        = EXCLUDED.display_name,
    description         = EXCLUDED.description,
    publisher           = EXCLUDED.publisher,
    default_credit_rate = EXCLUDED.default_credit_rate,
    metadata            = EXCLUDED.metadata,
    status              = EXCLUDED.status,
    updated_at          = NOW();

-- Add claude-opus-4-8 to every plan that doesn't already define a custom
-- rate for it. The NOT ? guard makes this safe to re-run and preserves any
-- operator-set override (matching the pattern in 015_add_opus_4_7_pricing.sql
-- and 027_add_gpt_5_5.sql).
UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{claude-opus-4-8}',
        '{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE NOT (model_credit_rates ? 'claude-opus-4-8');
