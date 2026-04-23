-- 022_extra_usage_bypass.sql
--
-- Adds a superadmin-managed bypass flag on extra_usage_settings. When true,
-- the guard middleware skips the enabled and balance checks; settlement
-- continues to deduct, which can drive the balance negative. The monthly
-- limit is still enforced. See
-- docs/superpowers/specs/2026-04-23-extra-usage-superadmin-bypass-design.md.
--
-- Forward-only. Once any project has been granted bypass and has driven
-- its balance negative, re-adding the dropped CHECK constraints would
-- fail. Rolling back requires first admin-adjusting all affected balances
-- back to non-negative.

ALTER TABLE extra_usage_settings
    ADD COLUMN IF NOT EXISTS bypass_balance_check BOOLEAN NOT NULL DEFAULT FALSE;

-- Allow negative balances under bypass. The CHECK constraints were
-- auto-named by Postgres during CREATE TABLE in migration 017; the default
-- names follow the pattern <table>_<column>_check.
ALTER TABLE extra_usage_settings
    DROP CONSTRAINT IF EXISTS extra_usage_settings_balance_fen_check;

ALTER TABLE extra_usage_transactions
    DROP CONSTRAINT IF EXISTS extra_usage_transactions_balance_after_fen_check;
