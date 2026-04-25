-- 032_openai_long_context_pricing.sql
--
-- Two-fold change:
--
-- 1. Separate "official API price" (catalog) from "subscription discount"
--    (plans / rate_limit_policies) for gpt-5.5. Migration 030 wrote the
--    subscription discount into models.default_credit_rate, but the
--    extra-usage path (computeExtraUsageCostFen) reads catalog directly and
--    must bill at OpenAI's official rate. We restore the official rate at
--    the catalog and keep the calibrated subscription rate in plans.
--
-- 2. Attach long-context pricing on both sides. OpenAI charges 2x on input
--    (incl. cache) and 1.5x on output for the whole request when total input
--    tokens exceed 272K. The uplift is encoded in the existing JSONB rate
--    payload so no schema change is required.
--
-- gpt-5.4 has no separate subscription discount, so its catalog and plan
-- rates already match; only long_context is appended for it. The Pro
-- variants are inserted fresh — they have no plan-level subscription rate,
-- so subscribers fall through to the catalog (intentionally premium).

-- 1) Catalog rates: official API price + long_context. The ON CONFLICT
--    overwrite is deliberate and supersedes 030's catalog-side update.
WITH official_rates(name, display_name, description, default_credit_rate) AS (
    VALUES
        (
            'gpt-5.5',
            'GPT-5.5',
            'A new class of intelligence for coding and professional work.',
            '{"input_rate":0.667,"output_rate":4.0,"cache_creation_rate":0,"cache_read_rate":0.067,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb
        ),
        (
            'gpt-5.5-pro',
            'GPT-5.5 Pro',
            'GPT-5.5 variant for high-intelligence professional workloads.',
            '{"input_rate":4.0,"output_rate":24.0,"cache_creation_rate":0,"cache_read_rate":0,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb
        ),
        (
            'gpt-5.4',
            'GPT-5.4',
            'Flagship OpenAI GPT model.',
            '{"input_rate":0.333,"output_rate":2.0,"cache_creation_rate":0,"cache_read_rate":0.033,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb
        ),
        (
            'gpt-5.4-pro',
            'GPT-5.4 Pro',
            'GPT-5.4 variant for high-intelligence professional workloads.',
            '{"input_rate":4.0,"output_rate":24.0,"cache_creation_rate":0,"cache_read_rate":0,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb
        )
)
INSERT INTO models (name, display_name, description, aliases, default_credit_rate, status, publisher, metadata)
SELECT name, display_name, description, '{}', default_credit_rate, 'active', 'openai', '{}'::jsonb
FROM official_rates
ON CONFLICT (name) DO UPDATE
SET default_credit_rate = EXCLUDED.default_credit_rate,
    publisher = CASE WHEN models.publisher = '' THEN 'openai' ELSE models.publisher END,
    updated_at = NOW();

-- 2) gpt-5.5 plan / policy rate: enforce subscription discount + long_context.
--    Replace the entire object so any drifted base rate (e.g. an entry left
--    at the official price from before 027/030) is corrected, not just
--    decorated with long_context.
UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.5}',
        '{"input_rate":0.044,"output_rate":0.261,"cache_creation_rate":0,"cache_read_rate":0.0044,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates ? 'gpt-5.5';

UPDATE rate_limit_policies
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.5}',
        '{"input_rate":0.044,"output_rate":0.261,"cache_creation_rate":0,"cache_read_rate":0.0044,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates ? 'gpt-5.5';

-- 3) gpt-5.4 / gpt-5.4-pro / gpt-5.5-pro: no separate subscription discount,
--    so leave any existing base rate alone and only append long_context.
UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.4,long_context}',
        '{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates ? 'gpt-5.4';

UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.4-pro,long_context}',
        '{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates ? 'gpt-5.4-pro';

UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.5-pro,long_context}',
        '{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates ? 'gpt-5.5-pro';

UPDATE rate_limit_policies
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.4,long_context}',
        '{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates ? 'gpt-5.4';

UPDATE rate_limit_policies
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.4-pro,long_context}',
        '{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates ? 'gpt-5.4-pro';

UPDATE rate_limit_policies
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.5-pro,long_context}',
        '{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates ? 'gpt-5.5-pro';
