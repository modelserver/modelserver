-- 027_add_gpt_5_5.sql
--
-- Register gpt-5.5 in the model catalog and seed its credit rate into every
-- existing plan's model_credit_rates map. The subscription rates are calibrated
-- against Codex utilization percentages with fixed Max 20x capacities:
--   5h = 11,000,000 credits
--   7d = 83,333,300 credits
-- Current calibrated gpt-5.5 rate:
--   input_rate          0.044
--   output_rate         0.261
--   cache_creation_rate 0      (OpenAI has no separate cache-creation charge)
--   cache_read_rate     0.0044
--
-- The model is registered as 'active' with publisher 'openai'. Admins still
-- need to add it to an upstream's supported_models before traffic can hit it
-- (the catalog entry only makes the name selectable in the routing UI and
-- ensures billing has a default rate for fallback paths).

INSERT INTO models (name, display_name, description, aliases, default_credit_rate, status, publisher, metadata)
VALUES (
    'gpt-5.5',
    'GPT-5.5',
    'A new class of intelligence for coding and professional work.',
    '{}',
    '{"input_rate":0.044,"output_rate":0.261,"cache_creation_rate":0,"cache_read_rate":0.0044}'::jsonb,
    'active',
    'openai',
    '{}'::jsonb
)
ON CONFLICT (name) DO NOTHING;

-- Add gpt-5.5 to every plan that doesn't already define a custom rate for it.
-- The NOT ? guard makes this safe to re-run and preserves any operator-set
-- override.
UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.5}',
        '{"input_rate":0.044,"output_rate":0.261,"cache_creation_rate":0,"cache_read_rate":0.0044}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE NOT (model_credit_rates ? 'gpt-5.5');
