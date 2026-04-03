-- Rename "vertex" provider to "vertex-anthropic" to align with the
-- Vertex AI publisher namespace (publishers/anthropic/models).
UPDATE upstreams SET provider = 'vertex-anthropic' WHERE provider = 'vertex';
