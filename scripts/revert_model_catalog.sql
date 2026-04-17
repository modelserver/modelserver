-- revert_model_catalog.sql
--
-- Emergency rollback for migration 016_model_catalog.sql.
--
-- IMPORTANT: this file is NOT in internal/store/migrations/ because the
-- runtime migrator runs every .sql there at startup. To execute, run it
-- manually with psql against the production database, then redeploy a
-- pre-016 binary build.
--
-- Best-effort restoration: routes.model_pattern is repopulated from the
-- canonical names in routes.model_names as a `|`-joined string (NOT a
-- valid filepath glob). Operators must hand-edit each route's pattern
-- afterwards or the pre-016 router will 404 every request.
--
-- Steps performed:
--   1. Drop all triggers and trigger functions installed by 016.
--   2. Restore routes.model_pattern from model_names (best effort).
--   3. Drop routes.model_names.
--   4. Drop the models table.
--   5. Mark migration 016 as un-applied so the runtime won't think it ran.

BEGIN;

DROP TRIGGER IF EXISTS trg_models_names_unique ON models;
DROP TRIGGER IF EXISTS trg_upstream_models ON upstreams;
DROP TRIGGER IF EXISTS trg_route_models ON routes;
DROP TRIGGER IF EXISTS trg_api_key_models ON api_keys;
DROP TRIGGER IF EXISTS trg_models_delete_guard ON models;

DROP FUNCTION IF EXISTS assert_model_names_unique();
DROP FUNCTION IF EXISTS assert_model_names_in_catalog(TEXT[]);
DROP FUNCTION IF EXISTS trg_assert_upstream_models();
DROP FUNCTION IF EXISTS trg_assert_route_models();
DROP FUNCTION IF EXISTS trg_assert_api_key_models();
DROP FUNCTION IF EXISTS trg_block_referenced_model_delete();

ALTER TABLE routes ADD COLUMN IF NOT EXISTS model_pattern TEXT NOT NULL DEFAULT '';
UPDATE routes
   SET model_pattern = COALESCE(array_to_string(model_names, '|'), '')
 WHERE model_pattern = '';
ALTER TABLE routes DROP COLUMN IF EXISTS model_names;

DROP TABLE IF EXISTS models;

DELETE FROM schema_migrations WHERE name = '016_model_catalog.sql';

COMMIT;

\echo 'Rollback complete. Pre-016 binary may now be redeployed.'
\echo 'Action required: rewrite each routes.model_pattern to a valid filepath glob.'
