-- Add billing_tag to projects for plan group matching.
ALTER TABLE projects ADD COLUMN IF NOT EXISTS billing_tag TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_projects_billing_tag ON projects(billing_tag) WHERE billing_tag != '';
