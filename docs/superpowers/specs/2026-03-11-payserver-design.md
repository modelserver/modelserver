# Payserver Design: Independent Payment Microservice

## Overview

An independent payment microservice (`payserver`) that sits between modelserver and WeChat Pay / Alipay. It acts as a thin protocol-translation gateway: receives modelserver's standardized `PaymentRequest`, translates it into native WeChat/Alipay API calls, and on payment completion translates callbacks back into modelserver's `DeliveryPayload`.

Channels in scope: WeChat Native Pay (QR code), Alipay Page Pay (desktop web).

## Project Structure

```
services/payserver/
├── cmd/payserver/
│   └── main.go                  # Entry point, load config, start HTTP server
├── config.example.yml
├── internal/
│   ├── config/
│   │   └── config.go            # Config structs + loading
│   ├── gateway/
│   │   ├── gateway.go           # Gateway interface definition
│   │   ├── wechat.go            # WeChat Native Pay (wechatpay-go official SDK)
│   │   └── alipay.go            # Alipay Page Pay (hand-written V3 API + RSA2)
│   ├── notify/
│   │   ├── handler.go           # Unified callback routing
│   │   ├── callback.go          # Callback to modelserver (HMAC signing)
│   │   ├── wechat.go            # WeChat callback signature verification + AES decryption
│   │   └── alipay.go            # Alipay callback RSA2 signature verification
│   ├── server/
│   │   ├── routes.go            # Route registration
│   │   └── handler.go           # POST /payments handler
│   └── store/
│       ├── store.go             # DB connection + migration
│       ├── migrations/
│       │   └── 001_payments.sql
│       └── payments.go          # Payment record CRUD
├── go.mod                       # Independent go module
└── go.sum
```

Independent Go module: `github.com/modelserver/modelserver/services/payserver`, decoupled compilation from modelserver.

## Core Interfaces

### Gateway Interface

```go
type Gateway interface {
    CreatePayment(ctx context.Context, req *PaymentRequest) (*PaymentResult, error)
    Channel() string
}

type PaymentRequest struct {
    OutTradeNo  string // modelserver's order_id
    Description string // Product name
    Amount      int64  // Amount in CNY fen (cents)
    NotifyURL   string // Payserver's own callback URL for the payment platform
    ReturnURL   string // Frontend redirect URL after payment
}

type PaymentResult struct {
    TradeNo    string // Platform trade number (may be empty at creation time)
    PaymentURL string // WeChat: code_url; Alipay: full payment page URL
}
```

## Data Model

### Payments Table

```sql
CREATE TABLE IF NOT EXISTS payments (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id          TEXT NOT NULL UNIQUE,
    channel           TEXT NOT NULL,              -- "wechat" | "alipay"
    trade_no          TEXT NOT NULL DEFAULT '',   -- Platform trade number
    amount            BIGINT NOT NULL,            -- Amount in fen
    status            TEXT NOT NULL DEFAULT 'pending', -- pending / paid / failed
    callback_status   TEXT NOT NULL DEFAULT 'pending', -- pending / success / failed
    callback_retries  INT NOT NULL DEFAULT 0,
    raw_notify        JSONB,                      -- Raw callback payload for audit
    paid_at           TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_payments_order_id ON payments(order_id);
```

## Configuration

```yaml
server:
  addr: ":8090"

db:
  url: "postgres://user:password@localhost:5432/payserver?sslmode=disable"

callback:
  modelserver_url: "http://localhost:8081/api/v1/billing/webhook/delivery"
  webhook_secret: ""
  timeout: 10s

api_key: "change-me"

wechat:
  app_id: ""
  mch_id: ""
  mch_api_v3_key: ""
  mch_serial_no: ""
  mch_private_key_path: ""
  notify_url: ""

alipay:
  app_id: ""
  private_key_path: ""
  alipay_public_key_path: ""
  notify_url: ""
  return_url: ""
```

## API Protocol

### Create Payment (called by modelserver)

```
POST /payments
Authorization: Bearer {api_key}

{
    "order_id": "uuid-from-modelserver",
    "product_name": "Pro Plan",
    "channel": "wechat",
    "currency": "CNY",
    "amount": 2000,
    "notify_url": "http://modelserver:8081/api/v1/billing/webhook/delivery",
    "return_url": "https://app.example.com/payment/success",
    "metadata": {}
}
```

Response:
```json
{
    "payment_ref": "payserver-payment-uuid",
    "payment_url": "weixin://wxpay/bizpayurl?pr=xxxxx",
    "status": "pending"
}
```

`channel` is a top-level field in `PaymentRequest`, not in metadata.

### Payment Platform Callbacks

```
POST /notify/wechat    — WeChat async callback
POST /notify/alipay    — Alipay async callback
```

No Bearer token auth; each uses platform-native signature verification.

## Request Flow

