-- Make policy_id nullable on subscriptions.
-- New subscriptions resolve rate limits from the plan at runtime
-- instead of snapshotting into a separate policy row.
ALTER TABLE subscriptions ALTER COLUMN policy_id DROP NOT NULL;
