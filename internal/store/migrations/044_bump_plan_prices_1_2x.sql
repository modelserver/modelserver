-- 044_bump_plan_prices_1_2x.sql
--
-- Across-the-board 1.2× bump on every priced plan. The Free tier (slug='free',
-- price_per_period=0) is left alone — multiplying by 1.2 is a no-op there
-- anyway, but excluding it makes the intent explicit and protects against
-- accidentally activating a non-zero "free" tier in the future.
--
-- Pricing is stored as an integer number of fen (1/100 CNY). Multiplying by
-- 1.2 produces .8 fractions for our existing tiers (e.g. 9999 → 11998.8); we
-- ROUND() to the nearest fen, matching the convention used elsewhere in the
-- codebase for fen-denominated arithmetic.
--
-- Already-issued orders snapshot unit_price/amount at checkout time
-- (see orders table in 001_init.sql), so this migration only affects future
-- purchases. Active subscriptions stay valid at their original purchase price.
--
-- The schema_migrations table guarantees this migration runs exactly once per
-- database, so the bump is idempotent across redeploys.

UPDATE plans
SET price_per_period = ROUND(price_per_period * 1.2),
    updated_at = NOW()
WHERE slug <> 'free';
