-- 031_seed_gpt_image_2.sql
--
-- Register gpt-image-2 in the model catalog with the Images API credit rates.
-- Rates follow the catalog convention of API USD price per 1M tokens / 7.5:
--   text input          $5.00  -> 0.667
--   text cached input   $1.25  -> 0.167
--   text output         n/a    -> 0
--   image input         $8.00  -> 1.067
--   image cached input  $2.00  -> 0.267
--   image output        $30.00 -> 4.0
--
-- Routes and upstream supported_models are intentionally not seeded here;
-- operators still need to connect the model to an OpenAI upstream group.

INSERT INTO models (
    name,
    display_name,
    description,
    aliases,
    default_credit_rate,
    default_image_credit_rate,
    status,
    publisher,
    metadata
)
VALUES (
    'gpt-image-2',
    'GPT Image 2',
    'OpenAI image generation and editing model.',
    '{}',
    NULL,
    '{"text_input_rate":0.667,"text_cached_input_rate":0.167,"text_output_rate":0,"image_input_rate":1.067,"image_cached_input_rate":0.267,"image_output_rate":4.0}'::jsonb,
    'active',
    'openai',
    '{"capabilities":["image_generation","image_edit"],"provider_hint":"openai","category":"image"}'::jsonb
)
ON CONFLICT (name) DO UPDATE
SET default_image_credit_rate = COALESCE(
        models.default_image_credit_rate,
        EXCLUDED.default_image_credit_rate
    ),
    publisher = CASE
        WHEN models.publisher = '' THEN EXCLUDED.publisher
        ELSE models.publisher
    END,
    metadata = CASE
        WHEN models.metadata = '{}'::jsonb THEN EXCLUDED.metadata
        ELSE models.metadata
    END,
    updated_at = CASE
        WHEN models.default_image_credit_rate IS NULL
          OR models.publisher = ''
          OR models.metadata = '{}'::jsonb
        THEN NOW()
        ELSE models.updated_at
    END;
