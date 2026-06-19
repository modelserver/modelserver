# Stripe Payserver Channel Design

## Overview

Add a third payment channel `stripe` to payserver, alongside the existing
WeChat Native Pay and Alipay Page Pay. Stripe targets overseas customers
paying in USD; WeChat / Alipay stay as the CNY rails. The integration uses
**Stripe Checkout Session** (Stripe-hosted payment page), keeps payserver as
the protocol-translation gateway, and reuses the existing two-phase callback
+ compensation pipeline end-to-end.

This is a v1 — one-shot payment only, no Stripe Subscription, no refund
events, no async-payment-method states.

## Confirmed Decisions

| Topic | Decision |
|---|---|
| Use case | One-shot payment (purchase 1/3/12 periods of a plan), semantically equivalent to wechat/alipay |
| Currency | USD only on Stripe channel; plan exposes both `price_cny_fen` and `price_usd_cents` |
| Integration form | Stripe Checkout Session (hosted page); returns `payment_url` like alipay |
| SDK | Official `github.com/stripe/stripe-go/v82` |
| Webhook scope (v1) | Only `checkout.session.completed` |
| Webhook destination | payserver `/notify/stripe` (symmetric with wechat/alipay) |
| Customer email | Passed from modelserver order owner; Stripe prefills, user can edit |
| Two-phase callback | Reuse existing `callback_status` + `compensate.Worker` |
| `gateway.PaymentRequest` | Extended with `Currency`, `ReturnURL`, `CustomerEmail`, `Metadata` |
| Multi-currency rollout | Plan gets new column `price_usd_cents`; old column renamed `price_per_period → price_cny_fen` in the same migration |

## §1 — File Changes

### payserver (new / modified)

```
services/payserver/
├── internal/
│   ├── config/config.go              # +StripeConfig + env var overrides
│   ├── gateway/
│   │   ├── gateway.go                # PaymentRequest gains Currency / ReturnURL / CustomerEmail / Metadata
│   │   └── stripe.go                 # NEW: CheckoutSession creation
│   ├── notify/
│   │   ├── stripe.go                 # NEW: webhook verify + persist + callback
│   │   └── stripe_test.go            # NEW
│   └── server/routes.go              # register POST /notify/stripe
├── cmd/payserver/main.go             # assemble stripe gateway / notify
├── config.example.yml                # +stripe: block
└── go.mod                            # +github.com/stripe/stripe-go/v82
```

### modelserver (modified)

```
internal/
├── billing/
│   ├── client.go                     # PaymentRequest gains CustomerEmail / Metadata
│   ├── http_client.go                # serialize new fields
│   ├── channel.go                    # NEW: ChannelPricing helper
│   ├── savings.go / savings_test.go  # field rename
├── store/
│   ├── migrations/
│   │   └── 049_plans_multi_currency.sql  # NEW: rename + add USD column + backfill
│   └── plans.go                      # column rename, scan new column
├── types/plan.go                     # rename field + add PriceUSDCents
├── admin/
│   ├── handle_orders.go              # ChannelPricing, cross-currency upgrade rejected, pass email/metadata
│   ├── handle_plans.go               # CRUD exposes new field
│   └── usage_period_test.go          # field rename
dashboard/src/
├── api/types.ts                      # Plan interface rename + add field
├── api/plans.ts                      # request payload field name
├── pages/admin/PlansPage.tsx         # two price inputs (CNY / USD)
└── pages/subscriptions/SubscriptionPage.tsx  # display price by currency
```

## §2 — `gateway.PaymentRequest` Extension

```go
type PaymentRequest struct {
    OutTradeNo    string            // compact UUID (no dashes), as today
    Description   string            // product name
    Amount        int64             // integer minor units (CNY fen / USD cents)
    Currency      string            // NEW: "CNY" / "USD"
    ReturnURL     string            // NEW: post-payment redirect
    CustomerEmail string            // NEW: optional; Stripe prefill, ignored by wechat/alipay
    Metadata      map[string]string // NEW: opaque business metadata, forwarded to gateway
}
```

`server/handler.go` forwards the four new fields. WeChat / Alipay gateways do
not read them, so behavior is unchanged for existing channels.

`billing.PaymentRequest` gains `CustomerEmail` and `Metadata`; `http_client.go`
serializes them; payserver `paymentAPIRequest` mirrors the fields and forwards
them into `gateway.PaymentRequest`.

