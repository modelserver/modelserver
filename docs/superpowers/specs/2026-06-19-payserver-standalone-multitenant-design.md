# Payserver Standalone Multi-Tenant Design

## Overview

Decouple payserver from "modelserver is its only upstream" by turning it
into a true multi-tenant payment gateway. Any upstream product gets its own
`tenant_id + secret`, calls payserver with `Authorization: Bearer
<tenant_id>:<secret>`, and receives payment-success callbacks at a tenant-
specific URL signed with a tenant-specific HMAC secret. An admin-only
React UI handles tenant CRUD and payment inspection, authenticated via
OIDC against the company IdP.

Scope deliberately stops short of per-tenant Stripe / wechat / alipay
credentials — provider config stays global. A "default" tenant is created
during migration so the existing modelserver deployment keeps working with
a one-time env var update.

## Confirmed Decisions

| Topic | Decision |
|---|---|
| Tenant identity | `tenant_id UUID + secret`. Bearer header `<tenant_id>:<secret>` |
| Auth storage | `secret_hash` (bcrypt) in tenants table; cleartext shown only at create/rotate response |
| Callback location | `callback_url + callback_secret` columns on tenants table — payserver derives from tenant on payment success |
| Migration path | Migration 002 creates `default` tenant from legacy `cfg.APIKey + cfg.Callback.*`; backfills `payments.tenant_id` |
| Provider credentials | Global config, all tenants share (Stripe account / wechat MCH / alipay app are operator-level, not tenant-level) |
| Admin UI scope | Two pages: Tenants CRUD + Payments inspector. Admin-only — no tenant self-service |
| Admin frontend stack | React + TypeScript + Vite + Tailwind + shadcn (matches existing modelserver dashboard) |
| Admin auth | OIDC against company IdP (issuer/clientID/secret/redirect URL configured in payserver) |
| Frontend hosting | Built `dist/` embedded into payserver Go binary via `//go:embed`; served at `/admin/` |
| Term | "Payment" (matches Stripe / industry; existing `payments` table name unchanged) |

## §1 — File Changes (high level)

```
services/payserver/
├── internal/
│   ├── store/
│   │   ├── migrations/
│   │   │   └── 002_tenants.sql           # NEW: tenants table + default tenant + payments.tenant_id FK
│   │   ├── tenants.go                    # NEW: CRUD on Store
│   │   ├── payments.go                   # MODIFY: SELECT/INSERT carry tenant_id
│   │   └── migrations_002_test.go        # NEW
│   ├── tenant/
│   │   ├── tenant.go                     # NEW: types + GenerateSecret/HashSecret/VerifySecret
│   │   └── tenant_test.go                # NEW
│   ├── server/
│   │   ├── auth.go                       # NEW: tenantAuthMiddleware
│   │   ├── auth_test.go                  # NEW
│   │   ├── handler.go                    # MODIFY: handleCreatePayment reads tenant from ctx, writes tenant_id
│   │   ├── admin_handler.go              # NEW: CRUD + rotate-secret + payments list
│   │   ├── admin_handler_test.go         # NEW
│   │   ├── oidc.go                       # NEW: code grant + session cookie middleware
│   │   ├── oidc_test.go                  # NEW
│   │   └── routes.go                     # MODIFY: mount /admin/* with OIDC, /admin/static for embed
│   ├── notify/
│   │   ├── callback.go                   # MODIFY: Send(ctx, target, payload) — target is per-call
│   │   ├── wechat.go alipay.go stripe.go # MODIFY: each derives CallbackTarget from payment.tenant_id
│   │   └── *_test.go                     # MODIFY: tenant fixture
│   ├── compensate/
│   │   ├── compensate.go                 # MODIFY: per-row tenant lookup; inactive tenant → MarkFailed
│   │   └── compensate_test.go            # MODIFY
│   └── config/
│       └── config.go                     # MODIFY: deprecate cfg.APIKey/cfg.Callback.*; add OIDCConfig
├── cmd/payserver/
│   └── main.go                           # MODIFY: OIDC wire-up; migration bootstrap session settings; embed admin dist
├── admin/                                # NEW: React+Vite+TS admin frontend
│   ├── package.json
│   ├── pnpm-lock.yaml
│   ├── vite.config.ts
│   ├── tsconfig.json
│   ├── index.html
│   └── src/
│       ├── main.tsx, App.tsx
│       ├── api/{client,tenants,payments}.ts
│       ├── pages/{TenantsPage,TenantDetailPage,PaymentsPage}.tsx
│       └── components/{AppShell,SecretRevealOnce,ui/...}.tsx
├── admin_dist/                           # GENERATED at build time, embedded
└── go.mod / go.sum                       # +go-oidc, +golang.org/x/oauth2, +golang.org/x/crypto/bcrypt
```

## §2 — Tenant Model + Auth

### tenants table

```sql
CREATE TABLE IF NOT EXISTS tenants (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL UNIQUE,
    secret_hash     TEXT NOT NULL,
    callback_url    TEXT NOT NULL DEFAULT '',
    callback_secret TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_name ON tenants(name);
```

### payments table changes

