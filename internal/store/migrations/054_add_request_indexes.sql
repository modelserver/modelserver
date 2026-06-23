-- 054_add_request_indexes.sql
--
-- Single-column index on requests.model to back the dashboard's per-project
-- model filter query (WHERE project_id = $1 AND model = $2). request_kind
-- already has a partial index from 025_request_kind.sql
-- (idx_requests_request_kind WHERE request_kind <> ''), so no new index
-- is needed for that column.
--
-- The migration runner executes each migration inside a transaction
-- (store.go), which forbids CREATE INDEX CONCURRENTLY — same constraint
-- every prior index migration on this table (001/003/025) ran under. The
-- non-concurrent CREATE INDEX briefly locks writes on requests; operators
-- with very large requests tables should apply during a low-write window.
--
-- IF NOT EXISTS makes the migration safe to re-apply by hand should the
-- transactional record in schema_migrations and the on-disk index ever
-- get out of sync (e.g. after a manual partial rollback).

CREATE INDEX IF NOT EXISTS idx_requests_model ON requests(model);
