-- Seed predefined plans into the database. Idempotent via ON CONFLICT.
INSERT INTO plans (name, slug, display_name, tier_level, price_per_period, period_months, credit_rules, model_credit_rates)
VALUES
    ('Pro', 'pro', 'Pro', 100, 2000, 1,
     '[{"window":"5h","window_type":"sliding","max_credits":550000,"scope":"project"},{"window":"1w","window_type":"calendar","max_credits":5000000,"scope":"project"}]',
     '{"claude-opus-4":{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0.667},"claude-sonnet-4":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0.4},"claude-haiku-4-5":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0.133},"_default":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0.4}}'),
    ('Max 5x', 'max_5x', 'Max 5x', 200, 10000, 1,
     '[{"window":"5h","window_type":"sliding","max_credits":3300000,"scope":"project"},{"window":"1w","window_type":"calendar","max_credits":41666700,"scope":"project"}]',
     '{"claude-opus-4":{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0.667},"claude-sonnet-4":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0.4},"claude-haiku-4-5":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0.133},"_default":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0.4}}'),
    ('Max 20x', 'max_20x', 'Max 20x', 300, 20000, 1,
     '[{"window":"5h","window_type":"sliding","max_credits":11000000,"scope":"project"},{"window":"1w","window_type":"calendar","max_credits":83333300,"scope":"project"}]',
     '{"claude-opus-4":{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0.667},"claude-sonnet-4":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0.4},"claude-haiku-4-5":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0.133},"_default":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0.4}}')
ON CONFLICT (slug) DO NOTHING;