Caller change in `admin/handle_orders.go`:

```go
owner, _ := st.GetUserByID(project.OwnerID)
email := ""
if owner != nil { email = owner.Email }

payClient.CreatePayment(r.Context(), billing.PaymentRequest{
    OrderID:       order.ID,
    ProductName:   plan.DisplayName,
    Channel:       body.Channel,
    Currency:      order.Currency,
    Amount:        order.Amount,
    NotifyURL:     billingCfg.NotifyURL,
    ReturnURL:     billingCfg.ReturnURL,
    CustomerEmail: email,
    Metadata:      map[string]string{"plan_slug": plan.Slug, "periods": strconv.Itoa(periods)},
})
```

If owner email lookup turns out to be awkward, sending `""` is acceptable —
Stripe will collect the email on its own checkout page. The field is purely
UX-prefill.

## §3 — Stripe Gateway (`services/payserver/internal/gateway/stripe.go`)

### Config

```go
type StripeGatewayConfig struct {
    SecretKey     string // sk_live_... / sk_test_...
    SuccessURL    string // fallback; PaymentRequest.ReturnURL takes precedence
    CancelURL     string // fallback
    DefaultLocale string // "auto" / "en" / "zh" / ...
}

type StripeGateway struct {
    sc  *client.API
    cfg StripeGatewayConfig
}

func NewStripeGateway(cfg StripeGatewayConfig) (*StripeGateway, error) {
    if cfg.SecretKey == "" {
        return nil, errors.New("stripe: secret_key is required")
    }
    sc := &client.API{}
    sc.Init(cfg.SecretKey, nil)
    return &StripeGateway{sc: sc, cfg: cfg}, nil
}

func (g *StripeGateway) Channel() string { return "stripe" }
```

### CreatePayment

```go
func (g *StripeGateway) CreatePayment(ctx context.Context, req *gateway.PaymentRequest) (*gateway.PaymentResult, error) {
    currency := strings.ToLower(req.Currency)
    if currency == "" {
        currency = "usd"
    }

    successURL := req.ReturnURL
    if successURL == "" { successURL = g.cfg.SuccessURL }
    cancelURL := g.cfg.CancelURL
    if cancelURL == "" { cancelURL = successURL }

    params := &stripe.CheckoutSessionParams{
        Mode:              stripe.String(string(stripe.CheckoutSessionModePayment)),
        SuccessURL:        stripe.String(successURL),
        CancelURL:         stripe.String(cancelURL),
        ClientReferenceID: stripe.String(req.OutTradeNo), // webhook uses this to locate the payment row
        LineItems: []*stripe.CheckoutSessionLineItemParams{{
            Quantity: stripe.Int64(1),
            PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
                Currency:   stripe.String(currency),
                UnitAmount: stripe.Int64(req.Amount),
                ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
                    Name: stripe.String(req.Description),
                },
            },
        }},
        PaymentIntentData: &stripe.CheckoutSessionPaymentIntentDataParams{
            Metadata: map[string]string{"order_id": req.OutTradeNo},
        },
    }
    params.AddMetadata("order_id", req.OutTradeNo)
    for k, v := range req.Metadata { params.AddMetadata(k, v) }

    if req.CustomerEmail != "" {
        params.CustomerEmail = stripe.String(req.CustomerEmail)
    }
    if g.cfg.DefaultLocale != "" {
        params.Locale = stripe.String(g.cfg.DefaultLocale)
    }

    sess, err := g.sc.CheckoutSessions.New(params)
    if err != nil { return nil, fmt.Errorf("stripe checkout session: %w", err) }

    return &gateway.PaymentResult{
        TradeNo:    sess.ID,  // cs_xxx — written to payments.trade_no
        PaymentURL: sess.URL, // hosted Stripe page URL the frontend redirects to
    }, nil
}
```

### Notes

- `OutTradeNo` keeps the existing compact-UUID convention used by handler.go
  for all channels. The webhook calls the same `uuidFromCompact` to restore
  the dashed form. No channel-specific branching.
- Amount unit is integer minor units (cents) — matches `payments.amount`
  semantics; no conversion needed.
- `ClientReferenceID = OutTradeNo`; metadata also carries `order_id` as a
  fallback (currently unused by webhook, available for audit).
