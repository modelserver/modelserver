# Extra-Usage Credits Wallet + Stripe Topup Path

## Overview

Replace the extra-usage subsystem's fen-denominated balance with a
**credits-denominated wallet**, and add a Stripe payment path so users
can fund the wallet with USD as well as CNY (wechat/alipay).

A single wallet per project holds credits; channel-specific unit prices
determine how much each currency-denominated payment buys. The
`1 USD = 6 CNY` exchange rate is encoded as a config knob applied only
at the moment of USD topup — credits are stable after that, and any
future rate change affects only subsequent payments.

Existing fen-denominated state (`extra_usage_settings.balance_fen`,
`extra_usage_transactions.amount_fen`, `requests.extra_usage_cost_fen`)
is migrated one-shot to credits using the current `credit_price_cny_fen`
(the existing `credit_price_fen` config renamed) at deploy time.

## Confirmed Decisions

| Topic | Decision |
|---|---|
| Unit of account | **credits** (BIGINT), the same unit `computeExtraUsageCostFen` already derives from `tokens × model.DefaultCreditRate` |
| Wallet shape | **Single** `balance_credits` column. Channel is provenance in the ledger, not a wallet attribute |
| CNY topup pricing | `credits = amount_fen × 1_000_000 / credit_price_cny_fen` |
| USD topup pricing | `credits = amount_cents × 1_000_000 / credit_price_usd_cents` |
| Unit-price config | Two independent integer prices: `credit_price_cny_fen` (default 5438, the existing `credit_price_fen` renamed) and `credit_price_usd_cents` (default 907, ≈ 5438/6 reflecting the 1USD≈6CNY business rule). No standalone exchange-rate config — the implicit rate is `credit_price_cny_fen / credit_price_usd_cents`, an emergent property operators can recompute but never set. Promotional USD pricing (e.g. $7/1M during a campaign) becomes one config knob change, not a cross-currency dance. |
| Deduction | Unchanged conceptually: compute cost in credits, `balance_credits -= cost`. No spillover, no priority order (single wallet) |
| Subscription ↔ wallet | No coupling. Any project on any plan can topup via any channel. UI naturally lays out "微信/支付宝 → CNY 单价 → credits" vs "Stripe → USD 单价 → credits" |
| Refund (Stripe) | MVP: reverse the original topup's credits (ledger `refund` row, `amount_credits = -orig`). Balance may go negative; extra-usage guard rejects until topped up or admin-adjusted |
| Refund (wechat/alipay) | Same code path. Refunds via these channels are rare in practice |
| Disputes / chargebacks | Same as refund (B1 in brainstorm). Account state may go negative; ops can `admin_adjust` to recover |
| Per-channel min/max topup | Stay in payment-channel currency: `min_topup_cny_fen` + `max_topup_cny_fen` (renamed from existing) and new `min_topup_usd_cents` + `max_topup_usd_cents`. Operator-friendly — they think in money for "minimum charge size" / "fraud cap" decisions. |
| Daily topup cap | Currency-agnostic: `daily_topup_limit_credits` (renamed from `daily_topup_limit_fen`). Sums credits-bought across all channels in the day. Matches the new "credits is the unit of account" model and avoids any cross-currency arithmetic. |
| Rounding (topup credits) | `floor` — user gets whole credits; sub-credit fraction goes to platform (≤ 1 credit) |
| Rounding (deduction cost) | `ceil` (existing) — platform doesn't undercharge |
| Migration of historical fen | One-shot at deploy: `balance_credits = balance_fen × 1_000_000 / credit_price_cny_fen` (the renamed-from-`credit_price_fen` value at migration time). Same conversion for `extra_usage_transactions.amount_fen` and `requests.extra_usage_cost_fen`. fen columns dropped. (All pre-migration topups were wechat/alipay, so CNY is the unambiguous source currency.) |
| Dashboard primary unit | credits balance; with informational "≈ ¥X.XX (at current unit price)" alongside |
| Existing schema column rename | `extra_usage_settings.balance_fen` → `balance_credits`; `extra_usage_transactions.amount_fen` → `amount_credits`; `requests.extra_usage_cost_fen` → `extra_usage_cost_credits`; `orders.extra_usage_amount_fen` → `extra_usage_amount_credits` |

