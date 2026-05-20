-- 039_add_gemini_3_5_flash.sql
--
-- Register gemini-3.5-flash in the model catalog. Per Google's docs
-- (https://ai.google.dev/gemini-api/docs/models/gemini-3.5-flash) this is
-- the stable release of Google's mid-tier multimodal model (text/image/
-- video/audio/PDF in, text out) with a 1,048,576-token input window and
-- a 65,536-token output window.
--
-- Standard-tier pricing per Google's pricing page
-- (https://ai.google.dev/gemini-api/docs/pricing), USD per 1M tokens:
--     input        $1.50
--     output       $9.00   (includes thinking tokens)
--     cache read   $0.15
--     cache creation — not billed as a separate event; storage is per-hour
--                      and intentionally not modelled here.
--
-- default_credit_rate uses the catalog convention `API_price / 7.5` (see
-- 015_add_opus_4_7_pricing.sql, 035_seed_catalog_default_credit_rates.sql):
--     input_rate         = 1.50 / 7.5 = 0.2
--     output_rate        = 9.00 / 7.5 = 1.2
--     cache_creation_rate= 0
--     cache_read_rate    = 0.15 / 7.5 = 0.02
--
-- Unlike the gemini-3-* rows noted in 035 (which were left without a
-- default rate because Google had not yet published a public standard
-- price), this migration sets default_credit_rate now that 3.5-flash has
-- a clear published price. The per-plan rate is still skipped, following
-- the gemini-3-* precedent: operators attach a per-plan rate via the
-- admin UI if they want subscription metering. The proxy layer already
-- forwards arbitrary Gemini model names transparently (internal/proxy/
-- provider_gemini.go), so once this row exists the model is selectable
-- in the routing UI and has a fallback default rate for billing/savings
-- paths.
--
-- Routes and upstreams are intentionally not seeded — operators wire the
-- model into the relevant gemini / vertex-google upstream group after
-- deployment.

INSERT INTO models (
    name,
    display_name,
    description,
    aliases,
    default_credit_rate,
    status,
    publisher,
    metadata
)
VALUES (
    'gemini-3.5-flash',
    'Gemini 3.5 Flash',
    'Google Gemini 3.5 Flash — multimodal (text, image, video, audio, PDF) input with text output, 1M-token context.',
    '{}',
    '{"input_rate":0.2,"output_rate":1.2,"cache_creation_rate":0,"cache_read_rate":0.02}'::jsonb,
    'active',
    'google',
    '{"context_window":1048576,"category":"chat"}'::jsonb
)
ON CONFLICT (name) DO UPDATE SET
    display_name        = EXCLUDED.display_name,
    description         = EXCLUDED.description,
    publisher           = EXCLUDED.publisher,
    default_credit_rate = EXCLUDED.default_credit_rate,
    metadata            = EXCLUDED.metadata,
    status              = EXCLUDED.status,
    updated_at          = NOW();
