-- 058_add_sonnet_5_fable_5.sql
--
-- Register claude-sonnet-5 and claude-fable-5 in the model catalog and seed
-- their subscription credit rates into every plan and rate-limit policy.
-- Follows the pattern in 042_add_opus_4_8.sql (catalog INSERT + per-plan seed)
-- and 045_add_gpt_5_4_mini_nano.sql (two models in one migration + policy seed).
--
-- Anthropic API prices (see the claude-api skill's model catalog):
--   claude-sonnet-5:  input=$3   / output=$15  (same as claude-sonnet-4-6;
--                     $2/$10 intro promo through 2026-08-31 ignored so the
--                     in-plan ratio tracks sonnet-4-6 exactly)
--   claude-fable-5:   input=$10  / output=$50, cache_creation=$12.50 (1.25 *
--                     input, 5min TTL), cache_read=$1.00 (0.1 * input)
--
-- Rate formula: credit_rate = API_price / 7.5 (project-wide convention, see
-- 001_init.sql:240 and 035_seed_catalog_default_credit_rates.sql).
--
--   claude-sonnet-5:  input=0.4,   output=2.0,   cache_creation=0.4,   cache_read=0
--                     (identical to claude-sonnet-4-6 — per request, plan
--                      pricing "学习一下 claude-sonnet-4-6 的定价比例";
--                      follows the existing Anthropic-tier convention where
--                      cache_read is free on subscription and cache_creation
--                      is billed at the input rate.)
--   claude-fable-5:   input=1.333, output=6.667, cache_creation=1.667, cache_read=0.133
--                     (default_credit_rate at API price per request; unlike
--                      opus/sonnet/haiku, cache_read is NOT zeroed — the
--                      user explicitly asked for API-price cache rates,
--                      making fable-5 the first Claude row in the catalog
--                      whose cache pricing tracks Anthropic's published rates
--                      instead of the subscription-discount convention.
--                      metadata.extra_usage_only=true routes ALL clients to
--                      the extra-usage path regardless of client kind, so
--                      this rate is what actually bills; there is no plan-
--                      side rate to seed.)
--
-- The NOT (rates ? 'name') guards make the plan/policy UPDATEs idempotent and
-- preserve any operator-set custom override between deploy and re-run.

-- 1) Catalog rows. ON CONFLICT DO UPDATE matches 042_add_opus_4_8.sql exactly
--    so re-runs refresh display metadata and default_credit_rate together.
--    Since these are brand-new models an operator override is unlikely; if
--    one is needed later, it should live in a follow-up migration next to
--    the change rationale, not depend on this UPSERT sparing it.
INSERT INTO models (name, display_name, description, aliases, default_credit_rate, status, publisher, metadata)
VALUES
    (
        'claude-sonnet-5',
        'Claude Sonnet 5',
        'Anthropic Claude Sonnet 5 — Sonnet-tier successor to 4.6 with near-Opus quality on agentic and coding work.',
        '{}',
        '{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0}'::jsonb,
        'active',
        'anthropic',
        '{"category":"chat"}'::jsonb
    ),
    (
        'claude-fable-5',
        'Claude Fable 5',
        'Anthropic Claude Fable 5 — most capable widely released Anthropic model, for the most demanding reasoning and long-horizon agentic work.',
        '{}',
        '{"input_rate":1.333,"output_rate":6.667,"cache_creation_rate":1.667,"cache_read_rate":0.133}'::jsonb,
        'active',
        'anthropic',
        -- extra_usage_only=true routes ALL clients (including Claude Code /
        -- Claude Desktop) through the extra-usage path — see
        -- SubscriptionEligibilityMiddleware. fable-5 is priced above any
        -- current subscription bundle, so plan-side consumption would let
        -- subscribers silently exceed their plan price. Enforced by
        -- metadata, not a hard-coded list, so ops can flip it via the admin
        -- UI's metadata editor if pricing/policy changes.
        '{"category":"chat","extra_usage_only":true}'::jsonb
    )
ON CONFLICT (name) DO UPDATE SET
    display_name        = EXCLUDED.display_name,
    description         = EXCLUDED.description,
    publisher           = EXCLUDED.publisher,
    default_credit_rate = EXCLUDED.default_credit_rate,
    metadata            = EXCLUDED.metadata,
    status              = EXCLUDED.status,
    updated_at          = NOW();

-- 2) Seed claude-sonnet-5 into every plan that doesn't already define it.
UPDATE plans
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{claude-sonnet-5}',
        '{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE NOT (model_credit_rates ? 'claude-sonnet-5');

-- 3) Same sonnet-5 seed against rate_limit_policies so per-policy overrides
--    pick it up too.
UPDATE rate_limit_policies
SET model_credit_rates = jsonb_set(
        model_credit_rates,
        '{claude-sonnet-5}',
        '{"input_rate":0.4,"output_rate":2.0,"cache_creation_rate":0.4,"cache_read_rate":0}'::jsonb,
        true
    ),
    updated_at = NOW()
WHERE NOT (model_credit_rates ? 'claude-sonnet-5');

-- claude-fable-5 is intentionally NOT seeded into plans or rate_limit_policies:
-- metadata.extra_usage_only=true forces every request onto the extra-usage
-- path, which prices from catalog.default_credit_rate (set above) via
-- computeExtraUsageCostCredits. A plan-side rate would be a dead entry that
-- misleads dashboard readers into thinking subscribers can consume it.
