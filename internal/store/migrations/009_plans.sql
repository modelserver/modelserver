-- Plans table: dynamic plan management replacing hardcoded PredefinedPlans.
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

CREATE INDEX IF NOT EXISTS idx_plans_slug ON plans(slug);
CREATE INDEX IF NOT EXISTS idx_plans_group_tag ON plans(group_tag);
CREATE INDEX IF NOT EXISTS idx_plans_active ON plans(is_active) WHERE is_active = TRUE;
