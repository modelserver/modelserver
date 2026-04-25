-- 030_update_gpt_5_5_credit_rates.sql
--
-- Recalibrate gpt-5.5 subscription credit rates against Codex official
-- utilization percentages while keeping Max 20x capacities fixed.
--
-- Old rates were API-price-derived:
--   {"input_rate":0.667,"output_rate":4.0,"cache_creation_rate":0,"cache_read_rate":0.067}
--
-- New rates are the first calibrated estimate from the 5h window:
--   target credits = 11,000,000 * 6% = 660,000
--   observed credits = 10,097,386.538
--   multiplier = 0.06536

UPDATE models
SET default_credit_rate = '{"input_rate":0.044,"output_rate":0.261,"cache_creation_rate":0,"cache_read_rate":0.0044}'::jsonb,
    updated_at = NOW()
WHERE name = 'gpt-5.5'
  AND default_credit_rate = '{"input_rate":0.667,"output_rate":4.0,"cache_creation_rate":0,"cache_read_rate":0.067}'::jsonb;

UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{gpt-5.5}',
        '{"input_rate":0.044,"output_rate":0.261,"cache_creation_rate":0,"cache_read_rate":0.0044}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates -> 'gpt-5.5' = '{"input_rate":0.667,"output_rate":4.0,"cache_creation_rate":0,"cache_read_rate":0.067}'::jsonb;
