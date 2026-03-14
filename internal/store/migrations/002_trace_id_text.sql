-- Change traces.id and requests.trace_id from UUID to TEXT to support
-- non-UUID trace IDs from clients like OpenCode (e.g. "ses_...").

-- 1. Drop foreign keys referencing traces.id.
ALTER TABLE requests DROP CONSTRAINT IF EXISTS requests_trace_id_fkey;

-- 2. Convert traces.id from UUID to TEXT.
ALTER TABLE traces ALTER COLUMN id SET DEFAULT NULL;
ALTER TABLE traces ALTER COLUMN id TYPE TEXT USING id::text;
ALTER TABLE traces ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;

-- 3. Convert requests.trace_id from UUID to TEXT.
ALTER TABLE requests ALTER COLUMN trace_id TYPE TEXT USING trace_id::text;

-- 4. Re-add the foreign key (now both sides are TEXT).
ALTER TABLE requests
    ADD CONSTRAINT requests_trace_id_fkey
    FOREIGN KEY (trace_id) REFERENCES traces(id) ON DELETE SET NULL;
