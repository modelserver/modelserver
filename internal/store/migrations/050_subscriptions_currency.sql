-- 050_subscriptions_currency.sql
--
-- Add `currency` to subscriptions so the currency lock survives DeliverOrder.
--
-- Background: a paid order carries `currency` ("CNY"/"USD") and an
-- `existing_subscription_id` pointing at the subscription that was active
-- when the order was placed. DeliverOrder() then REVOKES that subscription
-- and INSERTS a brand-new one — leaving the paid order pointing at the
-- now-revoked predecessor. Trying to enforce the cross-currency lock by
-- joining `orders.existing_subscription_id = active_subscription.id`
-- therefore returns no rows after the first delivery, silently unlocking
-- the project. We denormalize currency onto subscriptions so the lookup is
-- O(1) and naturally tied to lifecycle (a new active sub starts with no
-- carried currency, and ExpireAndFallbackToFree's free row gets '').
--
-- Backfill: for any existing active subscription we set currency from the
-- most recent paid/delivered order on the same project whose timestamp
-- precedes the subscription's start (the order that triggered its
-- creation). Free-tier rows and never-paid projects stay at ''.
--
-- Operators adding subscriptions outside DeliverOrder (admin manual
-- creation) should populate `currency` themselves if the project is
-- intended to be currency-locked.

ALTER TABLE subscriptions
    ADD COLUMN IF NOT EXISTS currency TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN subscriptions.currency IS
    'Currency the subscription was purchased in ("CNY"/"USD"); empty for free tier or never-paid';

-- Backfill: for each subscription, find the most recent paid/delivered
-- order on the same project whose created_at <= the subscription's
-- starts_at, and copy its currency. We use created_at < starts_at + 1s
-- to tolerate same-instant inserts from DeliverOrder's transaction.
UPDATE subscriptions s
SET currency = COALESCE(
    (SELECT o.currency
     FROM orders o
     WHERE o.project_id = s.project_id
       AND o.status IN ('paid', 'delivered')
       AND o.created_at <= s.starts_at + interval '1 second'
     ORDER BY o.created_at DESC
     LIMIT 1),
    ''
)
WHERE s.currency = '';