```sql
ALTER TABLE payments ADD COLUMN IF NOT EXISTS tenant_id UUID;
-- Backfill (see §3 migration 002):
UPDATE payments SET tenant_id = (SELECT id FROM tenants WHERE name = 'default')
WHERE tenant_id IS NULL;
ALTER TABLE payments ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE payments
    ADD CONSTRAINT fk_payments_tenant
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_payments_tenant ON payments(tenant_id);
-- idx_payments_order_id UNIQUE is preserved as-is. Cross-tenant order_id
-- collision is prevented globally — operators are expected to use UUIDs.
```

### Tenant type + crypto

```go
// services/payserver/internal/tenant/tenant.go
package tenant

import (
    "crypto/rand"
    "encoding/base64"
    "time"
    "golang.org/x/crypto/bcrypt"
)

type Tenant struct {
    ID             string    `json:"id"`
    Name           string    `json:"name"`
    SecretHash     string    `json:"-"`              // never serialize
    CallbackURL    string    `json:"callback_url"`
    CallbackSecret string    `json:"-"`              // never serialize
    Description    string    `json:"description"`
    IsActive       bool      `json:"is_active"`
    CreatedAt      time.Time `json:"created_at"`
    UpdatedAt      time.Time `json:"updated_at"`
}

func GenerateSecret() (string, error) {
    b := make([]byte, 32)
    if _, err := rand.Read(b); err != nil { return "", err }
    return base64.RawURLEncoding.EncodeToString(b), nil
}

func HashSecret(secret string) (string, error) {
    h, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
    return string(h), err
}

func VerifySecret(hash, secret string) bool {
    return bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)) == nil
}
```

### Auth middleware

```go
// services/payserver/internal/server/auth.go
type ctxKey int
const ctxKeyTenant ctxKey = iota

func TenantFromContext(ctx context.Context) *tenant.Tenant {
    return ctx.Value(ctxKeyTenant).(*tenant.Tenant)
}

const dummyBcryptHash = "$2a$10$dummyhashthatwillneverpass......................"

func tenantAuthMiddleware(st *store.Store, logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            auth := r.Header.Get("Authorization")
            if !strings.HasPrefix(auth, "Bearer ") {
                writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing bearer token"})
                return
            }
            id, secret, ok := strings.Cut(auth[7:], ":")
            if !ok {
                writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "malformed token; expected <tenant_id>:<secret>"})
                return
            }
            t, err := st.GetTenantByID(id)
            if err != nil || t == nil || !t.IsActive {
                _ = tenant.VerifySecret(dummyBcryptHash, secret) // mask timing
                writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
                return
            }
            if !tenant.VerifySecret(t.SecretHash, secret) {
                writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
                return
            }
            ctx := context.WithValue(r.Context(), ctxKeyTenant, t)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

Notes:
- bcrypt compare is constant-time; timing-mask call on the tenant-not-found
  branch is best-effort (latency parity, not provable equality).
- `is_active=false` takes effect immediately; no caching of tenant lookups.
- Header form `<uuid>:<secret>` survives standard HTTP clients without
  custom encoding.

## §3 — Migration 002 + Default-Tenant Bootstrap

### `002_tenants.sql`

The migration runner (`store.migrate` in `store.go`) already wraps each
migration's SQL in a `BEGIN/COMMIT` tx and records `schema_migrations` in
the **same** tx. Therefore this SQL file must **not** include its own
`BEGIN/COMMIT` — nested transactions in PostgreSQL emit a warning and
worse, an inner `COMMIT` actually closes the outer tx so the
`schema_migrations` insert lands outside the migration's scope, allowing
duplicate runs on restart.

```sql
-- 1) tenants table
CREATE TABLE IF NOT EXISTS tenants (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL UNIQUE,
    secret_hash     TEXT NOT NULL,
    callback_url    TEXT NOT NULL DEFAULT '',
    callback_secret TEXT NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_name ON tenants(name);

-- 2) Default tenant from legacy config values. The Go runner injects the
-- three current_setting() values into this transaction via SET LOCAL
-- before running this SQL (see store.go change below). current_setting()
-- with a missing GUC raises ERROR — the migration fails fast and the
-- transaction rolls back. That's the desired behavior when the operator
-- forgot to set PAYSERVER_DEFAULT_TENANT_SECRET.
INSERT INTO tenants (name, secret_hash, callback_url, callback_secret, description)
VALUES (
    'default',
    current_setting('payserver.default_tenant_secret_hash'),
    current_setting('payserver.default_callback_url'),
    current_setting('payserver.default_callback_secret'),
    'Auto-created during migration 002. Maps to the legacy cfg.APIKey / cfg.Callback.* fields. Rename via admin API once multi-tenancy is in use.'
)
ON CONFLICT (name) DO NOTHING;