- v1 does not create a Stripe `Customer` object; one-shot payments do not
  require it.

## §4 — Stripe Webhook (`services/payserver/internal/notify/stripe.go`)

Structurally symmetric with the wechat / alipay handlers.

```go
type StripeNotifyHandler struct {
    webhookSecret string
    store         *store.Store
    callback      *CallbackClient
    logger        *slog.Logger
}

func (h *StripeNotifyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // 1) Read raw body — webhook.ConstructEvent requires the unparsed bytes.
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
    if err != nil { http.Error(w, "read body", http.StatusBadRequest); return }

    // 2) Verify Stripe-Signature with the webhook secret.
    sig := r.Header.Get("Stripe-Signature")
    event, err := webhook.ConstructEvent(body, sig, h.webhookSecret)
    if err != nil {
        h.logger.Error("stripe notify: signature verification failed", "error", err)
        http.Error(w, "invalid signature", http.StatusBadRequest); return
    }

    // 3) v1: only checkout.session.completed; everything else acked.
    if event.Type != "checkout.session.completed" {
        w.WriteHeader(http.StatusOK); return
    }

    var sess stripe.CheckoutSession
    if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
        http.Error(w, "decode", http.StatusBadRequest); return
    }

    // 4) Only accept fully paid sessions; ignore unpaid / no_payment_required.
    if sess.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
        w.WriteHeader(http.StatusOK); return
    }

    orderID := uuidFromCompact(sess.ClientReferenceID)
    tradeNo := sess.ID
    paidAmount := sess.AmountTotal
    paidAt := time.Unix(event.Created, 0).UTC()

    // 5) Locate the payment row; verify channel + amount.
    payment, err := h.store.GetPaymentByOrderID(orderID)
    if err != nil || payment == nil {
        h.logger.Error("stripe notify: payment not found", "order_id", orderID, "error", err)
        http.Error(w, "not found", http.StatusNotFound); return
    }
    if payment.Channel != "stripe" {
        h.logger.Error("stripe notify: channel mismatch", "order_id", orderID, "channel", payment.Channel)
        http.Error(w, "channel mismatch", http.StatusBadRequest); return
    }
    if paidAmount != payment.Amount {
        h.logger.Error("stripe notify: amount mismatch",
            "order_id", orderID, "expected", payment.Amount, "got", paidAmount)
        http.Error(w, "amount mismatch", http.StatusBadRequest); return
    }

    // 6) Idempotency: already done.
    if payment.Status == "paid" && payment.CallbackStatus == "success" {
        w.WriteHeader(http.StatusOK); return
    }

    // Phase 1: persist paid state (CAS on status='pending').
    if payment.Status == "pending" {
        rawNotify, _ := json.Marshal(sess)
        if _, err := h.store.MarkPaymentPaid(orderID, tradeNo, string(rawNotify), paidAt); err != nil {
            h.logger.Error("stripe notify: mark paid failed", "order_id", orderID, "error", err)
            http.Error(w, "internal", http.StatusInternalServerError); return
        }
    }

    // 7) Ack Stripe immediately; do callback in detached context.
    w.WriteHeader(http.StatusOK)

    payload := DeliveryPayload{
        OrderID:    orderID,
        PaymentRef: payment.ID,
        Status:     "paid",
        PaidAmount: payment.Amount, // DB-authoritative
        PaidAt:     paidAt.Format(time.RFC3339),
    }
    cbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    if err := h.callback.Send(cbCtx, payload); err != nil {
        h.logger.Warn("stripe notify: callback to modelserver failed, will retry",
            "order_id", orderID, "error", err)
        h.store.IncrCallbackRetries(orderID)
        return
    }
    h.store.MarkCallbackSuccess(orderID)
}
```

### Routes

```go
r.Route("/notify", func(r chi.Router) {
    if cfg.WeChatNotify != nil { r.Post("/wechat", cfg.WeChatNotify.ServeHTTP) }
    if cfg.AlipayNotify != nil { r.Post("/alipay", cfg.AlipayNotify.ServeHTTP) }
    if cfg.StripeNotify != nil { r.Post("/stripe", cfg.StripeNotify.ServeHTTP) }
})
```

### Differences from wechat/alipay handlers (intentional)

- Raw-body verification (`webhook.ConstructEvent` must see unparsed bytes), so
  the handler must not pre-decode `r.Body`.
