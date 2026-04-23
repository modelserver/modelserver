-- 025_request_kind.sql
--
-- Records the wire-level endpoint kind that served each request (e.g.
-- anthropic_messages, openai_responses). This mirrors the request_kinds
-- column on routes (added in 021) but stored per-request for log display
-- and filtering. Empty string means the kind was unknown at insert time
-- (e.g. requests rejected by the rate-limit middleware before routing).

ALTER TABLE requests
    ADD COLUMN IF NOT EXISTS request_kind TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_requests_request_kind
    ON requests (request_kind)
    WHERE request_kind <> '';
