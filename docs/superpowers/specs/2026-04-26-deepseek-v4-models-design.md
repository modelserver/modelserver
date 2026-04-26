# DeepSeek V4 Models Support – Design

**Date**: 2026-04-26
**Status**: Approved (pending implementation)

## Goal

Add `deepseek-v4-flash` and `deepseek-v4-pro` to the model catalog and to all 9 subscription plans, so projects can route requests to DeepSeek upstreams. No discount handling for v4-pro (full price across the board).

## Scope

In scope:
- One database migration (`033_deepseek_v4.sql`) adding two model rows and updating all 9 plan rows.

Out of scope:
- Provider transformer code, provider constant, `SubscriptionEligibility` rules — none needed.
- v4-pro 2.5× discount window (full price for now; the discount expires 2026-05-05 anyway).
- Legacy aliases `deepseek-chat` / `deepseek-reasoner`.
- Long-context tier multipliers (DeepSeek charges flat within 1M context per pricing docs).
- Upstream / upstream-group / route records — created manually in the admin UI after deployment, since they require a real API key.

## Why no code

The existing `AnthropicTransformer.SetUpstream` and `OpenAITransformer.SetUpstream` both use `upstream.BaseURL` directly with no hard-coded provider URL (verified in `internal/proxy/provider_anthropic.go:29` and `internal/proxy/provider_openai.go:24`). DeepSeek exposes both an OpenAI-compatible endpoint at `https://api.deepseek.com` and an Anthropic-compatible endpoint at `https://api.deepseek.com/anthropic`. Either can be served by an upstream row with the appropriate `provider` and `base_url` — no transformer changes needed.

## Pricing derivation

DeepSeek bills in CNY. The project's billing pipeline expresses per-token rates as **credits**, with `cost_fen = ceil(credits × credit_price_fen / 1_000_000)`. With the production `credit_price_fen = 5438`:

```
credits_per_token = price_fen_per_token × 1_000_000 / 5438
                  = price_yuan_per_M_tokens × 100 × 1_000_000 / 5438 / 1_000_000
                  = price_yuan_per_M_tokens / 54.38
```

Catalog rates (extra-usage path) — full DeepSeek published prices:

| Model | input ¥/M | output ¥/M | cache hit ¥/M | input rate | output rate | cache_read rate | cache_creation rate |
|---|---|---|---|---|---|---|---|
| deepseek-v4-flash | 1 | 2 | 0.02 | 0.01838 | 0.03677 | 0.000368 | 0 |
| deepseek-v4-pro | 3 | 6 | 0.025 | 0.05517 | 0.11034 | 0.000460 | 0 |

`cache_creation_rate = 0` because DeepSeek does not bill cache creation as a separate event — cache misses are billed as ordinary input.

Subscription rates (each of the 9 plans) — catalog × 0.066, matching the compression ratio used for `gpt-5.5` (Pro plan: input 0.044 / catalog 0.667 ≈ 0.066):

| Model | input | output | cache_read |
|---|---|---|---|
| deepseek-v4-flash | 0.001213 | 0.002427 | 0.0000243 |
| deepseek-v4-pro | 0.003641 | 0.007283 | 0.0000304 |

`cache_creation_rate` omitted from subscription entries (defaults to 0 / unset, same as gpt-5.5 in plans).

All 9 plans use the same numbers — same convention as `gpt-5.5`, `gpt-5.1-codex-*`, etc. (no per-tier compression).

## Migration shape

`internal/store/migrations/033_deepseek_v4.sql`:

```sql
-- Catalog: two new models
INSERT INTO models (name, display_name, publisher, default_credit_rate, metadata, status)
VALUES
  ('deepseek-v4-flash', 'DeepSeek V4 Flash', 'deepseek',
    '{"input_rate":0.01838,"output_rate":0.03677,"cache_creation_rate":0,"cache_read_rate":0.000368}'::jsonb,
    '{"context_window":1000000,"category":"chat"}'::jsonb,
    'active'),
  ('deepseek-v4-pro', 'DeepSeek V4 Pro', 'deepseek',
    '{"input_rate":0.05517,"output_rate":0.11034,"cache_creation_rate":0,"cache_read_rate":0.000460}'::jsonb,
    '{"context_window":1000000,"category":"chat"}'::jsonb,
    'active')
ON CONFLICT (name) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  publisher = EXCLUDED.publisher,
  default_credit_rate = EXCLUDED.default_credit_rate,
  metadata = EXCLUDED.metadata,
  status = EXCLUDED.status,
  updated_at = NOW();

-- Plans: merge two model entries into every existing plan's model_credit_rates
UPDATE plans
SET model_credit_rates = COALESCE(model_credit_rates, '{}'::jsonb) || '{
  "deepseek-v4-flash": {"input_rate":0.001213,"output_rate":0.002427,"cache_creation_rate":0,"cache_read_rate":0.0000243},
  "deepseek-v4-pro":   {"input_rate":0.003641,"output_rate":0.007283,"cache_creation_rate":0,"cache_read_rate":0.0000304}
}'::jsonb,
    updated_at = NOW();
```

Single `UPDATE plans` (no WHERE clause) updates all 9 plans uniformly. The `||` operator on jsonb does a shallow merge, overwriting any existing entries with these names — safe to re-run the migration.

The catalog `INSERT … ON CONFLICT DO UPDATE` makes the catalog half re-runnable too. Together the migration is idempotent.

## Verification (post-deploy, manual)

1. `GET /api/v1/models?status=active` returns the two new models with their `default_credit_rate` populated.
2. `GET /api/v1/plans/{anyPlanID}` shows the two new entries inside `model_credit_rates`.
3. After admin creates an upstream + route in the UI:
   - Make a streaming request via the OpenAI-compat endpoint (`provider="openai"`, base URL `https://api.deepseek.com`) to `deepseek-v4-flash` and confirm:
     - The request lands in `requests` with non-zero `input_tokens` / `output_tokens`.
     - `credits_consumed` is non-zero and roughly matches expected price for the call.
   - Repeat with the Anthropic-compat endpoint (`provider="anthropic"`, base URL `https://api.deepseek.com/anthropic`).
   - If the Anthropic-compat endpoint's SSE format diverges from anthropic-sdk-go's expectations, the stream interceptor will under-report token usage. Check `output_tokens` against an equivalent non-streaming call.

## Risks

- **Anthropic-compat SSE compatibility**. We assume DeepSeek's `/anthropic` endpoint emits the same `message_start` / `content_block_delta` / `message_delta` SSE event shape that `internal/proxy/streaming.go` (anthropic-sdk-go) parses. If not, streaming requests will succeed end-to-end but `cache_read_tokens` / `output_tokens` may be 0 or wrong. Caught by post-deploy smoke (verification step 3 above).
- **API-key auth header on the Anthropic-compat endpoint**. `AnthropicTransformer` sets `x-api-key`. DeepSeek's docs don't explicitly document this; some Anthropic-compat servers require `Authorization: Bearer` instead. If `x-api-key` is rejected, switch the upstream to `provider="openai"` (which sets `Authorization: Bearer`) and route to the same models.
- **Cache token field name**. We assume DeepSeek returns `cache_read_input_tokens` (or equivalent) in the Anthropic-compat path. If they use a non-standard name, `cache_read_tokens` will read 0 and we'll under-bill cache hits — a billing-favorable error, but worth knowing.
- **DeepSeek price changes**. The published v4-pro price reverts to the discounted-but-permanent rate after some date; we're storing full price (¥3 / ¥6 / ¥0.025). If DeepSeek changes prices, we manually ship a new migration.