- Stripe retries on any non-2xx response (up to 3 days, exponential). Hence:
  signature / channel / amount mismatch → 4xx (definitive refusal); transient
  DB failure → 5xx (Stripe will retry).
- Explicit `payment.Channel == "stripe"` check. wechat/alipay handlers do not
  currently make this check; we add it on stripe and recommend a follow-up
  defensive patch for the other two (not in v1 scope, but listed as a quality
  improvement).

The `compensate.Worker` is channel-agnostic and picks up stripe rows
automatically — no changes needed.

## §5 — modelserver Multi-Currency Changes

### 5.1 Migration `049_plans_multi_currency.sql`

```sql
-- 1) Rename the existing column so its unit is explicit.
ALTER TABLE plans RENAME COLUMN price_per_period TO price_cny_fen;

-- 2) Add the USD-denominated column.
ALTER TABLE plans
    ADD COLUMN IF NOT EXISTS price_usd_cents BIGINT NOT NULL DEFAULT 0;

-- 3) Backfill USD prices from business anchors (no FX conversion):
--    pro=$20, max_5x=$100, max_20x=$200; other max_Nx scale linearly off
--    max_20x (Nx/20 × $200); max_2x = 2×pro = $40.
UPDATE plans SET price_usd_cents = CASE slug
    WHEN 'pro'      THEN   2000  --   $20
    WHEN 'max_2x'   THEN   4000  --   $40
    WHEN 'max_5x'   THEN  10000  --  $100
    WHEN 'max_20x'  THEN  20000  --  $200
    WHEN 'max_40x'  THEN  40000  --  $400
    WHEN 'max_60x'  THEN  60000  --  $600
    WHEN 'max_80x'  THEN  80000  --  $800
    WHEN 'max_100x' THEN 100000  -- $1000
    WHEN 'max_120x' THEN 120000  -- $1200
    WHEN 'max_200x' THEN 200000  -- $2000
    WHEN 'max_240x' THEN 240000  -- $2400
    ELSE price_usd_cents          -- free stays 0; unknown slugs stay 0
END,
updated_at = NOW();

COMMENT ON COLUMN plans.price_cny_fen   IS 'CNY price in fen for wechat/alipay channels';
COMMENT ON COLUMN plans.price_usd_cents IS 'USD price in cents for stripe channel';
```

The migration runs exactly once. Subsequent operator-edited prices are not
overwritten on redeploy. Any future `*_add_max_*x_plan.sql` must populate both
columns directly.

### 5.2 Go Field Rename (one-shot breaking)

| Old | New |
|---|---|
| `plans.price_per_period` | `plans.price_cny_fen` |
| `Plan.PricePerPeriod` | `Plan.PriceCNYFen` |
| JSON tag `price_per_period` | `price_cny_fen` |
| _(new)_ | `Plan.PriceUSDCents` / `price_usd_cents` |

Touch list:

- `internal/types/plan.go`
- `internal/store/plans.go` — SELECT/INSERT/UPDATE column lists + scan args
- `internal/admin/handle_orders.go`
- `internal/admin/handle_plans.go`
- `internal/admin/usage_period_test.go`
- `internal/billing/savings.go` + `savings_test.go`

Historical migration files (`001_init.sql`, `009..044_*.sql`) keep their
references to `price_per_period` — they ran against the database before the
rename and are immutable history.

### 5.3 `ChannelPricing` Helper

`internal/billing/channel.go`:

```go
func ChannelPricing(channel string, plan *types.Plan) (currency string, unitPrice int64, ok bool) {
    switch channel {
    case "wechat", "alipay":
        return "CNY", plan.PriceCNYFen, plan.PriceCNYFen > 0
    case "stripe":
        return "USD", plan.PriceUSDCents, plan.PriceUSDCents > 0
    default:
        return "", 0, false
    }
}
```

### 5.4 `handleCreateOrder` Diff

- Replace `unitPrice = plan.PricePerPeriod` with the channel-derived
  `basePrice` from `ChannelPricing(body.Channel, plan)`.
- Replace the hard-coded `Currency: "CNY"` with the derived `currency`.
- Apply the same derivation to `activePlan` when computing the
  upgrade-credit residual; if `ChannelPricing(channel, activePlan)` returns
  `ok=false`, reject the order. This safely refuses cross-currency upgrades
  (e.g. a CNY subscriber upgrading via Stripe).
