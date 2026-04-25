BEGIN;

ALTER TABLE routes DROP CONSTRAINT routes_request_kinds_valid;

ALTER TABLE routes ADD CONSTRAINT routes_request_kinds_valid CHECK (
    request_kinds <@ ARRAY[
        'anthropic_messages',
        'anthropic_count_tokens',
        'openai_chat_completions',
        'openai_responses',
        'google_generate_content',
        'openai_images_generations',
        'openai_images_edits'
    ]::TEXT[]
    AND array_length(request_kinds, 1) >= 1
);

COMMIT;