## §1 — Schema Changes (single migration)

```sql
-- 1. settings: rename + change semantics
ALTER TABLE extra_usage_settings
    RENAME COLUMN balance_fen TO balance_credits;

-- 2. ledger: rename
ALTER TABLE extra_usage_transactions
    RENAME COLUMN amount_fen TO amount_credits;
ALTER TABLE extra_usage_transactions
    RENAME COLUMN balance_after_fen TO balance_after_credits;

-- 3. requests: rename
ALTER TABLE requests
    RENAME COLUMN extra_usage_cost_fen TO extra_usage_cost_credits;

-- 4. orders: rename
ALTER TABLE orders
    RENAME COLUMN extra_usage_amount_fen TO extra_usage_amount_credits;

-- 5. One-shot data conversion. Uses the credit_price_cny_fen value
--    (= the deployment's pre-rename credit_price_fen config) pinned
--    into the migration as a literal. Operators must NOT change
--    this value between writing the migration and applying it; the
--    runner aborts on retry if a prior audit row already exists.
--    See §6 "Migration safety" for the safeguard.
UPDATE extra_usage_settings
   SET balance_credits = (balance_credits * 1000000) / <CREDIT_PRICE_CNY_FEN>;
UPDATE extra_usage_transactions
   SET amount_credits = (amount_credits * 1000000) / <CREDIT_PRICE_CNY_FEN>,
       balance_after_credits = (balance_after_credits * 1000000) / <CREDIT_PRICE_CNY_FEN>;
UPDATE requests
   SET extra_usage_cost_credits = (extra_usage_cost_credits * 1000000) / <CREDIT_PRICE_CNY_FEN>
   WHERE extra_usage_cost_credits > 0;
UPDATE orders
   SET extra_usage_amount_credits = (extra_usage_amount_credits * 1000000) / <CREDIT_PRICE_CNY_FEN>
   WHERE extra_usage_amount_credits > 0;
```

