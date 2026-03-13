CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================================
-- Users
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    nickname TEXT NOT NULL DEFAULT '',
    picture TEXT,
    is_superadmin BOOLEAN NOT NULL DEFAULT FALSE,
    max_projects INTEGER NOT NULL DEFAULT 5,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- OAuth / OIDC connections (a user may link multiple providers).
CREATE TABLE IF NOT EXISTS user_oidc_connections (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, provider, provider_id),
    UNIQUE (provider, provider_id)
);

-- ============================================================
-- Projects
-- ============================================================
CREATE TABLE IF NOT EXISTS projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    description TEXT,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    billing_tags TEXT[] NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'active',
    settings JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_projects_created_by ON projects(created_by);
CREATE INDEX IF NOT EXISTS idx_projects_billing_tags ON projects USING GIN(billing_tags);

CREATE TABLE IF NOT EXISTS project_members (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'developer',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, project_id)
);
CREATE INDEX IF NOT EXISTS idx_project_members_project ON project_members(project_id);

-- ============================================================
-- Channels
-- ============================================================
CREATE TABLE IF NOT EXISTS channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider TEXT NOT NULL,
    name TEXT NOT NULL,
    base_url TEXT NOT NULL DEFAULT 'https://api.anthropic.com',
    api_key_encrypted BYTEA,
    supported_models TEXT[] NOT NULL DEFAULT '{}',
    weight INTEGER NOT NULL DEFAULT 1,
    selection_priority INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active',
    max_concurrent INTEGER NOT NULL DEFAULT 0,
    test_model TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS channel_routes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
    model_pattern TEXT NOT NULL,
    channel_ids UUID[] NOT NULL,
    match_priority INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_channel_routes_project ON channel_routes(project_id);
CREATE INDEX IF NOT EXISTS idx_channel_routes_priority ON channel_routes(match_priority DESC);

-- ============================================================
-- Rate-limit policies
-- ============================================================
CREATE TABLE IF NOT EXISTS rate_limit_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    credit_rules JSONB,
    model_credit_rates JSONB,
    classic_rules JSONB,
    starts_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_policies_project ON rate_limit_policies(project_id);

