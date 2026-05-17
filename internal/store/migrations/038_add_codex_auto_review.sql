-- 038_add_codex_auto_review.sql
--
-- Register codex-auto-review in the model catalog as a distinct entry and
-- seed its credit rate into every existing plan's model_credit_rates map.
--
-- Per OpenAI's Codex pricing docs (https://developers.openai.com/codex/pricing
-- and https://developers.openai.com/codex/cloud/code-review):
--   * "codex-auto-review" is the agent that runs when @Codex is tagged on a
--     GitHub PR (or when automatic review is enabled). It is metered against
--     its own "Code Reviews / 5h" quota separately from Local Messages and
--     Cloud Tasks.
--   * Auto-reviews "run on GPT-5.3-Codex" — there is no distinct per-token
--     rate published. The catalog default mirrors gpt-5.3-codex (so any
--     fallback path stays consistent with the upstream model), but the
--     per-plan rate is intentionally pinned to gpt-5.5's much lower
--     calibrated rate (~0.044/0.261/0.0044). Rationale: code review is a
--     bursty, lower-value workload for the user, and bundling it at the
--     full codex price would exhaust 5h budgets too quickly.
--
-- We model this as its own catalog row (not an alias of gpt-5.3-codex) so
-- that admins can see auto-review spend broken out in the per-model usage
-- pie chart and ratelimit windows. If OpenAI publishes a distinct rate
-- later, only this entry needs to change.
--
-- Routes and upstreams are intentionally not seeded — operators wire the
-- model into the relevant codex upstream group after deployment.

INSERT INTO models (
    name,
    display_name,
    description,
    aliases,
    default_credit_rate,
    status,
    publisher,
    metadata
)
VALUES (
    'codex-auto-review',
    'Codex Auto Review',
    'OpenAI Codex automatic PR review agent. Triggered via GitHub (e.g. @Codex on a pull request); runs on GPT-5.3-Codex.',
    '{}',
    '{"input_rate":0.233,"output_rate":1.867,"cache_creation_rate":0,"cache_read_rate":0.023}'::jsonb,
    'active',
    'openai',
    '{"category":"chat"}'::jsonb
)
ON CONFLICT (name) DO UPDATE SET
    display_name        = EXCLUDED.display_name,
    description         = EXCLUDED.description,
    publisher           = EXCLUDED.publisher,
    default_credit_rate = EXCLUDED.default_credit_rate,
    metadata            = EXCLUDED.metadata,
    status              = EXCLUDED.status,
    updated_at          = NOW();

-- Merge the rate into every plan. Uniform across all tiers (no per-tier
-- compression, matching the gpt-5.5 / gpt-5.x-codex convention). Rate is
-- pinned to gpt-5.5's calibrated subscription rate, NOT the catalog
-- default — see header for why. `||` shallow-merges, so re-running this
-- overwrites with the latest rate.
UPDATE plans
SET model_credit_rates = COALESCE(model_credit_rates, '{}'::jsonb) || '{
    "codex-auto-review": {"input_rate":0.044,"output_rate":0.261,"cache_creation_rate":0,"cache_read_rate":0.0044}
}'::jsonb,
    updated_at = NOW();
