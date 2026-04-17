-- 016_model_catalog.sql
--
-- Introduce a global `models` table as the authoritative catalog of model
-- names. Every name referenced from upstreams, routes, api_keys, plans,
-- and rate_limit_policies must be a registered canonical name after this
-- migration runs. `routes.model_pattern` (glob) is replaced by
-- `routes.model_names` (exact match against canonical names).

-- ---------------------------------------------------------------------------
-- 0. Normalize legacy data to lowercase BEFORE seeding the catalog.
--    The catalog stores canonical names lowercase and the runtime lowercases
--    incoming model names before lookup. Existing array columns may carry
--    mixed-case strings from earlier admin writes; if we don't normalize
--    them, the writer-side triggers installed in step 4 will reject any
--    later UPDATE on those rows. JSONB-key columns (plan/policy rate maps)
--    are not normalized here: the spec carries no JSONB-key trigger and the
--    runtime billing path tolerates a miss by falling back through the
--    catalog default to plan "_default".
-- ---------------------------------------------------------------------------
UPDATE upstreams
   SET supported_models = COALESCE(
       ARRAY(SELECT lower(unnest(supported_models))),
       '{}'
   )
 WHERE supported_models IS NOT NULL;

UPDATE api_keys
   SET allowed_models = COALESCE(
       ARRAY(SELECT lower(unnest(allowed_models))),
       NULL
   )
 WHERE allowed_models IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 1. Catalog table + uniqueness trigger.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS models (
    name                  TEXT PRIMARY KEY,
    display_name          TEXT NOT NULL,
    description           TEXT,
    aliases               TEXT[] NOT NULL DEFAULT '{}',
    default_credit_rate   JSONB,
    status                TEXT NOT NULL DEFAULT 'active',
    metadata              JSONB NOT NULL DEFAULT '{}',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE OR REPLACE FUNCTION assert_model_names_unique() RETURNS TRIGGER AS $$
DECLARE
    conflict TEXT;
    alias_count INT;
    distinct_count INT;
BEGIN
    -- Row-internal: aliases must not contain duplicates nor the canonical name.
    SELECT array_length(NEW.aliases, 1), count(DISTINCT a)
        INTO alias_count, distinct_count
        FROM unnest(NEW.aliases) AS a;
    IF alias_count IS NOT NULL AND alias_count <> distinct_count THEN
        RAISE EXCEPTION 'duplicate alias within aliases array';
    END IF;
    IF NEW.name = ANY(NEW.aliases) THEN
        RAISE EXCEPTION 'alias cannot equal canonical name (%)', NEW.name;
    END IF;

    -- Cross-row: name or any alias must not collide with any other row.
    SELECT n INTO conflict
    FROM (SELECT NEW.name AS n UNION SELECT unnest(NEW.aliases) AS n) candidates
    WHERE EXISTS (
        SELECT 1 FROM models m
        WHERE m.name <> NEW.name
          AND (m.name = candidates.n OR candidates.n = ANY(m.aliases))
    )
    LIMIT 1;
    IF conflict IS NOT NULL THEN
        RAISE EXCEPTION 'model name/alias conflict: %', conflict;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_models_names_unique ON models;
CREATE TRIGGER trg_models_names_unique
    BEFORE INSERT OR UPDATE ON models
    FOR EACH ROW EXECUTE FUNCTION assert_model_names_unique();

-- ---------------------------------------------------------------------------
-- 2. Seed the catalog from existing references. Names are lowercased here
--    too so that future enforcement of the "canonical names are lowercase"
--    invariant doesn't reject existing rows. `_default` is a rate-map
--    sentinel, not a model, so it is excluded.
-- ---------------------------------------------------------------------------
WITH names AS (
    SELECT lower(unnest(supported_models)) AS n FROM upstreams
    UNION
    SELECT lower(jsonb_object_keys(model_credit_rates)) AS n FROM plans
        WHERE model_credit_rates IS NOT NULL
    UNION
    SELECT lower(jsonb_object_keys(model_credit_rates)) AS n FROM rate_limit_policies
        WHERE model_credit_rates IS NOT NULL
    UNION
    SELECT lower(unnest(allowed_models)) AS n
        FROM api_keys WHERE allowed_models IS NOT NULL
)
INSERT INTO models (name, display_name, status, metadata)
SELECT DISTINCT n, n, 'active', '{}'::jsonb
FROM names
WHERE n <> '_default' AND n <> '' AND n IS NOT NULL
ON CONFLICT (name) DO NOTHING;

-- ---------------------------------------------------------------------------
-- 3. Expand routes.model_pattern (glob) → routes.model_names (TEXT[]).
--    The glob-to-LIKE substitution covers the patterns actually in use
--    (`*`, `claude-*`, `gpt-4o`). The pre-flight script flags patterns that
--    use `?` or character classes so admins can triage them beforehand.
-- ---------------------------------------------------------------------------
ALTER TABLE routes ADD COLUMN IF NOT EXISTS model_names TEXT[] NOT NULL DEFAULT '{}';

UPDATE routes r SET model_names = COALESCE((
    SELECT array_agg(DISTINCT m.name)
    FROM models m
    WHERE EXISTS (
        SELECT 1
        FROM upstream_group_members gm
        JOIN upstreams u ON u.id = gm.upstream_id
        WHERE gm.upstream_group_id = r.upstream_group_id
          AND u.status = 'active'
          AND m.name = ANY(u.supported_models)
          AND (
              r.model_pattern = m.name
              OR r.model_pattern = '*'
              OR (r.model_pattern LIKE '%*%' AND m.name LIKE replace(r.model_pattern, '*', '%'))
          )
    )
), '{}');

ALTER TABLE routes DROP COLUMN IF EXISTS model_pattern;

-- ---------------------------------------------------------------------------
-- 4. Referential integrity triggers. PG cannot express cross-table checks as
--    CHECK constraints, so both the writer side (inserts/updates reject
--    unknown names) and the reader side (deletes of a referenced model are
--    blocked) are enforced by triggers.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION assert_model_names_in_catalog(names TEXT[]) RETURNS VOID AS $$
DECLARE
    missing TEXT;
BEGIN
    IF names IS NULL THEN RETURN; END IF;
    SELECT n INTO missing
    FROM unnest(names) AS n
    WHERE n NOT IN (SELECT name FROM models)
    LIMIT 1;
    IF missing IS NOT NULL THEN
        RAISE EXCEPTION 'model name not in catalog: %', missing;
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;

-- Writer-side triggers on upstreams, routes, api_keys.
CREATE OR REPLACE FUNCTION trg_assert_upstream_models() RETURNS TRIGGER AS $$
BEGIN PERFORM assert_model_names_in_catalog(NEW.supported_models); RETURN NEW; END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_upstream_models ON upstreams;
CREATE TRIGGER trg_upstream_models BEFORE INSERT OR UPDATE ON upstreams
    FOR EACH ROW EXECUTE FUNCTION trg_assert_upstream_models();

CREATE OR REPLACE FUNCTION trg_assert_route_models() RETURNS TRIGGER AS $$
BEGIN PERFORM assert_model_names_in_catalog(NEW.model_names); RETURN NEW; END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_route_models ON routes;
CREATE TRIGGER trg_route_models BEFORE INSERT OR UPDATE ON routes
    FOR EACH ROW EXECUTE FUNCTION trg_assert_route_models();

CREATE OR REPLACE FUNCTION trg_assert_api_key_models() RETURNS TRIGGER AS $$
BEGIN PERFORM assert_model_names_in_catalog(NEW.allowed_models); RETURN NEW; END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_api_key_models ON api_keys;
CREATE TRIGGER trg_api_key_models BEFORE INSERT OR UPDATE ON api_keys
    FOR EACH ROW EXECUTE FUNCTION trg_assert_api_key_models();

-- Reader-side: block DELETE of a model while any array-column references it.
CREATE OR REPLACE FUNCTION trg_block_referenced_model_delete() RETURNS TRIGGER AS $$
DECLARE
    referrer TEXT;
BEGIN
    SELECT 'upstream ' || id INTO referrer
        FROM upstreams WHERE OLD.name = ANY(supported_models) LIMIT 1;
    IF referrer IS NULL THEN
        SELECT 'route ' || id INTO referrer
            FROM routes WHERE OLD.name = ANY(model_names) LIMIT 1;
    END IF;
    IF referrer IS NULL THEN
        SELECT 'api_key ' || id INTO referrer
            FROM api_keys WHERE OLD.name = ANY(allowed_models) LIMIT 1;
    END IF;
    IF referrer IS NOT NULL THEN
        RAISE EXCEPTION 'cannot delete model %: referenced by %', OLD.name, referrer;
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_models_delete_guard ON models;
CREATE TRIGGER trg_models_delete_guard BEFORE DELETE ON models
    FOR EACH ROW EXECUTE FUNCTION trg_block_referenced_model_delete();
