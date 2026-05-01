-- Rename "bedrock" provider to "bedrock-anthropic" so the existing channel
-- can coexist with the new "bedrock-openai" provider added in this release.
-- Mirrors migration 012 (vertex → vertex-anthropic) but extends the rename
-- to the requests table so historical analytics rows do not split across
-- two values for the same channel.
UPDATE upstreams SET provider = 'bedrock-anthropic' WHERE provider = 'bedrock';
UPDATE requests  SET provider = 'bedrock-anthropic' WHERE provider = 'bedrock';