- Pass `CustomerEmail` and `Metadata` per §2.

### 5.5 Dashboard

- `dashboard/src/api/types.ts` — split `price_per_period` into
  `price_cny_fen` + `price_usd_cents`.
- `dashboard/src/api/plans.ts` — request payload fields.
- `PlansPage.tsx` — two-input form (CNY / USD).
- `SubscriptionPage.tsx` — display by currency.

### 5.6 Unchanged

- `webhook/delivery` endpoint shape + HMAC scheme.
- `types.Order.Currency` (already exists).
- Time-based proration logic itself is currency-agnostic; it only compares
  values within the same currency.

### 5.7 Risk / Compatibility Notes

- This is a **breaking schema rename**. Old binaries fail when querying the
  renamed column. Deployment order: run migration → roll new binary within
  minutes. If the deployment pipeline cannot guarantee a tight window, fall
  back to a dual-column transition (add new + dual-write + drop old).
- Dashboard and backend must ship together to avoid missing fields in the
  plans page.
- Cross-currency upgrade: v1 rejects via `ChannelPricing` returning `ok=false`
  on the active plan side.

## §6 — Configuration & Deployment

### 6.1 `config.example.yml`

```yaml
stripe:
  secret_key: ""        # sk_live_... / sk_test_...
  webhook_secret: ""    # whsec_... — from Stripe Dashboard webhook endpoint
  success_url: ""       # fallback; PaymentRequest.ReturnURL preferred
  cancel_url: ""        # fallback
  default_locale: "auto"
```

### 6.2 Environment Overrides

```
PAYSERVER_STRIPE_SECRET_KEY
PAYSERVER_STRIPE_WEBHOOK_SECRET
PAYSERVER_STRIPE_SUCCESS_URL
PAYSERVER_STRIPE_CANCEL_URL
PAYSERVER_STRIPE_DEFAULT_LOCALE
```

Secrets are injected via env / secrets manager in production; the config file
stays empty.

### 6.3 `cmd/payserver/main.go` Assembly

```go
var stripeNotify *notifyPkg.StripeNotifyHandler
if cfg.Stripe.SecretKey != "" {
    sg, err := gateway.NewStripeGateway(gateway.StripeGatewayConfig{
        SecretKey:     cfg.Stripe.SecretKey,
        SuccessURL:    cfg.Stripe.SuccessURL,
        CancelURL:     cfg.Stripe.CancelURL,
        DefaultLocale: cfg.Stripe.DefaultLocale,
    })
    if err != nil { log.Fatalf("failed to init stripe gateway: %v", err) }
    gateways["stripe"] = sg

    if cfg.Stripe.WebhookSecret == "" {
        log.Fatal("stripe.webhook_secret is required when stripe is enabled")
    }
    stripeNotify = notifyPkg.NewStripeNotifyHandler(cfg.Stripe.WebhookSecret, st, callbackClient, logger)
    logger.Info("stripe gateway initialized")
}
```

`server.Config` gains `StripeNotify *notify.StripeNotifyHandler`.

### 6.4 Stripe Dashboard (one-time)

1. Developers → Webhooks → Add endpoint
2. URL: `https://<payserver-public-host>/notify/stripe`
3. Events: tick only `checkout.session.completed`
4. Copy the `whsec_...` value into `PAYSERVER_STRIPE_WEBHOOK_SECRET`
5. Provision both test and live mode (two secret_key + webhook_secret pairs)

### 6.5 Docker

Dockerfile needs no changes (`go mod download` picks up stripe-go).
`docker-compose.yml` adds the new env vars to the payserver service block.

### 6.6 modelserver Config

Unchanged. `billing.notify_url` / `billing.return_url` already exist; the
return URL should point to the dashboard "order complete" page.

### 6.7 Rollout

- If Stripe config is left empty, the `gateways` map has no `stripe` key and
  any order with `channel="stripe"` is rejected by payserver as
  "unsupported channel" before any state changes.
- A frontend feature flag controls Stripe visibility, preventing dashboard
  exposure before secrets are provisioned.

## §7 — Testing & Error Handling

### 7.1 New Unit Tests

