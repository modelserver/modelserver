-- 026_drop_dial_timeout.sql
--
-- Drop upstreams.dial_timeout. The column was added in 003 but never wired
-- into the proxy executor's http.Transport — Go's default net.Dialer was
-- always used regardless of the configured value, so the field was dead
-- config. Removing it eliminates a misleading admin/UI surface.
--
-- Forward-only. If a per-upstream dial timeout is reintroduced later it
-- should also be wired into Transport.DialContext to actually take effect.

ALTER TABLE upstreams
    DROP COLUMN IF EXISTS dial_timeout;
