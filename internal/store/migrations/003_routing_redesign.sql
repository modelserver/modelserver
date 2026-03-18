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
-- 5. Data migration: convert existing routes with channel_ids into upstream_groups
-- ============================================================
-- For each existing route that has channel_ids, create an upstream_group and
-- populate its members. This preserves the existing routing behavior.
DO $$
DECLARE
    r RECORD;
    new_group_id UUID;
    cid UUID;
BEGIN
    FOR r IN SELECT id, model_pattern, channel_ids FROM routes WHERE channel_ids IS NOT NULL AND array_length(channel_ids, 1) > 0 LOOP
        -- Create an upstream_group for this route.
        INSERT INTO upstream_groups (name, lb_policy, retry_policy, status)
        VALUES ('auto-' || r.model_pattern, 'weighted_random', '{}', 'active')
        RETURNING id INTO new_group_id;

        -- Add each channel_id as a group member.
        FOREACH cid IN ARRAY r.channel_ids LOOP
            -- Only add if the upstream actually exists (skip orphaned references).
            IF EXISTS (SELECT 1 FROM upstreams WHERE id = cid) THEN
                INSERT INTO upstream_group_members (upstream_group_id, upstream_id, weight, is_backup)
                VALUES (new_group_id, cid, NULL, FALSE);
            END IF;
        END LOOP;

        -- Link the route to its new upstream_group.
        UPDATE routes SET upstream_group_id = new_group_id WHERE id = r.id;
    END LOOP;
END $$;

-- ============================================================
-- 6. Add routing observability columns to requests
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
