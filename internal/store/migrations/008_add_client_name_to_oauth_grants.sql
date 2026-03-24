-- Add client_name column that was added to 006 after it had already been applied.
ALTER TABLE oauth_grants ADD COLUMN IF NOT EXISTS client_name TEXT NOT NULL DEFAULT '';
