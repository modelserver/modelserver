CREATE TABLE IF NOT EXISTS payments (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id          TEXT NOT NULL,
    channel           TEXT NOT NULL,
    trade_no          TEXT NOT NULL DEFAULT '',
    payment_url       TEXT NOT NULL DEFAULT '',
    amount            BIGINT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending',
    callback_status   TEXT NOT NULL DEFAULT 'pending',
    callback_retries  INT NOT NULL DEFAULT 0,
    raw_notify        JSONB,
    paid_at           TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_order_id ON payments(order_id);
