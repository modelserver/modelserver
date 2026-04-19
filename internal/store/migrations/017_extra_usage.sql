-- 017_extra_usage.sql
--
-- Introduce the extra_usage subsystem: per-project settings with opt-in
-- enable + balance, an immutable ledger of deductions/top-ups/adjustments,
-- extensions to requests for extra-usage attribution, a publisher column
-- on models for subscription eligibility decisions, and order_type support
-- for top-up orders.

-- ---------------------------------------------------------------------------
-- 1. Per-project settings. One row per project; balance tracked in CNY fen.
--    `enabled=false` by default — users opt in from the dashboard.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS extra_usage_settings (
    project_id        UUID        PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    enabled           BOOLEAN     NOT NULL DEFAULT FALSE,
    balance_fen       BIGINT      NOT NULL DEFAULT 0 CHECK (balance_fen >= 0),
    monthly_limit_fen BIGINT      NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ---------------------------------------------------------------------------
-- 2. Immutable ledger. Every top-up / deduction / refund / adjust is one row.
--    `amount_fen` sign convention: positive = credit (top-up/refund),
--    negative = debit (deduction). `balance_after_fen` snapshots the balance
--    immediately after this row was applied, providing a redundant audit trail
--    that lets us verify the settings balance without replaying the whole log.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS extra_usage_transactions (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id        UUID        NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type              TEXT        NOT NULL
                      CHECK (type IN ('topup','deduction','refund','adjust')),
    amount_fen        BIGINT      NOT NULL,
    balance_after_fen BIGINT      NOT NULL CHECK (balance_after_fen >= 0),
    request_id        UUID        NULL REFERENCES requests(id) ON DELETE SET NULL,
    order_id          UUID        NULL REFERENCES orders(id)   ON DELETE SET NULL,
    reason            TEXT        NOT NULL DEFAULT '',
    description       TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_eut_project_created
    ON extra_usage_transactions (project_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_eut_project_deduction
    ON extra_usage_transactions (project_id, type, created_at)
    WHERE type = 'deduction';

-- Idempotency: a single top-up order can only write one ledger row. Partial
-- index skips rows where order_id is NULL (all non-top-up rows).
CREATE UNIQUE INDEX IF NOT EXISTS uniq_eut_topup_order
    ON extra_usage_transactions (order_id)
    WHERE type = 'topup' AND order_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 3. Extend `requests` to record whether a successful request was settled via
--    extra usage, its cost, and the reason. Nullable reason stored as '' so
--    existing rows (pre-migration) remain valid with no data rewrite.
-- ---------------------------------------------------------------------------
ALTER TABLE requests
    ADD COLUMN IF NOT EXISTS is_extra_usage       BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS extra_usage_cost_fen BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS extra_usage_reason   TEXT    NOT NULL DEFAULT '';

-- ---------------------------------------------------------------------------
-- 4. Publisher column on models. Business decision field (required, controlled
--    vocabulary) separate from metadata.provider_hint (UI soft hint). The
--    backfill covers existing canonical families; admin UI enforces non-empty
--    on subsequent writes. Runtime treats '' as "allow subscription" + metric.
-- ---------------------------------------------------------------------------
ALTER TABLE models ADD COLUMN IF NOT EXISTS publisher TEXT NOT NULL DEFAULT '';

UPDATE models SET publisher = 'anthropic'
    WHERE publisher = '' AND name LIKE 'claude-%';
UPDATE models SET publisher = 'openai'
    WHERE publisher = '' AND name ~ '^(gpt-|o[0-9]|chatgpt-|text-)';
UPDATE models SET publisher = 'google'
    WHERE publisher = '' AND name LIKE 'gemini-%';

-- ---------------------------------------------------------------------------
-- 5. Extend `orders` with `order_type` so the webhook handler can branch
--    between subscription delivery and extra-usage top-up. Existing rows keep
--    their implicit 'subscription' type; plan_id becomes nullable because
--    top-up orders have no plan association.
-- ---------------------------------------------------------------------------
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS order_type TEXT NOT NULL DEFAULT 'subscription'
        CHECK (order_type IN ('subscription','extra_usage_topup')),
    ADD COLUMN IF NOT EXISTS extra_usage_amount_fen BIGINT NOT NULL DEFAULT 0;

ALTER TABLE orders ALTER COLUMN plan_id DROP NOT NULL;
