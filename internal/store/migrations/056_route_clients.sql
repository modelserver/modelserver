-- 056_route_clients.sql
--
-- Add the routing client-bucket dimension to the routes table.
--
--   clients — when populated, only requests whose derived ClientBucket
--             (5 values: claude-code-cli, claude-desktop, codex-cli,
--             codex-desktop, other) is in this list match the route.
--
-- Empty array means "match any value", preserving today's behavior for
-- every existing route. Migration is therefore safe to deploy ahead of
-- the matcher upgrade — old routes simply continue to match every
-- request as they do today.
--
-- Idempotent via IF NOT EXISTS. No down step.

ALTER TABLE routes
    ADD COLUMN IF NOT EXISTS clients TEXT[] NOT NULL DEFAULT '{}';
