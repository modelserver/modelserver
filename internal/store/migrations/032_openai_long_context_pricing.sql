-- 032_openai_long_context_pricing.sql
--
-- OpenAI charges some GPT models at a higher rate when the request context
-- exceeds 272K input tokens. The uplift applies to the whole request:
-- input/cache input = 2x, output = 1.5x.
--
-- Keep this in the existing JSONB rate payload so no table rewrite is needed.

WITH rates(name, display_name, description, default_credit_rate) AS (
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
FROM rates
ON CONFLICT (name) DO UPDATE
SET default_credit_rate = EXCLUDED.default_credit_rate,
    publisher = CASE WHEN models.publisher = '' THEN 'openai' ELSE models.publisher END,
    updated_at = NOW();

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
        '{gpt-5.5,long_context}',
        '{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates ? 'gpt-5.5';

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
        '{gpt-5.5,long_context}',
        '{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE model_credit_rates ? 'gpt-5.5';

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
