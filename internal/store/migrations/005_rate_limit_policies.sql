CREATE TABLE IF NOT EXISTS rate_limit_policies (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    credit_rules JSONB,
    model_credit_rates JSONB,
    classic_rules JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_policies_project ON rate_limit_policies(project_id);

ALTER TABLE api_keys ADD CONSTRAINT fk_api_keys_policy
    FOREIGN KEY (rate_limit_policy_id) REFERENCES rate_limit_policies(id) ON DELETE SET NULL;
