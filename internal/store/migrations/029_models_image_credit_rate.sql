BEGIN;

ALTER TABLE models ADD COLUMN default_image_credit_rate JSONB;

COMMIT;
