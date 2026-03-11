-- Change channel_id FK on requests to SET NULL on channel deletion,
-- so deleting a channel does not fail due to existing request logs.
ALTER TABLE requests ALTER COLUMN channel_id DROP NOT NULL;
ALTER TABLE requests DROP CONSTRAINT IF EXISTS requests_channel_id_fkey;
ALTER TABLE requests ADD CONSTRAINT requests_channel_id_fkey
    FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE SET NULL;