-- 3) payments.tenant_id with FK
ALTER TABLE payments ADD COLUMN IF NOT EXISTS tenant_id UUID;
UPDATE payments SET tenant_id = (SELECT id FROM tenants WHERE name = 'default')
WHERE tenant_id IS NULL;
ALTER TABLE payments ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE payments
    ADD CONSTRAINT fk_payments_tenant
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE RESTRICT;
CREATE INDEX IF NOT EXISTS idx_payments_tenant ON payments(tenant_id);
```

### main.go bootstrap (always-set, fail-on-rerun-OK)

Rather than gating on "is 002 already applied" (which would need a second
DB connection before `store.New`), we always derive bootstrap values from
the env at startup. If 002 has already run, the values are computed but
unused (the migration runner sees 002 in `schema_migrations` and skips
the SQL entirely, so no `SET LOCAL` runs and no `current_setting()`
fires). On the first run, the values are required; absence is detected
inside the migration when `current_setting()` raises ERROR.

```go
// cmd/payserver/main.go (before store.New)
//
// On the very first deploy of the multi-tenant payserver, migration 002
// runs and needs the operator-supplied default-tenant secret. On every
// subsequent boot, 002 has already run; bootstrap.* values are derived
// but never consumed.
var bootstrap store.MigrationBootstrap
if cfg.DefaultTenantSecret != "" {
    hash, err := tenant.HashSecret(cfg.DefaultTenantSecret)
    if err != nil {
        log.Fatalf("hash default tenant secret: %v", err)
    }
    bootstrap = store.MigrationBootstrap{
        DefaultTenantSecretHash: hash,
        DefaultCallbackURL:      cfg.Callback.ModelserverURL,
        DefaultCallbackSecret:   cfg.Callback.WebhookSecret,
    }
}
st, err := store.New(cfg.DB.URL, logger, bootstrap)
// If 002 hasn't run yet AND bootstrap.DefaultTenantSecretHash == "",
// the migration transaction will fail with:
//   ERROR: unrecognized configuration parameter "payserver.default_tenant_secret_hash"
// Operator sees that, sets PAYSERVER_DEFAULT_TENANT_SECRET, restarts.
```

### `store.migrate` modification

`store.New` takes a third arg `bootstrap MigrationBootstrap`. The
existing `migrate(ctx)` loop is extended: when about to execute migration
002's SQL, the runner first runs three `SET LOCAL` statements inside the
same migration tx, then `tx.Exec(content)`:

```go
// services/payserver/internal/store/store.go (within the per-migration tx block)
if name == "002_tenants.sql" && s.bootstrap.DefaultTenantSecretHash != "" {
    for _, stmt := range []struct{ key, val string }{
        {"payserver.default_tenant_secret_hash", s.bootstrap.DefaultTenantSecretHash},
        {"payserver.default_callback_url",       s.bootstrap.DefaultCallbackURL},
        {"payserver.default_callback_secret",    s.bootstrap.DefaultCallbackSecret},
    } {
        // SET LOCAL with parameterized value not directly supported;
        // values are operator-controlled (hash / URL / secret) so we
        // quote-escape via pq's recommended approach. Values containing
        // a single quote (impossible for bcrypt or our URLs/secrets)
        // would need explicit escaping — assert and bail if seen.
        if strings.ContainsRune(stmt.val, '\'') {
            tx.Rollback(ctx)
            return fmt.Errorf("migration 002 bootstrap value for %s contains a single quote (unsupported)", stmt.key)
        }
        if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL %s = '%s'", stmt.key, stmt.val)); err != nil {
            tx.Rollback(ctx)
            return fmt.Errorf("set local %s: %w", stmt.key, err)
        }
    }
}
if _, err := tx.Exec(ctx, string(content)); err != nil { ... }
```

The single-quote guard is belt-and-braces — bcrypt output is `[A-Za-z0-9./$]`,
and the URL/secret come from operator config that's already been read by
viper's YAML/env layer (no SQL injection vector through that path).

### Operator runbook

```bash
# Step 1: generate the default tenant's API secret (used by modelserver
# to authenticate to payserver — distinct from the HMAC secret below).
openssl rand -base64 32   # e.g. "Xc2BkP9...=="    ← record this

# Step 2: payserver: set bootstrap env and the existing per-channel
# secrets your deployment already uses. Note that the EXISTING
# `PAYSERVER_CALLBACK_WEBHOOK_SECRET` is what migration 002 copies into
# default tenant.callback_secret — DO NOT change it during this deploy
# (changing it later via the admin UI is fine).
export PAYSERVER_DEFAULT_TENANT_SECRET="Xc2BkP9...=="
# Keep existing PAYSERVER_CALLBACK_MODELSERVER_URL and
# PAYSERVER_CALLBACK_WEBHOOK_SECRET set — migration 002 reads them once.

# Step 3: restart payserver. Migration 002 runs once. Logs print:
#   "default tenant id=<uuid>"
# Record <uuid>.

# Step 4: modelserver: swap the auth header value. The webhook secret
# stays exactly as it was (see §3.x "Cross-service env coupling" below).
# old: MODELSERVER_BILLING_PAYMENT_API_KEY=<old-payserver-api-key>
# new: MODELSERVER_BILLING_PAYMENT_API_KEY=<uuid>:Xc2BkP9...==

