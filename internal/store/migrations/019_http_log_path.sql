-- 019_http_log_path.sql
--
-- Add a nullable TEXT column to requests that stores the S3 object key
-- for the full HTTP request log (request + response headers and bodies).
-- NULL means HTTP logging was not enabled for this request.

ALTER TABLE requests
    ADD COLUMN IF NOT EXISTS http_log_path TEXT;
