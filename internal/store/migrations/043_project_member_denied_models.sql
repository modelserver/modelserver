-- 043_project_member_denied_models.sql
--
-- Per-member model denylist. Owners/maintainers populate this via the
-- admin PATCH endpoint; the proxy checks it on every request before
-- the existing api_keys.allowed_models allowlist.
--
-- PostgreSQL 11+ applies ADD COLUMN ... NOT NULL DEFAULT '{}' as a
-- "fast default" — no table rewrite, every pre-existing row reads
-- back as '{}'. No backfill script needed.
ALTER TABLE project_members
  ADD COLUMN denied_models TEXT[] NOT NULL DEFAULT '{}';
