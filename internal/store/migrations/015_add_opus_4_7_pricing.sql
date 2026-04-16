-- Add claude-opus-4-7 pricing to every plan's model_credit_rates.
--
-- Rates match claude-opus-4-6: Anthropic keeps the Opus tier at $5 input /
-- $25 output per MTok, which under the subscription formula (API_price / 7.5,
-- cache_read free) yields:
--   input_rate=0.667, output_rate=3.333, cache_creation_rate=0.667, cache_read_rate=0
--
-- The NOT ? guard makes this safe to re-run: any plan that has already set a
-- custom claude-opus-4-7 rate (e.g. via the admin UI) is left untouched.
UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{claude-opus-4-7}',
        '{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE NOT (model_credit_rates ? 'claude-opus-4-7');
