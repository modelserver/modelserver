INSERT INTO plans (name, slug, display_name, description, tier_level, price_per_period, period_months, credit_rules, model_credit_rates)
VALUES
    ('Max 40x', 'max_40x', 'Max 40x', 'Same usage limits as Claude Max (40x)', 4000, 199999, 1,
     '[{"window":"5h","window_type":"sliding","max_credits":22000000,"scope":"project"},{"window":"7d","window_type":"sliding","max_credits":166666600,"scope":"project"}]',
     '{
        "claude-opus-4-7":          {"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0},
        "claude-opus-4-6":          {"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0},
        "claude-sonnet-4-6":        {"input_rate":0.4,  "output_rate":2.0,  "cache_creation_rate":0.4,  "cache_read_rate":0},
        "claude-haiku-4-5":         {"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},
        "claude-haiku-4-5-20251001":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},
        "gpt-5.4":                  {"input_rate":0.333,"output_rate":2.0,  "cache_creation_rate":0,"cache_read_rate":0.033},
        "gpt-5.3-codex":            {"input_rate":0.233,"output_rate":1.867,"cache_creation_rate":0,"cache_read_rate":0.023},
        "gpt-5.2-codex":            {"input_rate":0.233,"output_rate":1.867,"cache_creation_rate":0,"cache_read_rate":0.023},
        "gpt-5.2":                  {"input_rate":0.233,"output_rate":1.867,"cache_creation_rate":0,"cache_read_rate":0.023},
        "gpt-5.1-codex-max":        {"input_rate":0.167,"output_rate":1.333,"cache_creation_rate":0,"cache_read_rate":0.017},
        "gpt-5.1-codex-mini":       {"input_rate":0.033,"output_rate":0.267,"cache_creation_rate":0,"cache_read_rate":0.003},
        "_default":                 {"input_rate":0.4,  "output_rate":2.0,  "cache_creation_rate":0.4,  "cache_read_rate":0}
      }')
ON CONFLICT (slug) DO NOTHING;
