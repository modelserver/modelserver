-- 047_gpt_5_x_plan_rebase.sql
--
-- Re-anchor every gpt-5.x subscription rate to catalog * 0.1, and at the
-- same time backfill the 6 NULL catalog rows for legacy gpt-5.1 / gpt-5.2
-- variants. After this migration, every gpt-5.x plan entry can be
-- re-derived from its catalog row by multiplying by 0.1 — replacing the
-- mix of four previous calibration eras (0.066, 1.0, 0.132, gpt-5.5-pin).
--
-- See docs/superpowers/specs/2026-06-17-gpt-plan-rebase-design.md for
-- full rationale, burn-rate impact analysis, and risks.
--
-- ---------------------------------------------------------------------
-- Part 1: Catalog backfill (only updates NULL rows)
-- ---------------------------------------------------------------------
-- 6 legacy gpt models lost their default_credit_rate when first added
-- (migration 035 deliberately skipped them). Rate = official OpenAI API
-- price (per 1M tokens) / 7.5 (project-wide conversion).
--
--   gpt-5.1, gpt-5.1-codex, gpt-5.1-codex-max:  $1.25 / $10  / $0.125
--   gpt-5.1-codex-mini:                          $0.25 / $2   / $0.025
--   gpt-5.2, gpt-5.2-codex:                      $1.75 / $14  / $0.175
--
-- IS NULL guard: re-runs are safe and any operator-set rate wins, same
-- convention as 035.
--
-- Note: gpt-5.1 / gpt-5.1-codex base prices are not separately published
-- by OpenAI; we use the same numbers as gpt-5.1-codex-max ($1.25/$10/$0.125)
-- as a documented "best guess". Easy to override with a follow-up
-- migration if the published price differs.

UPDATE models
SET default_credit_rate = '{"input_rate":0.167,"output_rate":1.333,"cache_creation_rate":0,"cache_read_rate":0.017}'::jsonb,
    updated_at = NOW()
WHERE name IN ('gpt-5.1', 'gpt-5.1-codex', 'gpt-5.1-codex-max')
  AND default_credit_rate IS NULL;

UPDATE models
SET default_credit_rate = '{"input_rate":0.033,"output_rate":0.267,"cache_creation_rate":0,"cache_read_rate":0.003}'::jsonb,
    updated_at = NOW()
WHERE name = 'gpt-5.1-codex-mini'
  AND default_credit_rate IS NULL;

UPDATE models
SET default_credit_rate = '{"input_rate":0.233,"output_rate":1.867,"cache_creation_rate":0,"cache_read_rate":0.023}'::jsonb,
    updated_at = NOW()
WHERE name IN ('gpt-5.2', 'gpt-5.2-codex')
  AND default_credit_rate IS NULL;

-- ---------------------------------------------------------------------
-- Part 2: Plan rebase (unguarded — this is authoritative)
-- ---------------------------------------------------------------------
-- Shallow-merge the 13 entries into every plan's model_credit_rates.
-- The || operator overwrites any existing entries with these names but
-- leaves other model entries (claude-*, deepseek-*, glm-5.2 from 046,
-- _default) intact.
--
-- This is NOT idempotent in the "preserve operator overrides" sense —
-- re-running re-overwrites the 13 entries with the migration's values.
-- That's intentional; operators who want to keep a custom override must
-- apply it AFTER this migration runs, not before.

UPDATE plans
SET model_credit_rates = COALESCE(model_credit_rates, '{}'::jsonb) || '{
    "gpt-5.5":            {"input_rate":0.0667,"output_rate":0.4,"cache_creation_rate":0,"cache_read_rate":0.0067},
    "gpt-5.4":            {"input_rate":0.0333,"output_rate":0.2,"cache_creation_rate":0,"cache_read_rate":0.0033},
    "gpt-5.4-mini":       {"input_rate":0.0033,"output_rate":0.0267,"cache_creation_rate":0,"cache_read_rate":0.0003,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}},
    "gpt-5.4-nano":       {"input_rate":0.0007,"output_rate":0.0053,"cache_creation_rate":0,"cache_read_rate":0.0001,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}},
    "gpt-5.3-codex":      {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.2":            {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.2-codex":      {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.1":            {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex":      {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex-max":  {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex-mini": {"input_rate":0.0033,"output_rate":0.0267,"cache_creation_rate":0,"cache_read_rate":0.0003},
    "codex-auto-review":  {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023}
}'::jsonb,
    updated_at = NOW();

-- ---------------------------------------------------------------------
-- Part 3: Same against rate_limit_policies
-- ---------------------------------------------------------------------

UPDATE rate_limit_policies
SET model_credit_rates = COALESCE(model_credit_rates, '{}'::jsonb) || '{
    "gpt-5.5":            {"input_rate":0.0667,"output_rate":0.4,"cache_creation_rate":0,"cache_read_rate":0.0067},
    "gpt-5.4":            {"input_rate":0.0333,"output_rate":0.2,"cache_creation_rate":0,"cache_read_rate":0.0033},
    "gpt-5.4-mini":       {"input_rate":0.0033,"output_rate":0.0267,"cache_creation_rate":0,"cache_read_rate":0.0003,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}},
    "gpt-5.4-nano":       {"input_rate":0.0007,"output_rate":0.0053,"cache_creation_rate":0,"cache_read_rate":0.0001,"long_context":{"threshold_input_tokens":272000,"input_multiplier":2.0,"output_multiplier":1.5}},
    "gpt-5.3-codex":      {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.2":            {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.2-codex":      {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023},
    "gpt-5.1":            {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex":      {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex-max":  {"input_rate":0.0167,"output_rate":0.1333,"cache_creation_rate":0,"cache_read_rate":0.0017},
    "gpt-5.1-codex-mini": {"input_rate":0.0033,"output_rate":0.0267,"cache_creation_rate":0,"cache_read_rate":0.0003},
    "codex-auto-review":  {"input_rate":0.0233,"output_rate":0.1867,"cache_creation_rate":0,"cache_read_rate":0.0023}
}'::jsonb,
    updated_at = NOW();
