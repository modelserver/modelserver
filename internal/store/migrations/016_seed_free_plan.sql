-- Seed a free plan (tier 0) with conservative rate limits.
INSERT INTO plans (name, slug, display_name, description, tier_level, price_per_period, period_months, credit_rules, model_credit_rates)
VALUES
    ('Free', 'free', 'Free', 'Default free tier with basic rate limits', 0, 0, 1,
     '[{"window":"5h","window_type":"sliding","max_credits":55000,"scope":"project"},{"window":"1w","window_type":"calendar","max_credits":500000,"scope":"project"}]',
     '{"claude-opus-4":{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0.667},"claude-sonnet-4":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0.4},"claude-haiku-4-5":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0.133},"_default":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0.4}}')
ON CONFLICT (slug) DO NOTHING;
