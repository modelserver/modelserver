-- Requests can be authenticated via API key OR OAuth access token.
-- Make api_key_id nullable and add oauth_grant_id for token-based auth.
ALTER TABLE requests ALTER COLUMN api_key_id DROP NOT NULL;
ALTER TABLE requests DROP CONSTRAINT IF EXISTS requests_api_key_id_fkey;
ALTER TABLE requests ADD COLUMN oauth_grant_id UUID REFERENCES oauth_grants(id) ON DELETE SET NULL;
