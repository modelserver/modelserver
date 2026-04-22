-- 021_route_request_kinds.sql
-- Adds the request_kind dimension to routes. Backfill is provider-inferred
-- per upstream group so existing traffic continues to land on the same
-- upstreams without operator intervention. Cross-family shared groups
-- (e.g. an Anthropic + OpenAI mix in one group) get a placeholder
-- ['anthropic_messages'] and emit a NOTICE — operators must split those
-- groups and assign explicit kinds.

BEGIN;

ALTER TABLE routes
    ADD COLUMN request_kinds TEXT[] NOT NULL DEFAULT '{}';

-- Per-group provider inference. Aggregates the distinct providers behind
-- each group, classifies the family, and writes the resulting kinds onto
-- every route that points at the group.
WITH group_providers AS (
    SELECT
        m.upstream_group_id AS gid,
        array_agg(DISTINCT u.provider) AS providers
    FROM upstream_group_members m
    JOIN upstreams u ON u.id = m.upstream_id
    GROUP BY m.upstream_group_id
),
group_kinds AS (
    SELECT
        gid,
        CASE
            -- Pure Anthropic-family (anthropic + claudecode)
            WHEN providers <@ ARRAY['anthropic','claudecode']::TEXT[]
                THEN ARRAY['anthropic_messages','anthropic_count_tokens']
            -- Anthropic-side, includes bedrock or vertex-anthropic (no count_tokens)
            WHEN providers <@ ARRAY['anthropic','claudecode','bedrock','vertex-anthropic']::TEXT[]
                THEN ARRAY['anthropic_messages']
            -- Pure OpenAI native
            WHEN providers = ARRAY['openai']::TEXT[]
                THEN ARRAY['openai_responses']
            -- Pure Vertex OpenAI-compat
            WHEN providers = ARRAY['vertex-openai']::TEXT[]
                THEN ARRAY['openai_chat_completions']
            -- OpenAI-side mixed: same family but covers both wire variants;
            -- assign both kinds so neither endpoint silently 404s.
            WHEN providers <@ ARRAY['openai','vertex-openai']::TEXT[]
                THEN ARRAY['openai_responses','openai_chat_completions']
            -- Google family
            WHEN providers <@ ARRAY['gemini','vertex-google']::TEXT[]
                THEN ARRAY['google_generate_content']
            -- Mixed / unrecognised — placeholder, audited below.
            ELSE ARRAY['anthropic_messages']
        END AS inferred_kinds
    FROM group_providers
)
UPDATE routes r
SET request_kinds = gk.inferred_kinds
FROM group_kinds gk
WHERE r.upstream_group_id = gk.gid;

-- Routes whose upstream_group has no members (or null group) — wouldn't
-- route anywhere usable today; fall back to the safe Anthropic default
-- so the NOT NULL constraint is satisfied.
UPDATE routes
SET request_kinds = ARRAY['anthropic_messages']
WHERE request_kinds = '{}';

ALTER TABLE routes
    ADD CONSTRAINT routes_request_kinds_valid CHECK (
        request_kinds <@ ARRAY[
            'anthropic_messages',
            'anthropic_count_tokens',
            'openai_chat_completions',
            'openai_responses',
            'google_generate_content'
        ]::TEXT[]
        AND array_length(request_kinds, 1) >= 1
    );

CREATE INDEX idx_routes_request_kinds ON routes USING GIN (request_kinds);

-- Audit: routes whose upstream group spans multiple provider families.
-- Today these "shared groups" worked because AllowedProviders filtered
-- per-ingress at request time; after this change the route's request_kinds
-- is a placeholder and the group should be split. Output is informational
-- only — operators see it in the migration log.
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT
            rt.id AS route_id,
            rt.model_names,
            rt.upstream_group_id,
            array_agg(DISTINCT u.provider) AS providers
        FROM routes rt
        JOIN upstream_group_members m ON m.upstream_group_id = rt.upstream_group_id
        JOIN upstreams u ON u.id = m.upstream_id
        GROUP BY rt.id, rt.model_names, rt.upstream_group_id
        HAVING
            (
                bool_or(u.provider IN ('anthropic','claudecode','bedrock','vertex-anthropic'))
                AND bool_or(u.provider IN ('openai','vertex-openai'))
            )
            OR (
                bool_or(u.provider IN ('anthropic','claudecode','bedrock','vertex-anthropic'))
                AND bool_or(u.provider IN ('gemini','vertex-google'))
            )
            OR (
                bool_or(u.provider IN ('openai','vertex-openai'))
                AND bool_or(u.provider IN ('gemini','vertex-google'))
            )
    LOOP
        RAISE NOTICE 'Route % (models=%, group=%) has cross-family providers % — split the upstream group and assign request_kinds explicitly',
            r.route_id, r.model_names, r.upstream_group_id, r.providers;
    END LOOP;
END $$;

-- Audit: Anthropic-side groups that include bedrock or vertex-anthropic
-- alongside anthropic/claudecode. These were silently downgraded to
-- ['anthropic_messages'] because bedrock/vertex-anthropic don't natively
-- support /v1/messages/count_tokens — sending count_tokens there would
-- 4xx. Operators with IDE-driven count_tokens traffic should add a
-- separate route on a claudecode-only (or anthropic-only) group with
-- request_kinds=['anthropic_count_tokens'] before clients hit 404s.
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT
            rt.id AS route_id,
            rt.model_names,
            rt.upstream_group_id,
            array_agg(DISTINCT u.provider) AS providers
        FROM routes rt
        JOIN upstream_group_members m ON m.upstream_group_id = rt.upstream_group_id
        JOIN upstreams u ON u.id = m.upstream_id
        GROUP BY rt.id, rt.model_names, rt.upstream_group_id
        HAVING
            bool_or(u.provider IN ('anthropic','claudecode'))
            AND bool_or(u.provider IN ('bedrock','vertex-anthropic'))
    LOOP
        RAISE NOTICE 'Route % (models=%, group=%) has Anthropic-family mix % — count_tokens dropped from request_kinds (bedrock/vertex-anthropic do not support it). Add a separate count_tokens route on a claudecode-only group if needed.',
            r.route_id, r.model_names, r.upstream_group_id, r.providers;
    END LOOP;
END $$;

COMMIT;
