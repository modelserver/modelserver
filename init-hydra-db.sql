-- Create the hydra database if it doesn't exist.
-- This script runs during PostgreSQL container initialization.
SELECT 'CREATE DATABASE hydra OWNER modelserver'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'hydra')\gexec
