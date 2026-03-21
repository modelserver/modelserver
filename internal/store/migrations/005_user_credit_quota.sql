-- 005_user_credit_quota.sql

-- Per-user credit quota on project_members
ALTER TABLE project_members
  ADD COLUMN credit_quota_percent DOUBLE PRECISION
    CHECK (credit_quota_percent >= 0 AND credit_quota_percent <= 100);

-- Denormalize API key owner into requests for fast per-user credit queries
ALTER TABLE requests ADD COLUMN created_by TEXT;

-- Index for per-user credit sum queries (hot path)
CREATE INDEX idx_requests_project_user_created
  ON requests(project_id, created_by, created_at)
  WHERE created_by IS NOT NULL;
