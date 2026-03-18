-- Routing system redesign: channels → upstreams, introduce upstream_groups.
-- This migration is designed to be atomic (transactional DDL in PostgreSQL).

BEGIN;

-- ============================================================
-- 1. Rename channels → upstreams
-- ============================================================
ALTER TABLE channels RENAME TO upstreams;
ALTER TABLE upstreams DROP COLUMN IF EXISTS selection_priority;
-- health_check stores HealthCheckConfig as JSONB. Duration fields use nanosecond integers
-- to match Go's time.Duration JSON serialization (e.g. 30s = 30000000000).
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS health_check JSONB NOT NULL DEFAULT '{"enabled": true, "interval": 30000000000, "timeout": 5000000000}';
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS dial_timeout INTERVAL;
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS read_timeout INTERVAL;

-- Backward-compat view (SELECT only; old code can still read "channels").
CREATE VIEW channels AS SELECT *, 0 AS selection_priority FROM upstreams;

-- ============================================================
-- 2. Create upstream_groups table
-- ============================================================
CREATE TABLE upstream_groups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    lb_policy TEXT NOT NULL DEFAULT 'weighted_random',
    retry_policy JSONB NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================================
-- 3. Create upstream_group_members join table
-- ============================================================
CREATE TABLE upstream_group_members (
    upstream_group_id UUID NOT NULL REFERENCES upstream_groups(id) ON DELETE CASCADE,
    upstream_id UUID NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    weight INTEGER,
    is_backup BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (upstream_group_id, upstream_id)
);
CREATE INDEX idx_ugm_upstream ON upstream_group_members(upstream_id);

-- ============================================================
-- 4. Rename channel_routes → routes, add upstream_group_id + conditions
-- ============================================================
ALTER TABLE channel_routes RENAME TO routes;
ALTER TABLE routes ADD COLUMN upstream_group_id UUID REFERENCES upstream_groups(id) ON DELETE SET NULL;
ALTER TABLE routes ADD COLUMN conditions JSONB NOT NULL DEFAULT '{}';

-- Backward-compat view (SELECT only; old code can still read "channel_routes").
CREATE VIEW channel_routes AS SELECT * FROM routes;

-- ============================================================
-- 5. Add routing observability columns to requests
-- ============================================================
ALTER TABLE requests ADD COLUMN upstream_id UUID;
-- Backfill upstream_id from existing channel_id.
UPDATE requests SET upstream_id = channel_id;

ALTER TABLE requests ADD COLUMN route_id UUID;
ALTER TABLE requests ADD COLUMN upstream_group_id UUID;
ALTER TABLE requests ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1;
ALTER TABLE requests ADD COLUMN retry_reason TEXT;
ALTER TABLE requests ADD COLUMN selection_ms DOUBLE PRECISION;

-- Update foreign key index name to match new terminology.
CREATE INDEX IF NOT EXISTS idx_requests_upstream ON requests(upstream_id);

COMMIT;