# Step 5: restart modelserver.
```

### Cross-service env coupling (critical — read before deploying)

The HMAC webhook channel (payserver → modelserver `POST
/api/v1/admin/billing/webhook/delivery`) has a secret that **lives on
both sides** and must remain identical for callbacks to authenticate.
Migration 002 freezes that secret onto the default tenant; modelserver's
verifier middleware reads it from a separate env. Diverging them silently
breaks delivery callbacks (modelserver returns 401, payserver's
compensate worker retries and eventually marks `callback_status=failed`).

| Side | Env | Used For | Action |
|---|---|---|---|
| modelserver | `MODELSERVER_BILLING_WEBHOOK_SECRET` | HMAC verifier on `/api/v1/admin/billing/webhook/delivery` route mount + signature check | **KEEP as-is.** Same string before and after this PR |
| modelserver | `MODELSERVER_BILLING_PAYMENT_API_KEY` | Bearer header sent on `POST /payments` to payserver | **REPLACE** with `<default-tenant-uuid>:<PAYSERVER_DEFAULT_TENANT_SECRET>` |
| modelserver | `MODELSERVER_BILLING_PAYMENT_API_URL` | payserver base URL | **KEEP** |
| modelserver | `MODELSERVER_BILLING_NOTIFY_URL` | (no longer sent on `PaymentRequest`; payserver derives from tenant.callback_url) | **DEPRECATED** — value is ignored by payserver going forward. Can be unset or left for documentation. |
| modelserver | `MODELSERVER_BILLING_RETURN_URL` | per-order success_url prefix (PR #47) | **KEEP** |
| payserver | `PAYSERVER_CALLBACK_WEBHOOK_SECRET` | One-time bootstrap into default-tenant `callback_secret` | **KEEP for the deploy, MUST equal `MODELSERVER_BILLING_WEBHOOK_SECRET`.** After migration 002 runs, value is ignored — future changes happen through the admin UI by editing the tenant's `callback_secret` |
| payserver | `PAYSERVER_CALLBACK_MODELSERVER_URL` | One-time bootstrap into default-tenant `callback_url` | Same disposition |
| payserver | `PAYSERVER_API_KEY` | The legacy single bearer key (entirely removed) | **DELETE.** Any value silently ignored after this PR — the auth middleware no longer reads it |
| payserver | `PAYSERVER_DEFAULT_TENANT_SECRET` | NEW; consumed only by migration 002 on first run | Set before first deploy; can be unset on subsequent deploys |

**Rollback path**: there is none in v1. Once migration 002 has applied,
the auth middleware permanently expects `<id>:<secret>` form. A real
emergency rollback means manually inserting into the now-required
`tenant_id` column for any new payments while running the old code —
deeply unsafe. Do not deploy this without confirming the modelserver env
change is queued.

### Deployment window — service interruption

Between "payserver restart" (Step 3) and "modelserver restart" (Step 5),
**all `POST /payments` from modelserver fail with 401** because old-format
`Authorization: Bearer <old-api-key>` doesn't parse as `<uuid>:<secret>`.
Subscription ordering is a low-frequency operation; the window is
typically 30–60s with rolling restarts. To minimize:

- Pre-stage the `MODELSERVER_BILLING_PAYMENT_API_KEY` change so step 5
  is just a process restart, not an env mutation
- Pick a low-traffic deploy window (e.g. 02:00–05:00 local)
- If you cannot accept any window, add a **temporary auth shim**: in
  `auth.go`, before invoking the tenant middleware, check if the bearer
  token matches a hard-coded `cfg.LegacyAPIKey` and inject the default
  tenant if so. Strip the shim in the next PR. Not in this spec; mention
  in Future Work if you want it followed up.

### Legacy field handling

| Legacy cfg field | Disposition |
|---|---|
| `cfg.APIKey` | Deleted from Go config + ApplyEnvOverrides + main.go. Operator may delete `PAYSERVER_API_KEY` env after this PR |
| `cfg.Callback.ModelserverURL` | Read once by migration 002 → stored on default tenant. Ignored on subsequent boots (warn at startup if still set after 002 applied) |
| `cfg.Callback.WebhookSecret` | Same |
| `cfg.Callback.Timeout` | Retained as global HTTP timeout for `CallbackClient` |

## §4 — Per-Tenant Callback (handler + notify + compensate)

### Inbound: `POST /payments` drops `notify_url`

```go
type paymentAPIRequest struct {
    OrderID       string            `json:"order_id"`
    ProductName   string            `json:"product_name"`
    Channel       string            `json:"channel"`
    Currency      string            `json:"currency"`
    Amount        int64             `json:"amount"`
    ReturnURL     string            `json:"return_url"`
    CustomerEmail string            `json:"customer_email,omitempty"`
    Metadata      map[string]string `json:"metadata,omitempty"`
    // NotifyURL REMOVED — derived from authenticated tenant
}
```

`handleCreatePayment` writes `payment.TenantID = TenantFromContext(...).ID`.

### `CallbackClient` refactor

```go
// services/payserver/internal/notify/callback.go
type CallbackTarget struct {
    URL    string
    Secret string
}

type CallbackClient struct {
    httpClient *http.Client
}

func NewCallbackClient(timeout time.Duration) *CallbackClient {
    return &CallbackClient{httpClient: &http.Client{Timeout: timeout}}
}

