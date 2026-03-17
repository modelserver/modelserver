-- Post-verification cleanup: drop backward-compat views and old columns.
-- Only run AFTER the new routing code is verified in production.

BEGIN;

DROP VIEW IF EXISTS channels;
DROP VIEW IF EXISTS channel_routes;

ALTER TABLE routes DROP COLUMN IF EXISTS channel_ids;
ALTER TABLE requests DROP COLUMN IF EXISTS channel_id;

COMMIT;
