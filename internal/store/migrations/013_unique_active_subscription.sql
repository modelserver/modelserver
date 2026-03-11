-- Ensure at most one active subscription per project.
CREATE UNIQUE INDEX IF NOT EXISTS idx_subscriptions_one_active_per_project
    ON subscriptions(project_id) WHERE status = 'active';
