-- internal/store/migrations/049_plans_multi_currency.sql
--
-- Rename price_per_period → price_cny_fen so the unit is explicit, and add
-- price_usd_cents for the Stripe channel. Backfill USD prices from business
-- anchors (no FX conversion):
--   pro=$20, max_5x=$100, max_20x=$200
--   other max_Nx: N/20 * $200 (= N * $10)
--   max_2x = 2 * pro = $40
-- free stays at 0. Unknown slugs stay at 0; admins must populate USD prices
-- for them before they can be sold via Stripe (ChannelPricing returns
-- ok=false on a zero price).
--
-- After this migration ships, any future *_add_max_*x_plan.sql or seed-data
-- insert must populate BOTH price_cny_fen and price_usd_cents directly.

ALTER TABLE plans RENAME COLUMN price_per_period TO price_cny_fen;

ALTER TABLE plans
    ADD COLUMN IF NOT EXISTS price_usd_cents BIGINT NOT NULL DEFAULT 0;

UPDATE plans SET price_usd_cents = CASE slug
    WHEN 'pro'      THEN   2000
    WHEN 'max_2x'   THEN   4000
    WHEN 'max_5x'   THEN  10000
    WHEN 'max_20x'  THEN  20000
    WHEN 'max_40x'  THEN  40000
    WHEN 'max_60x'  THEN  60000
    WHEN 'max_80x'  THEN  80000
    WHEN 'max_100x' THEN 100000
    WHEN 'max_120x' THEN 120000
    WHEN 'max_200x' THEN 200000
    WHEN 'max_240x' THEN 240000
    ELSE price_usd_cents
END,
updated_at = NOW();

COMMENT ON COLUMN plans.price_cny_fen   IS 'CNY price in fen for wechat/alipay channels';
COMMENT ON COLUMN plans.price_usd_cents IS 'USD price in cents for stripe channel';
