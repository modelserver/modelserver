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
