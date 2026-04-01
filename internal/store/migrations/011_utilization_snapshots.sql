CREATE TABLE utilization_snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    upstream_id UUID NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    window_type TEXT NOT NULL,                -- '5h' or '7d'
    official_pct DOUBLE PRECISION NOT NULL DEFAULT 0, -- utilization percentage from Anthropic API
    resets_at TIMESTAMPTZ NOT NULL,                   -- window reset time from Anthropic API
    model_breakdown JSONB NOT NULL DEFAULT '{}', -- per-model token breakdown
    total_credits DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_util_snapshots_upstream ON utilization_snapshots(upstream_id, window_type, created_at DESC);

-- Prevent duplicate snapshots for the same window reset.
CREATE UNIQUE INDEX idx_util_snapshots_dedup ON utilization_snapshots(upstream_id, window_type, resets_at);