// Send POSTs the payload to target.URL HMAC-signed with target.Secret.
// Empty target.URL is a no-op success (interpret as "this tenant does not
// want callbacks" — useful for read-only or test tenants).
func (c *CallbackClient) Send(ctx context.Context, target CallbackTarget, payload DeliveryPayload) error {
    if target.URL == "" { return nil }
    body, _ := json.Marshal(payload)
    mac := hmac.New(sha256.New, []byte(target.Secret))
    mac.Write(body)
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(body))
    if err != nil { return fmt.Errorf("create request: %w", err) }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-Webhook-Signature", hex.EncodeToString(mac.Sum(nil)))
    resp, err := c.httpClient.Do(req)
    if err != nil { return fmt.Errorf("send: %w", err) }
    defer resp.Body.Close()
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return fmt.Errorf("upstream returned %d", resp.StatusCode)
    }
    return nil
}
```

### Notify handler pattern (stripe/wechat/alipay)

```go
// after verify + GetPaymentByOrderID + MarkPaymentPaid (CAS) + provider ack:
t, err := h.store.GetTenantByID(payment.TenantID)
if err != nil || t == nil || !t.IsActive {
    h.logger.Warn("notify: tenant missing or inactive, skipping callback",
        "channel", "stripe", "order_id", orderID, "tenant_id", payment.TenantID)
    return // payment is paid, callback_status stays pending; compensate worker will also bail
}
target := notify.CallbackTarget{URL: t.CallbackURL, Secret: t.CallbackSecret}
if err := h.callback.Send(cbCtx, target, payload); err != nil {
    h.store.IncrCallbackRetries(orderID)
    return
}
h.store.MarkCallbackSuccess(orderID)
```

### Compensate worker

```go
// services/payserver/internal/compensate/compensate.go
for _, p := range rows {
    t, err := w.store.GetTenantByID(p.TenantID)
    if err != nil || t == nil || !t.IsActive {
        w.logger.Warn("compensate: tenant gone or inactive, marking failed",
            "payment_id", p.ID, "tenant_id", p.TenantID)
        w.store.MarkCallbackFailed(p.OrderID)
        continue
    }
    target := notify.CallbackTarget{URL: t.CallbackURL, Secret: t.CallbackSecret}
    if err := w.callback.Send(ctx, target, payload); err != nil {
        w.store.IncrCallbackRetries(p.OrderID)
        continue
    }
    w.store.MarkCallbackSuccess(p.OrderID)
}
```

### Notify cannot recover tenant from provider event alone

Stripe webhook payload doesn't carry tenant info. We look the payment up by
`order_id` (still globally UNIQUE), read `payment.tenant_id`, then resolve
the tenant. Operator convention: tenants use UUID order IDs so cross-tenant
collision is impossible in practice.

### Provider webhook routing — design intent, not a limitation

There is exactly **one** Stripe account, **one** wechat MCH, and **one**
alipay app at the platform level — this is intentional and permanent.
Tenants do not bring their own payment credentials; this platform is the
merchant of record for every payment, across every business that calls in.

What this means for the webhook path:

- Stripe sends *all* `checkout.session.completed` events to the single
  `/notify/stripe` endpoint, regardless of which tenant originated the
  order. The handler reads `ClientReferenceID` (= the compact order_id we
  put there at create time), loads the `payments` row, reads its
  `tenant_id`, and posts the `DeliveryPayload` to that tenant's
  `callback_url`. The Stripe → tenant mapping is therefore indirect (via
  the `payments` row) — by design — not a v1 limitation.
- wechat and alipay are identical: one global merchant identity, one
  `/notify/{wechat,alipay}` endpoint, per-row tenant lookup determines
  the callback destination.

There is no scenario in which a tenant needs to "see" Stripe events
directly. The platform is the gateway; tenants only see normalized
`DeliveryPayload` callbacks. This is the entire reason `payserver`
exists — to abstract away every channel's idiosyncrasies behind one
HMAC-signed callback envelope.

## §5 — Admin API + OIDC

### Routes

```
/healthz                              public
/payments                             tenant bearer middleware
/notify/{wechat,alipay,stripe}        provider native sig verification
/admin/login                          OIDC code-grant entrypoint (302)
/admin/callback                       OIDC redirect target → set cookie, 302 /admin/tenants
/admin/logout                         clear cookie → 302 /admin/login
/admin/whoami                         OIDC session cookie middleware
/admin/tenants                        GET list, POST create
/admin/tenants/{id}                   GET, PATCH, DELETE
/admin/tenants/{id}/rotate-secret     POST
/admin/payments                       GET list (?tenant_id, ?status, ?channel, page/per_page)
/admin/payments/{id}                  GET detail (includes raw_notify)
/admin/                               SPA index.html for any unmatched /admin/* path
/admin/static/*                       embedded JS/CSS/asset bundle
```

### OIDC config

```go
type OIDCConfig struct {
    IssuerURL     string   `yaml:"issuer_url"`
    ClientID      string   `yaml:"client_id"`
    ClientSecret  string   `yaml:"client_secret"`
    RedirectURL   string   `yaml:"redirect_url"`
    Scopes        []string `yaml:"scopes"`         // default ["openid","profile","email"]
    AllowedEmails []string `yaml:"allowed_emails"` // empty = allow any OIDC-validated user
    SessionSecret string   `yaml:"session_secret"` // 32+ random bytes for cookie HMAC
}
```

Env: `PAYSERVER_OIDC_ISSUER_URL`, `PAYSERVER_OIDC_CLIENT_ID`,
`PAYSERVER_OIDC_CLIENT_SECRET`, `PAYSERVER_OIDC_REDIRECT_URL`,
`PAYSERVER_OIDC_SESSION_SECRET`, `PAYSERVER_OIDC_ALLOWED_EMAILS` (comma-
separated).

### Session

Cookie name `payserver_admin_session`, HttpOnly + Secure + SameSite=Lax.
Body: HMAC-signed `{email, name, exp}` (JSON, base64). 24h expiry. No
refresh — re-login required after expiry. SessionSecret signs/verifies.

### Tenant CRUD handlers

```go
// POST /admin/tenants
// body: { name, callback_url, callback_secret, description }
// response 201: { tenant: {...minus secret_hash...}, secret: "<cleartext-once>" }
// 409 on duplicate name
func handleCreateTenant(st *store.Store) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        var body struct {
            Name           string `json:"name"`
            CallbackURL    string `json:"callback_url"`
            CallbackSecret string `json:"callback_secret"`
            Description    string `json:"description"`
        }
        if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
            writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"}); return
        }
        if body.Name == "" {
            writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"}); return
        }
        if body.CallbackURL != "" {
            if err := validateReturnURL(body.CallbackURL); err != nil {
                writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()}); return
            }
        }
        secret, err := tenant.GenerateSecret()
        if err != nil { writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"}); return }
        hash, _ := tenant.HashSecret(secret)
        t := &tenant.Tenant{
            Name: body.Name, SecretHash: hash,
            CallbackURL: body.CallbackURL, CallbackSecret: body.CallbackSecret,
            Description: body.Description, IsActive: true,
        }
        if err := st.CreateTenant(t); err != nil {
            writeJSON(w, http.StatusConflict, map[string]string{"error": "name already exists"}); return
        }
        writeJSON(w, http.StatusCreated, map[string]any{"tenant": t, "secret": secret})
    }
}
```

Other handlers follow the same shape:
- `GET /admin/tenants` — list (paginated, sorted by created_at DESC)
- `GET /admin/tenants/{id}` — single
- `PATCH /admin/tenants/{id}` — allows `callback_url`, `callback_secret`,
  `description`, `is_active` only. `name` is immutable. `secret_hash` is
  immutable through PATCH (use rotate-secret).
- `POST /admin/tenants/{id}/rotate-secret` — generates fresh secret +
  hash, UPDATE, responds with `{secret: "<new-cleartext>"}`. Old secret
  fails immediately on the next request.
- `DELETE /admin/tenants/{id}` — FK is RESTRICT; on violation respond 409
  `{"error": "tenant has N payments; deactivate via PATCH is_active=false
  instead"}`.
- `validateReturnURL` (already in `services/payserver/internal/server/handler.go`
  from PR #44 — http/https scheme, no userinfo, ≤2048 chars) is reused to
  validate `callback_url`.

### Payments list

```
GET /admin/payments
  ?tenant_id=<uuid>     optional
  ?status=paid          optional (paid|pending|failed)
  ?channel=stripe       optional
  ?page=1 ?per_page=50  default 1/50, max 200
→ 200 { items: [...full payment rows including raw_notify...],
        meta: { total, page, per_page } }

GET /admin/payments/{id}
→ 200 { payment: {...} }
```

Ordered by `created_at DESC`. raw_notify is included verbatim for ops
debugging (Stripe webhook payloads etc.).

## §6 — Admin Frontend

### Workspace

```
services/payserver/admin/
├── package.json
├── pnpm-lock.yaml
├── vite.config.ts            # base "/admin/", dev port 5174
├── tsconfig.json
├── index.html
└── src/
    ├── main.tsx, App.tsx
    ├── api/
    │   ├── client.ts         # fetch wrapper. 401 → window.location = "/admin/login"
    │   ├── tenants.ts        # React Query hooks
    │   └── payments.ts
    ├── pages/
    │   ├── TenantsPage.tsx
    │   ├── TenantDetailPage.tsx
    │   └── PaymentsPage.tsx
    ├── components/
    │   ├── AppShell.tsx      # nav: Tenants | Payments; top-right user email + Logout
    │   ├── SecretRevealOnce.tsx
    │   └── ui/...            # shadcn copy-in
    └── lib/utils.ts
```

Standalone — no shared code with modelserver dashboard. Same stack, same
shadcn primitives, no cross-repo import.

### Routing

```tsx
<BrowserRouter basename="/admin">
  <Routes>
    <Route element={<AppShell />}>
      <Route index element={<Navigate to="/tenants" replace />} />
      <Route path="tenants" element={<TenantsPage />} />
      <Route path="tenants/:id" element={<TenantDetailPage />} />
      <Route path="payments" element={<PaymentsPage />} />
    </Route>
  </Routes>
</BrowserRouter>
```

App bootstrap calls `GET /admin/whoami` — on 200 mount AppShell, on 401
redirect to `/admin/login` (server handles OIDC).

### Pages

**TenantsPage**
- Header: `+ New Tenant` button.
- Table columns: Name · Callback URL · Status (Active/Inactive badge) ·
  Created · Actions menu (Edit / Rotate Secret / Deactivate / Delete).
- New Tenant Dialog: form (name, callback_url, callback_secret,
  description) → POST → on 201 show `SecretRevealOnce` with the returned
  secret.
- Rotate Secret: confirmation dialog → POST → `SecretRevealOnce`.
- Delete: try DELETE; on 409 show inline message with "Deactivate"
  shortcut (PATCH `is_active=false`).

**TenantDetailPage** (`/tenants/:id`)
- Top read-only card: id, name, created_at, status.
- Editable form: callback_url, callback_secret, description, is_active.
- "Save" → PATCH.
- Link "View this tenant's payments" → `/payments?tenant_id={id}`.

**PaymentsPage**
- Filter bar: Tenant dropdown (from cached tenants list) / Status select /
  Channel select / optional date range.
- Table: Created · Order ID · Tenant (joined from cache) · Channel ·
  Amount (channel-aware: USD/CNY by currency field) · Status · Callback
  Status · Retries.
- Click row → Detail Dialog: full payment fields + `raw_notify`
  pretty-printed JSON.
- No retry button in v1.

### SecretRevealOnce

`<Dialog>` with cleartext secret in a `<pre>` block, `Copy` button + `I've
saved it` button. The acknowledgment button is `disabled` until copy is
clicked, to prevent accidental dismissal.

### Hosting

```go
// services/payserver/cmd/payserver/main.go
//go:embed admin_dist
var adminDist embed.FS

// routes.go:
adminSubFS, _ := fs.Sub(adminDist, "admin_dist")
r.Get("/admin/static/*", http.StripPrefix("/admin/", http.FileServer(http.FS(adminSubFS))).ServeHTTP)
r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
    http.ServeFileFS(w, r, adminSubFS, "index.html")
})
r.Get("/admin/*", func(w http.ResponseWriter, r *http.Request) {
    // SPA fallback for client-side routes
    http.ServeFileFS(w, r, adminSubFS, "index.html")
})
```

Build pipeline (Makefile target `make all`):
```
cd services/payserver/admin && pnpm install --frozen-lockfile && pnpm build
rm -rf services/payserver/admin_dist
cp -r services/payserver/admin/dist services/payserver/admin_dist
cd services/payserver && go build ./cmd/payserver
```

The `admin_dist/` directory is build output; add to `.gitignore`:

```
# services/payserver/.gitignore
admin/node_modules/
admin/dist/
admin_dist/
```

So that `//go:embed admin_dist` reads what the local build just produced.
Initial commit needs a placeholder `admin_dist/.gitkeep` plus a stub
`admin_dist/index.html` (empty `<html>`) so `go build` doesn't fail on
fresh checkouts where `pnpm build` hasn't run yet — the Makefile
overwrites both during the real build.

Dockerfile multi-stage: node:20 builds frontend (produces `dist/`),
golang:1.26 stage copies that `dist/` into `admin_dist/` and runs
`go build`.

## §7 — Testing & Out of Scope

### Tests

| Layer | File | Scenarios |
|---|---|---|
| Migration | `store/migrations_002_test.go` | tenants table exists; default tenant present with matching secret_hash; all payments have non-null tenant_id pointing at default; idx_payments_order_id UNIQUE preserved; FK RESTRICT blocks default deletion |
| Tenant store | `store/tenants_test.go` | CreateTenant (unique violation); GetTenantByID hit + miss; GetTenantByName; ListTenants paginated; UpdateTenant whitelist; RotateSecret; DeleteTenant blocked by payments |
| Tenant crypto | `tenant/tenant_test.go` | GenerateSecret length / non-repeating; HashSecret roundtrip; VerifySecret rejects wrong secret |
| Auth middleware | `server/auth_test.go` | missing Bearer → 401; malformed token → 401; unknown tenant_id → 401 (still runs bcrypt for timing parity); inactive tenant → 401; correct token → ctx carries tenant |
| handleCreatePayment | `server/handler_test.go` | payment row carries tenant_id; same order_id across two tenants → 409 (global UNIQUE) |
| Callback flow | `notify/{stripe,wechat,alipay}_test.go` | webhook reads payment.tenant_id → resolves tenant → CallbackClient.Send receives correct URL+secret; inactive tenant → callback skipped + warn log |
| Compensate | `compensate/compensate_test.go` | pending rows resolved per-tenant; tenant deleted/inactive → MarkCallbackFailed immediately |
| Admin API | `server/admin_handler_test.go` | POST: server-generated secret; name required; name duplicate 409; secret in response only once. Rotate: new cleartext returned, old secret fails. DELETE blocked by payments. PATCH name field ignored. validateReturnURL applied to callback_url |
| OIDC | `server/oidc_test.go` | missing/expired session → 401 (JSON) or 302 (HTML) based on Accept header; AllowedEmails enforcement |
| Frontend | `pnpm build` (no React unit harness) | type-clean build; manual smoke checklist below |

### Manual smoke checklist (for PR runbook)

1. Set OIDC env + `PAYSERVER_DEFAULT_TENANT_SECRET`; deploy + restart.
2. Logs show `migration 002 applied` and `default tenant id=<uuid>`.
3. modelserver Bearer header updated to `<uuid>:<secret>`. Place a wechat
   order → callback succeeds (regression-free).
4. OIDC login at `/admin/login` → land on `/admin/tenants` → default
   tenant visible.
5. Create tenant "test" → secret displayed once → copy.
6. `curl -H "Authorization: Bearer test-id:test-secret" -X POST .../payments`
   with a Stripe order → callback delivered to test tenant's callback_url.
7. Rotate "test" secret → old fails, new works.
8. Deactivate "test" → new orders 401; in-flight webhook arrives → log
   `notify: tenant missing or inactive, skipping callback`; no callback
   sent.
9. Delete default → 409; delete an empty fresh tenant → 204.

### Deployment Order

1. Deploy new payserver (migration 002 runs with default tenant bootstrap).
2. Capture default tenant id from logs.
3. Update modelserver `MODELSERVER_BILLING_PAYMENT_API_KEY=<id>:<secret>`.
4. Restart modelserver.

During the window between step 1 and step 4, modelserver requests with the
old API-key format are rejected (401). Subscription ordering is a low-
frequency operation; the window is acceptable. If unacceptable, add a
short-lived shim that accepts the old key and remaps to default tenant
(not in this spec).

### Out of Scope (Future Work)

- **Per-tenant provider credentials** are explicitly **not** a platform
  goal. payserver is the merchant of record for every payment across
  every tenant; one Stripe account, one wechat MCH, one alipay app
  forever. Removing this from "future work" means we never need
  per-call Stripe client init, never need tenant routing on the inbound
  webhook surface, and the operations team only ever reconciles three
  upstream accounts. Should that change at the business level, it is a
  different platform, not a v2 of this one.
- **Tenant self-service portal**. UI is admin-only.
- **Dashboard overview page** (today's volume, channel split, retry
  backlog) — third page, YAGNI for v1.
- **"Retry callback now" button** on PaymentsPage. compensate worker
  auto-retry is sufficient; manual retry is escape hatch only.
- **Audit log** (who changed which tenant's callback). OIDC + git logs
  suffice as cross-evidence at this scale.
- **Callback-secret graceful overlap** (accept old + new during rotation
  window). Current rotation is hard cut; failed callbacks are compensated.
- **Per-tenant rate limiting**. All tenants are trusted internal products
  in v1.
- **payserver split to its own repo**. Independent project.

### Risks

- **bcrypt cost per request**: every `POST /payments` runs bcrypt
  (cost=10, ~50ms). Payment creation is low-frequency, but if multi-tenant
  call volume rises, a small in-memory verified-token cache (LRU,
  60-second TTL) is the right next step. Out of scope for v1.
- **Order-id global uniqueness convention**: `payments.order_id` is
  globally UNIQUE across all tenants. Every onboarding tenant is told
  (in the admin UI "New Tenant" dialog help text + the API integration
  doc) to use UUIDs as order IDs — collision-free in practice.
  Cross-tenant reuse returns 409 with a message that names the conflict
  ("order_id already used by tenant=<name>") so debugging is fast.
- **Default tenant bootstrap secret leakage**: the env value lands in
  process env + (one time) in a Postgres SET LOCAL statement inside the
  migration transaction. SET LOCAL values are not persisted; PG audit
  logs may capture them. Operator should rotate the secret via admin UI
  shortly after first deploy if logging is sensitive.
- **OIDC misconfiguration locks out admin** — addressed by the rescue
  path below, not deferred.

### OIDC rescue path

If the OIDC issuer is down, the client secret rotates without notice, or
`allowed_emails` is misconfigured and excludes everyone with hands on the
console, admin UI becomes unreachable. The rescue is a CLI subcommand
that bypasses OIDC and prints a one-shot session token:

```bash
$ payserver admin rescue --email opsuser@example.com
issued rescue session, expires in 1h:
   payserver_admin_session=<base64-signed-cookie>
   set this cookie on the /admin/* domain to bypass OIDC.
```

The subcommand reads `PAYSERVER_OIDC_SESSION_SECRET` from the same env
that signs normal cookies — so the cookie is verifiable by the running
binary without any code-path branch in production. It writes an audit
log line `rescue session issued for=<email> ttl=1h pid=...` to stderr
and the structured logger.

This subcommand is only callable by a process that has shell access on
the payserver host (i.e. ops via SSH or `kubectl exec`). It has the same
security envelope as the host itself.

Implementation: a `cmd/payserver` subcommand parser around the existing
`flag.Parse()` — if `os.Args[1] == "admin" && os.Args[2] == "rescue"`,
branch into rescue path; otherwise fall through to the server. About 40
lines of code, no new dependencies (re-uses the OIDC session-signing
helper).
