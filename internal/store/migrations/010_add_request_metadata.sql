-- 010: Add metadata JSONB column to requests for storing notable client headers.
ALTER TABLE requests ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}';
