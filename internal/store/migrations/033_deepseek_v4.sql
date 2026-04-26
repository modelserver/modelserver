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