| File | Coverage |
|---|---|
| `gateway/stripe_test.go` | `CreatePayment` param assembly: currency/amount, ClientReferenceID, CustomerEmail optional, SuccessURL precedence (PaymentRequest > config). Use `stripe.SetBackend` with a fake backend; assert the outgoing form. |
| `notify/stripe_test.go` | 1) signature failure → 400; 2) non-`checkout.session.completed` → 200; 3) `payment_status != paid` → 200; 4) payment row not found → 404; 5) channel mismatch → 400; 6) amount mismatch → 400; 7) happy path → MarkPaymentPaid + CallbackClient.Send invoked; 8) duplicate webhook (already paid + callback success) → 200 with no extra callback; 9) callback failure → IncrCallbackRetries. |
| `billing/channel_test.go` | `ChannelPricing` full matrix. |
| `admin/handle_orders_test.go` | Cross-currency upgrade rejected; same-currency upgrade prorate unchanged. |
| `store/migrations_049_test.go` | Migration applies → all 12 plan rows have expected `price_usd_cents`; queries on the old column fail, queries on the new succeed. |

### 7.2 Integration (semi-manual)

- payserver: `stripe listen --forward-to localhost:8090/notify/stripe` +
  `stripe trigger checkout.session.completed` → assert 200, payments row,
  modelserver webhook received.
- End-to-end: dashboard → choose Stripe → Stripe Checkout (test card
  `4242 4242 4242 4242`) → success_url → subscription activated.

### 7.3 Error Matrix

| Stage | Failure | Behavior |
|---|---|---|
| modelserver create order | cross-currency upgrade | 400 (rejected by `ChannelPricing`) |
| modelserver → payserver | gateway "unsupported channel" | order → failed, frontend error |
| payserver → Stripe API | Stripe rejects (BIN, currency, etc.) | 502 → modelserver order → failed; payment row stays pending |
| Stripe webhook | signature failure | 400 → Stripe retries (up to 3 days) |
| Stripe webhook | payment row missing | 404 → Stripe retries; rare race; eventually succeeds |
| Stripe webhook | amount / currency / channel mismatch | 400 + alert log; never recovers; operator intervention |
| Stripe webhook | `MarkPaymentPaid` fails | 500 → Stripe retries |
| Stripe webhook | callback to modelserver fails | 200 to Stripe (already persisted); compensate worker takes over |
| compensate worker | `callback_retries` exceeded | `callback_status='failed'`, ops investigates |

### 7.4 Security

- `Stripe-Signature` verification uses raw bytes (per §4).
- `secret_key` / `webhook_secret` only injected via env / secrets manager.
- payserver does not expose any Stripe secret to modelserver.
- `success_url` carries only `order_id` for UI display; subscription
  activation depends solely on the webhook, never on the redirect.

### 7.5 Logging / Monitoring

- Unified fields: `channel="stripe"`, `order_id`, `trade_no=cs_xxx`,
  `event_id=evt_xxx` (for cross-reference in the Stripe Dashboard).
- Recommend an alert on `callback_status='failed'` (not introduced here,
  but Stripe traffic will increase the signal volume).

## §8 — Out of Scope (v1)

1. **Refunds / disputes.** No listeners on `charge.refunded` /
   `charge.dispute.created`. Refunds are handled out-of-band in the Stripe
   Dashboard plus a manual modelserver admin call.
2. **Stripe Subscription (auto-renewal).** v1 is one-shot only. Renewal
   requires the user to re-order.
3. **Stripe `Customer` reuse.** Guest checkout with email prefill; no
   Customer object created or reused.
4. **Multi-account / multi-region routing.** One Stripe account per payserver
   instance. Multi-region requires a `gateways` map keyed by `channel + region`.
5. **Automated FX sync.** USD prices are operator-maintained.
6. **3DS-pending / async payment methods.** v1 accepts only
   `payment_status=paid`. This effectively restricts the channel to cards +
   Apple/Google Pay. Adding bank-debit / Bancontact / Alipay-via-Stripe etc.
   requires listening to `checkout.session.async_payment_succeeded` /
   `async_payment_failed`.
7. **Cross-currency upgrade proration.** Hard rejected in v1.
8. **Tax / invoicing.** No Stripe Tax, no invoices generated. CN customers
   continue offline invoicing; US sales tax to be re-evaluated by business
   before launch.

### Future Work (priority order)

- ✱ Refund support (high operational frequency).
- ✱ Async payment methods + a more complete `payment_status` state machine.
- Stripe Subscription (depends on a broader billing redesign).