```
User → Frontend → modelserver → payserver → WeChat/Alipay

① User selects plan + payment method (wechat/alipay)
② Frontend POST /api/v1/projects/{id}/orders
   {plan_slug:"pro", periods:1, channel:"wechat"}
③ modelserver creates order (pending)
④ modelserver POST payserver/payments
   {order_id, channel:"wechat", amount, ...}
⑤ payserver selects wechat gateway
   ├─ Calls WeChat V3 /pay/transactions/native
   ├─ Receives code_url
   ├─ Writes payments table (pending)
   └─ Returns {payment_ref, payment_url: code_url}
⑥ modelserver updates order (paying, payment_url)
⑦ Frontend receives payment_url, renders QR code
⑧ User scans and pays
⑨ WeChat POST payserver/notify/wechat
   ├─ payserver verifies signature + AES decrypts
   ├─ Updates payments table (paid, trade_no, raw_notify)
   ├─ Callbacks modelserver: POST webhook/delivery
   │  {order_id, payment_ref, status:"paid", paid_amount, paid_at}
   │  + X-Webhook-Signature: HMAC-SHA256(body, secret)
   └─ Replies {"code":"SUCCESS"} to WeChat
⑩ modelserver receives delivery → DeliverOrder → activates subscription
```

Alipay flow is similar except:
- Step ⑤ calls `alipay.trade.page.pay`, returns full payment page URL
- Step ⑦ frontend redirects to Alipay cashier page (not QR code)
- Step ⑨ callback is form-encoded, verified via RSA2 (not AES decryption)
- Step ⑨ replies plain text `success` to Alipay

## WeChat Native Pay Implementation

Uses official SDK `github.com/wechatpay-apiv3/wechatpay-go`.

**Create payment**: `nativeSvc.Prepay()` → returns `CodeUrl`.

**Callback**: `notify.NewRSANotifyHandler()` → `ParseNotifyRequest()` auto-verifies signature and decrypts AES-256-GCM → extracts `OutTradeNo`, `TransactionId`, `Amount.Total`, `SuccessTime`.

## Alipay Page Pay Implementation (Hand-Written)

No third-party SDK. Uses only Go standard library: `crypto/rsa`, `crypto/sha256`, `crypto/x509`, `encoding/pem`, `encoding/base64`, `net/url`.

### Signing (RSA2-SHA256)

```
Sign string = HTTP method\n + request path (with query)\n + timestamp\n + nonce\n + request body\n
Signature = SHA256WithRSA(sign string, app private key)
Authorization: ALIPAY-SHA256withRSA app_id=xxx,timestamp=xxx,nonce=xxx,sign=base64(signature)
```

### Create payment

Constructs a signed URL for `alipay.trade.page.pay`. The `PaymentURL` returned is the full URL that the frontend redirects to, opening Alipay's cashier page.

Amount conversion: internal fen (int64) → Alipay yuan string with 2 decimal places: `formatAmount(2000) → "20.00"`.

### Callback verification

Alipay callbacks are form-encoded POST. Verification:
1. Parse form parameters
2. Sort all params except `sign` and `sign_type` by key alphabetically, join with `&`
3. Verify `sign` with Alipay public key using RSA2-SHA256
4. Confirm `trade_status` is `TRADE_SUCCESS` or `TRADE_FINISHED`

## Callback to Modelserver

Unified in `notify/callback.go`. Constructs `DeliveryPayload`, signs with HMAC-SHA256, POSTs to modelserver's webhook endpoint.

```go
type DeliveryPayload struct {
    OrderID    string `json:"order_id"`
    PaymentRef string `json:"payment_ref"`  // payserver's payment.id
    Status     string `json:"status"`        // "paid"
    PaidAmount int64  `json:"paid_amount"`   // fen
    PaidAt     string `json:"paid_at"`       // RFC3339
}
```

## Error Handling

### Create payment failure

payserver API call to WeChat/Alipay fails → returns HTTP 502 to modelserver → modelserver marks order as `failed`. Payment record stays `pending`.

### Callback handling (two-phase)

1. **Phase 1: Confirm receipt.** After signature verification, immediately update payments table to `paid`, save `raw_notify`. Reply success to payment platform. This prevents repeated callbacks from the platform.

2. **Phase 2: Callback modelserver.** Synchronous call in the same request. On failure, mark `callback_status = 'pending'` for the compensation mechanism.

### Compensation mechanism

Background goroutine scans `callback_status = 'pending'` records every 30 seconds. Retries callback to modelserver with exponential backoff, up to 10 attempts. After max retries, marks `callback_status = 'failed'` for manual investigation.

## Idempotency (Three Layers)

1. **payserver create payment**: `order_id` unique index prevents duplicate platform orders. Same `order_id` with `status = pending` returns existing `payment_ref` and `payment_url`. Same `order_id` with `status = paid` returns error.

2. **payserver callback handling**: Checks payment status before processing. `paid` + `callback_status = success` → returns success immediately. `paid` + `callback_status != success` → retries modelserver callback only. `pending` → normal flow.

3. **modelserver delivery**: Checks order status. Already `delivered` → returns existing subscription. Not `paying` → rejects.

## Modelserver Changes Required

1. `billing/client.go`: Add `Channel string` field to `PaymentRequest`
2. `admin/handle_orders.go`: Accept `channel` in request body, pass to `PaymentRequest.Channel`
3. `admin/handle_orders.go`: Change hardcoded currency from `"USD"` to `"CNY"`
4. Database seed plans: Update `price_per_period` values from USD cents to CNY fen