-- ============================================================
-- API keys
-- ============================================================
CREATE TABLE IF NOT EXISTS api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    key_hash TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'active',
    allowed_models TEXT[],
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_api_keys_project ON api_keys(project_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_created_by ON api_keys(created_by);

-- ============================================================
-- Threads & traces
-- ============================================================
CREATE TABLE IF NOT EXISTS threads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_threads_project ON threads(project_id);

CREATE TABLE IF NOT EXISTS traces (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    thread_id UUID REFERENCES threads(id) ON DELETE SET NULL,
    source TEXT NOT NULL DEFAULT 'auto',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_traces_project ON traces(project_id);
CREATE INDEX IF NOT EXISTS idx_traces_thread ON traces(thread_id);
CREATE INDEX IF NOT EXISTS idx_traces_created_at ON traces(created_at);

-- ============================================================
-- Requests
-- ============================================================
CREATE TABLE IF NOT EXISTS requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
    api_key_id UUID NOT NULL REFERENCES api_keys(id) ON DELETE RESTRICT,
    channel_id UUID REFERENCES channels(id) ON DELETE SET NULL,
    trace_id UUID REFERENCES traces(id) ON DELETE SET NULL,
    msg_id TEXT,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    streaming BOOLEAN NOT NULL DEFAULT FALSE,
    status TEXT NOT NULL DEFAULT 'processing',
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    credits_consumed NUMERIC NOT NULL DEFAULT 0,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    ttft_ms BIGINT NOT NULL DEFAULT 0,
    error_message TEXT,
    request_body_ref TEXT,
    response_body_ref TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_requests_project ON requests(project_id);
CREATE INDEX IF NOT EXISTS idx_requests_api_key ON requests(api_key_id);
CREATE INDEX IF NOT EXISTS idx_requests_channel ON requests(channel_id);
CREATE INDEX IF NOT EXISTS idx_requests_trace ON requests(trace_id);
CREATE INDEX IF NOT EXISTS idx_requests_project_created ON requests(project_id, created_at);

-- ============================================================
-- Plans
-- ============================================================
CREATE TABLE IF NOT EXISTS plans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    tier_level INTEGER NOT NULL DEFAULT 0,
    group_tag TEXT NOT NULL DEFAULT '',
    price_per_period BIGINT NOT NULL DEFAULT 0,
    period_months INTEGER NOT NULL DEFAULT 1,
    credit_rules JSONB,
    model_credit_rates JSONB,
    classic_rules JSONB,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_plans_group_tag ON plans(group_tag);

-- ============================================================
-- Subscriptions
-- ============================================================
CREATE TABLE IF NOT EXISTS subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    plan_id UUID REFERENCES plans(id),
    plan_name TEXT NOT NULL,
    policy_id UUID REFERENCES rate_limit_policies(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'active',
    starts_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_subscriptions_project ON subscriptions(project_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_status ON subscriptions(status, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_one_active_per_project
    ON subscriptions(project_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_subscriptions_plan ON subscriptions(plan_id);

-- ============================================================
-- Orders
-- ============================================================
CREATE TABLE IF NOT EXISTS orders (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    plan_id UUID NOT NULL REFERENCES plans(id),
    order_type TEXT NOT NULL,
    periods INTEGER NOT NULL DEFAULT 1,
    unit_price BIGINT NOT NULL,
    amount BIGINT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'USD',
    status TEXT NOT NULL DEFAULT 'pending',
    payment_ref TEXT NOT NULL DEFAULT '',
    payment_url TEXT NOT NULL DEFAULT '',
    existing_subscription_id UUID REFERENCES subscriptions(id),
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_orders_project ON orders(project_id);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_payment_ref ON orders(payment_ref) WHERE payment_ref != '';

-- ============================================================
-- Seed plans
-- ============================================================
INSERT INTO plans (name, slug, display_name, description, tier_level, price_per_period, period_months, credit_rules, model_credit_rates)
VALUES
    ('Free', 'free', 'Free', 'Default free tier with basic rate limits', 0, 0, 1,
     '[{"window":"5h","window_type":"sliding","max_credits":55000,"scope":"project"},{"window":"7d","window_type":"sliding","max_credits":500000,"scope":"project"}]',
     '{"claude-opus-4-6":{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0},"claude-sonnet-4-6":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0},"claude-haiku-4-5":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},"claude-haiku-4-5-20251001":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},"_default":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0}}'),
    ('Pro', 'pro', 'Pro', '', 100, 4999, 1,
     '[{"window":"5h","window_type":"sliding","max_credits":550000,"scope":"project"},{"window":"7d","window_type":"sliding","max_credits":5000000,"scope":"project"}]',
     '{"claude-opus-4-6":{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0},"claude-sonnet-4-6":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0},"claude-haiku-4-5":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},"claude-haiku-4-5-20251001":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},"_default":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0}}'),
    ('Max 2x', 'max_2x', 'Max 2x', '', 200, 9999, 1,
     '[{"window":"5h","window_type":"sliding","max_credits":1100000,"scope":"project"},{"window":"7d","window_type":"sliding","max_credits":10000000,"scope":"project"}]',
     '{"claude-opus-4-6":{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0},"claude-sonnet-4-6":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0},"claude-haiku-4-5":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},"claude-haiku-4-5-20251001":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},"_default":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0}}'),
    ('Max 5x', 'max_5x', 'Max 5x', '', 500, 24999, 1,
     '[{"window":"5h","window_type":"sliding","max_credits":3300000,"scope":"project"},{"window":"7d","window_type":"sliding","max_credits":41666700,"scope":"project"}]',
     '{"claude-opus-4-6":{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0},"claude-sonnet-4-6":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0},"claude-haiku-4-5":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},"claude-haiku-4-5-20251001":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},"_default":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0}}'),
    ('Max 20x', 'max_20x', 'Max 20x', '', 2000, 49999, 1,
     '[{"window":"5h","window_type":"sliding","max_credits":11000000,"scope":"project"},{"window":"7d","window_type":"sliding","max_credits":83333300,"scope":"project"}]',
     '{"claude-opus-4-6":{"input_rate":0.667,"output_rate":3.333,"cache_creation_rate":0.667,"cache_read_rate":0},"claude-sonnet-4-6":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0},"claude-haiku-4-5":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},"claude-haiku-4-5-20251001":{"input_rate":0.133,"output_rate":0.667,"cache_creation_rate":0.133,"cache_read_rate":0},"_default":{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0}}')
ON CONFLICT (slug) DO NOTHING;
