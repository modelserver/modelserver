CREATE TABLE device_codes (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_code        TEXT NOT NULL UNIQUE,
    user_code          TEXT NOT NULL UNIQUE,
    client_id          TEXT NOT NULL DEFAULT '',
    scopes             TEXT[] NOT NULL DEFAULT '{}',
    status             TEXT NOT NULL DEFAULT 'pending',
    verification_nonce TEXT NOT NULL UNIQUE,
    access_token       BYTEA,
    refresh_token      BYTEA,
    token_type         TEXT NOT NULL DEFAULT '',
    token_expires_in   INT NOT NULL DEFAULT 0,
    expires_at         TIMESTAMPTZ NOT NULL,
    poll_interval      INT NOT NULL DEFAULT 5,
    last_polled_at     TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_device_codes_user_code ON device_codes(user_code) WHERE status = 'pending';
CREATE INDEX idx_device_codes_device_code ON device_codes(device_code);