`<CREDIT_PRICE_CNY_FEN>` is the deployment's currently-configured
`credit_price_fen` value at the moment the migration runs. All
pre-migration topups were CNY (Stripe path didn't exist), so the CNY
divisor is the unambiguous correct one. Captured from runtime config (or
injected via the same `SET LOCAL` GUC mechanism used by
`002_tenants.sql` in payserver). Recorded in the migration's audit
output so the conversion can be reproduced later.

The same migration file also creates:

```sql
-- §6 audit table: records the conversion factor used so any later
-- audit / re-derivation knows the exact divisor applied.
CREATE TABLE IF NOT EXISTS extra_usage_credit_migration_audit (
    id               SERIAL PRIMARY KEY,
    credit_price_cny_fen BIGINT NOT NULL,   -- divisor used to convert legacy fen balances
    applied_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- §4 refund idempotency: a single order can produce at most one
-- ledger row of each non-topup type. Topups already have
-- uniq_eut_topup_order (migration 017); add the symmetrical guard
-- for refunds.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_eut_refund_order
    ON extra_usage_transactions (order_id)
    WHERE type = 'refund' AND order_id IS NOT NULL;
```

Column NOT NULL / CHECK constraints carry over unchanged. The
column-rename is forward-only — rollback requires running a reverse
conversion migration, not just renaming columns back. The reverse
migration is intentionally not provided; if a rollback is needed,
ops accept that historical USD-sourced topups can't be cleanly
expressed in fen and must be re-keyed via admin adjustment.

## §2 — Configuration

`internal/config/config.go` — `ExtraUsageConfig` reshaped:

```go
type ExtraUsageConfig struct {
    Enabled                bool   `mapstructure:"enabled"`

    // Per-channel unit prices. fen and cents are minor-unit integers
    // in each respective currency (Chinese mass-noun "fen" stays
    // singular per existing codebase convention; English "cents"
    // pluralizes — matches existing PriceUSDCents in plan.go).
    // Independent — no auto-derived rate between them. Operator may
    // price them however the business decides (e.g. promotional USD pricing).
    CreditPriceCNYFen      int64  `mapstructure:"credit_price_cny_fen"`     // default 5438
    CreditPriceUSDCents     int64  `mapstructure:"credit_price_usd_cents"`    // default 907

    // Per-channel min/max topup amounts, in payment-side currency.
    // Operators set these based on payment-fee floors and fraud
    // tolerance, which are inherently per-currency concerns.
    MinTopupCNYFen         int64  `mapstructure:"min_topup_cny_fen"`        // default 1000  (¥10)
    MaxTopupCNYFen         int64  `mapstructure:"max_topup_cny_fen"`        // default 200000 (¥2000)
    MinTopupUSDCents        int64  `mapstructure:"min_topup_usd_cents"`       // default 167   ($1.67 ≈ ¥10 at default rate)
    MaxTopupUSDCents        int64  `mapstructure:"max_topup_usd_cents"`       // default 33333 ($333.33 ≈ ¥2000)

    // Currency-agnostic per-day cap on credits purchased. Matches
    // the "credits is the unit of account" model.
    DailyTopupLimitCredits int64  `mapstructure:"daily_topup_limit_credits"` // default ≈ sum of historical 5000 CNY-equivalent → ~919,455_000 credits
}
```

Env names follow viper's standard mapping
(`MODELSERVER_EXTRA_USAGE_CREDIT_PRICE_CNY_FEN`, etc.).

Validation at `Load`:
- Both `CreditPriceCNYFen` and `CreditPriceUSDCents` must be > 0
  (a zero would mean "free credits" — likely a misconfiguration; even
  intentional zero-price testing should opt in explicitly via a
  separate flag if ever needed).
- All min/max/daily values must be ≥ 0.
- `MinTopupCNYFen ≤ MaxTopupCNYFen`; same for USD.

Removed: the previous spec draft included a `USDToCNYRate` field.
That config is **deleted entirely** — having two independent unit
prices eliminates the need for a separate rate knob, and the implicit
rate (`CreditPriceCNYFen × 100 / CreditPriceUSDCents`, a unit-less
ratio of "how many fen per cent of payment buys the same credits")
falls out as an emergent property operators can inspect but never
need to set.

## §3 — Topup Routing

The existing `POST /api/v1/projects/{id}/extra-usage/topup` handler is
extended to accept an explicit `channel` and corresponding
denominated amount. The wire shape:

```json
{
  "channel": "wechat" | "alipay" | "stripe",
  "amount_fen": 500       // present when channel = wechat or alipay
  "amount_cents": 500     // present when channel = stripe
}
```

(Exactly one of `amount_fen` / `amount_cents` must be set per request.
Submitting both, neither, or the wrong field for the channel returns
400.)

Server side:

```go
switch channel {
case "wechat", "alipay":
    if amount_fen < cfg.MinTopupCNYFen { reject 400 }
    if amount_fen > cfg.MaxTopupCNYFen { reject 400 }
    credits = (amount_fen * 1_000_000) / cfg.CreditPriceCNYFen
    currency = "CNY"
    payment_amount = amount_fen        // in fen, passed to payserver

case "stripe":
    if amount_cents < cfg.MinTopupUSDCents { reject 400 }
    if amount_cents > cfg.MaxTopupUSDCents { reject 400 }
    credits = (amount_cents * 1_000_000) / cfg.CreditPriceUSDCents
    currency = "USD"
    payment_amount = amount_cents      // in cents, passed to payserver
}

// Credits-cap (currency-agnostic). Both channels add to this counter.
todayCredits := Store.SumDailyExtraUsageTopupCredits(projectID, dayStart)
if todayCredits + credits > cfg.DailyTopupLimitCredits {
    reject 429 "daily_topup_limit"
}

order = CreateOrder{
    Amount:                  payment_amount,
    Currency:                currency,
    Channel:                 channel,
    OrderType:               "extra_usage_topup",
    ExtraUsageAmountCredits: credits,   // <-- pre-computed at order time
}
```

Pre-computing `credits` at order creation time pins the conversion to
*this order*: any subsequent change to `CreditPriceCNYFen` /
`CreditPriceUSDCents` does NOT retroactively change how many credits
the user receives when this order finally delivers. The delivery
handler (`internal/admin/handle_delivery.go`) reads the value from
the order row, not from current config.

Daily-cap implementation: `SumDailyExtraUsageTopupFen` is renamed
`SumDailyExtraUsageTopupCredits` and reads the credits-denominated
`extra_usage_amount_credits` column directly — no currency
conversion needed, because each topup row already stores the
credits it bought at order-creation time:

```sql
SELECT COALESCE(SUM(extra_usage_amount_credits), 0)::bigint
FROM orders
WHERE project_id = $1
  AND order_type = 'extra_usage_topup'
  AND status IN ('paying','paid','delivered')
  AND created_at >= $2
```

This is the cleanest possible cross-channel cap — no rate parameter,
no per-currency branch.

## §4 — Deduction (`settleExtraUsage`)

Unchanged conceptually. The settle code already computes credits as
an intermediate value (`credits` is the second return from
`computeExtraUsageCostFen`). The change is to:

1. Stop computing the fen cost. `computeExtraUsageCostFen` → renamed
   `computeExtraUsageCostCredits`, returns `(creditsInt64, err)`. The
   `cost × creditPriceFen / 1_000_000` step is removed.
2. `DeductExtraUsageReq.AmountFen` → `AmountCredits`.
3. SQL UPDATE on `extra_usage_settings`: `balance_credits` instead
   of `balance_fen`.
4. Ledger insert: `amount_credits` (negative) and `balance_after_credits`.

The negation must continue to happen in Go (the PR #52 fix); SQL
`-$2` would re-introduce SQLSTATE 42725.

Refunds use the existing `type = 'refund'` ledger row shape, with
`amount_credits = -(original_topup_credits)`. A new
`Store.RefundExtraUsageTopup(orderID string) error` method finds the
original topup row by `order_id`, computes the reverse credits, and
applies it inside one transaction. Idempotent: a partial unique index
on `extra_usage_transactions(order_id, type)` prevents a duplicate
refund row.

## §5 — Refund Wiring

Stripe webhook handler in payserver already routes
`charge.refunded` events back to modelserver. Modelserver currently
has no refund handler for extra-usage topups — needs to be added:

```
POST /api/v1/billing/webhook/refund   (signed via HMACAuthMiddleware,
                                       same as delivery)
body: { "order_id": "...", "amount": ..., "currency": ... }

handler: lookup order, branch on order_type:
  - subscription:    existing subscription-refund path (currently no-op)
  - extra_usage_topup: call Store.RefundExtraUsageTopup(orderID)
```

Wechat/alipay refunds are operator-initiated via the payserver admin
UI (per the recent payserver redesign — out of scope here, but the
same `RefundExtraUsageTopup` will be the modelserver-side entry point
when payserver's refund admin endpoint calls back).

Balance may go negative after refund. The extra-usage guard already
checks `BalanceFen <= 0 → reject` (renamed: `BalanceCredits <= 0`).
Recovery: user tops up again, or operator runs
`POST /api/v1/admin/extra-usage/projects/{id}/topup` (direct
admin_adjust path that bypasses payment provider).

## §6 — Migration Safety

The conversion factor (the value of the deployment's existing
`credit_price_fen` config, post-rename `credit_price_cny_fen`) is
part of runtime config, not the schema. Migration must use the same
value the production server is configured with, or migrated balances
will silently misvalue.

Safeguard: the migration runner reads the value from a dedicated
deploy-time env var:

`MODELSERVER_MIGRATION_052_CREDIT_PRICE_CNY_FEN`

set explicitly by the deploying operator to the value being baked
into the conversion. The runner refuses to start if this env is
unset. (Not auto-discovered from `extra_usage.credit_price_cny_fen`
— the config layer is plumbing the migration shouldn't depend on,
and a stale value at deploy time would be invisible.)

After the conversion completes, the runner writes one row to a new
audit table:

```sql
CREATE TABLE IF NOT EXISTS extra_usage_credit_migration_audit (
    id              SERIAL PRIMARY KEY,
    credit_price_cny_fen BIGINT NOT NULL,   -- divisor used to convert legacy fen balances
    applied_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

If the migration is ever re-run (idempotency check: `SELECT COUNT(*)`
on this table), it aborts. This rules out double-conversion if a
deploy partially fails and is retried.

## §7 — Dashboard Display

The `/api/v1/projects/{id}/extra-usage` endpoint's response gains
new fields, while old fields are renamed:

```json
{
  "enabled": true,
  "balance_credits": 651675,            // primary unit, replaces balance_fen
  "monthly_limit_credits": 100000000,   // replaces monthly_limit_fen
  "monthly_spent_credits": 12345,
  "monthly_window_start": "2026-06-01T00:00:00+08:00",

  "credit_unit_prices": {
    "cny_fen_per_million": 5438,          // = CreditPriceCNYFen
    "usd_cents_per_million": 907,          // = CreditPriceUSDCents
    "implicit_usd_to_cny_rate": 5.996     // = CreditPriceCNYFen / CreditPriceUSDCents
                                          //   (informational; nothing reads/writes this)
  },

  "min_topup": {
    "cny_fen": 1000,                       // = MinTopupCNYFen (¥10)
    "usd_cents": 167                        // = MinTopupUSDCents ($1.67)
  },
  "max_topup": { "cny_fen": 200000, "usd_cents": 33333 },
  "daily_topup_limit_credits": 919455000,  // = DailyTopupLimitCredits

  "bypass_balance_check": false,
  "updated_at": "..."
}
```

Frontend computes display values as needed:
- "余额 651,675 credits ≈ ¥35.43" using `balance_credits × cny_fen_per_million / 1_000_000 / 100`
- Topup form shows both channel options with their unit prices baked
  into the help text

`MonthlySpentCredits` is computed identically to the old
`MonthlySpentFen` — sum the deduction ledger rows for the current
month — but in credits. The function `Store.GetMonthlyExtraSpendFen`
is renamed `GetMonthlyExtraSpendCredits`.

`GetExtraUsageSpendInWindow` (the "Period Paid" backend) likewise
returns credits. Dashboard renders as credits primary, with the
"≈ ¥X.XX" annotation derived from current `credit_unit_prices`.

## §8 — Caveats & Out-of-Scope

**Out of scope for this spec / V2 follow-ups:**

1. **Currency-display preference per user.** Some users may want
   their dashboard to show "≈ $X.XX" instead of "≈ ¥X.XX". V1 shows
   CNY-equivalent as the secondary display universally; user-
   preference toggle is V2.

2. **Pro-rated subscription credit accrual.** Some platforms credit
   the wallet with each subscription period. Out of scope — this is
   strictly an extra-usage wallet.

3. **Multiple concurrent topup orders per project.** Existing daily
   cap implicitly bounds this. Not changing.

4. **Runtime-mutable unit prices via DB row.** Env-config is
   sufficient for now (operators set unit prices at deploy time and
   restart to change). A future "promotional pricing" workflow
   (e.g., `credit_price_usd_cents` temporarily lowered to 800 during
   a campaign) would justify moving to a DB-backed admin-editable
   config with audit log; defer until needed.

5. **Refund partial recovery from spent credits.** The B1 policy
   (full reversal, allow negative) is intentionally simple. Per-
   topup remaining-credit accounting is V2.

**Caveats:**

- `credit_price_cny_fen` and `credit_price_usd_cents` are the runtime
  unit prices. An operator who changes either changes the price of
  all future topups via that channel. Existing credit balances are
  unaffected (they're whole numbers, not derived). The dashboard's
  "≈ ¥X.XX" display WILL change for everyone after a CNY-price
  change — this is correct: a credit is a fixed amount of compute,
  and the CNY-equivalent of one credit just got cheaper / more
  expensive.

- Negative balances are possible (post-refund, post-dispute). Guard
  middleware's `BalanceCredits <= 0` reject is the runtime safety
  net. Ops should monitor `extra_usage_settings` rows where
  `balance_credits < 0` for collection follow-up.

- The migration is one-way. Reverting requires re-deriving fen from
  credits using the SAME `credit_price_cny_fen` snapshot. The audit
  row in §6 preserves that value forever. Any post-migration USD
  topups can't be re-keyed cleanly (no fen equivalent under the
  pre-migration schema); ops would have to admin_adjust them off
  before rollback.

## §9 — Test Plan

Unit tests:
- `computeExtraUsageCostCredits` — happy path, all-zero usage, missing
  rate (credits computation is independent of unit prices — no price
  field involved)
- topup credits conversion — CNY and USD, edge case of exactly
  divisible values, rounding-down behavior
- `RefundExtraUsageTopup` — happy path, no-such-order, double-refund
  idempotency

Integration tests (DB-backed, gated on `TEST_DATABASE_URL`):
- Full topup → deduction → refund cycle in CNY
- Same in USD
- Mixed: CNY topup → partial deduction → USD topup → deduction → CNY
  refund → balance correctness
- Migration: seed pre-migration data with `balance_fen`, run migration,
  assert `balance_credits` value and ledger rows

End-to-end:
- Frontend topup form for both channels: pay → webhook → balance
  increment → balance reflects in dashboard

## §10 — Deployment Order

1. Deploy modelserver containing the schema migration but NOT the new
   Stripe topup wire path (CNY only initially). Migration runs;
   `extra_usage_credit_migration_audit` row written. Dashboard now
   shows credits primary with CNY-equivalent secondary. CNY topup
   continues to work unchanged in user-visible behavior (now credits-
   denominated under the hood).

2. After 24h stability, deploy update that enables the Stripe topup
   path. Frontend reveals the USD topup channel option in the form.

Staged so the schema/migration risk is decoupled from the
Stripe-integration risk.

## §11 — File / Module Changes (high-level)

```
internal/store/migrations/
  052_extra_usage_credits.sql      # NEW: §1 schema + data convert + §6 audit table + refund-idempotency index (single migration file — no reason to split)

internal/store/extra_usage.go       # rename helpers, AmountCredits everywhere
internal/store/orders.go            # ExtraUsageAmountCredits column
internal/store/requests.go          # extra_usage_cost_credits in CompleteRequest
internal/store/usage.go             # GetExtraUsageSpendInWindow returns credits

internal/types/extra_usage.go       # ExtraUsageSettings.BalanceCredits, etc.
internal/types/request.go           # ExtraUsageCostCredits field
internal/types/order.go             # ExtraUsageAmountCredits field

internal/proxy/executor.go          # settle*: AmountCredits, computeCredits
internal/proxy/extra_usage_guard_middleware.go
                                    # BalanceCredits compare

internal/config/config.go           # ExtraUsageConfig: rename CreditPriceFen → CreditPriceCNYFen,
                                    # add CreditPriceUSDCents, MinTopupUSDCents, MaxTopupUSDCents;
                                    # rename DailyTopupLimitFen → DailyTopupLimitCredits

internal/admin/handle_extra_usage.go
                                    # topup channel routing + extra fields
internal/admin/handle_delivery.go   # use ExtraUsageAmountCredits
internal/admin/handle_requests.go   # extra_usage_cost_credits read

internal/billing/savings.go         # ComputeCostBreakdown takes credits
dashboard/...                       # display credits + unit-price table
```

## §12 — Implementation Plan

Will be authored in a separate document via the writing-plans skill
after this spec is approved. Anticipated phases:

1. **Schema migration + Go renames** (largest mechanical change; no
   user-visible behavior change)
2. **Stripe topup wire path** (handler routing, payserver integration
   already supports `channel=stripe`)
3. **Refund webhook handler** (new endpoint, lookup-and-reverse)
4. **Frontend changes** (topup form, dashboard display)
5. **Tests + observability** (metrics for `topup_credits_total{channel=}`,
   `deduction_credits_total{result=}`)
