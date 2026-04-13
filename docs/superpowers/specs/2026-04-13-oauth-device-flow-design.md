# OAuth Device Flow Design

**Date:** 2026-04-13
**Status:** Approved

## Problem

CLI tools (e.g., Claude Code, custom scripts) need to authenticate against modelserver's proxy API to make AI model inference calls. The current OAuth2 authorization code flow requires browser redirects, which is inconvenient for CLI environments that cannot easily handle redirect URIs. OAuth 2.0 Device Authorization Grant (RFC 8628) solves this by letting the user authenticate in a separate browser while the CLI polls for tokens.

## Use Case

A CLI tool wants to obtain a project-scoped access token for calling the proxy API. The user opens a browser, enters a short code, logs in, selects a project, and the CLI receives Hydra-issued tokens that work with the existing proxy auth middleware.

## Architecture

### Core Principle

modelserver manages the device code lifecycle (creation, verification, polling). Hydra manages the token lifecycle (issuance, introspection, refresh, revocation). The device flow bridges into Hydra's standard authorization code flow — the user goes through the same login + consent experience. The proxy auth middleware requires zero changes.

### Configuration

Add to `HydraConfig`:

```go
type HydraConfig struct {
    AdminURL   string           `yaml:"admin_url"    mapstructure:"admin_url"`
    PublicURL  string           `yaml:"public_url"   mapstructure:"public_url"`
    DeviceFlow DeviceFlowConfig `yaml:"device_flow"  mapstructure:"device_flow"`
}

type DeviceFlowConfig struct {
    ClientID     string `yaml:"client_id"      mapstructure:"client_id"`
    ClientSecret string `yaml:"client_secret"  mapstructure:"client_secret"`
    CodeTTL      int    `yaml:"code_ttl"       mapstructure:"code_ttl"`       // default 600s
    PollInterval int    `yaml:"poll_interval"  mapstructure:"poll_interval"`  // default 5s
}
```

Environment variables:
- `HYDRA_PUBLIC_URL` → `auth.oauth.hydra.public_url`
- `MODELSERVER_AUTH_OAUTH_HYDRA_DEVICE_FLOW_CLIENT_ID`
- `MODELSERVER_AUTH_OAUTH_HYDRA_DEVICE_FLOW_CLIENT_SECRET`
- `MODELSERVER_AUTH_OAUTH_HYDRA_DEVICE_FLOW_CODE_TTL`
- `MODELSERVER_AUTH_OAUTH_HYDRA_DEVICE_FLOW_POLL_INTERVAL`

### Hydra Client Registration

A dedicated device flow OAuth client registered in Hydra:

```json
{
  "client_id": "<configured>",
  "client_secret": "<configured>",
  "redirect_uris": ["https://<modelserver>/oauth/device/callback"],
  "grant_types": ["authorization_code", "refresh_token"],
  "response_types": ["code"],
  "scope": "project:inference offline_access",
  "token_endpoint_auth_method": "client_secret_post"
}
```

Provided via `scripts/register-device-flow-client.sh`.

## Database Schema

Migration `007_device_codes.sql`:

```sql
CREATE TABLE device_codes (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_code        TEXT NOT NULL UNIQUE,
    user_code          TEXT NOT NULL UNIQUE,
    client_id          TEXT NOT NULL DEFAULT '',
    scopes             TEXT[] NOT NULL DEFAULT '{}',
    status             TEXT NOT NULL DEFAULT 'pending',
    verification_nonce TEXT NOT NULL UNIQUE,
    access_token       BYTEA,
    refresh_token      BYTEA,
    token_type         TEXT NOT NULL DEFAULT '',
    token_expires_in   INT NOT NULL DEFAULT 0,
    expires_at         TIMESTAMPTZ NOT NULL,
    poll_interval      INT NOT NULL DEFAULT 5,
    last_polled_at     TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_device_codes_user_code ON device_codes(user_code) WHERE status = 'pending';
CREATE INDEX idx_device_codes_device_code ON device_codes(device_code);
```

### Field Details

| Field | Description |
|-------|-------------|
| `device_code` | 64 hex chars (32 bytes crypto/rand). CLI uses this to poll. |
| `user_code` | 8 consonant chars formatted as `XXXX-XXXX`. Charset: `BCDFGHJKLMNPQRSTVWXZ` (no vowels, no ambiguous letters). User types this in browser. |
| `client_id` | The requesting CLI tool's self-reported identifier. For audit/display. |
| `scopes` | Requested OAuth scopes. |
| `status` | `pending` → `approved` / `denied` / `expired` |
| `verification_nonce` | Random nonce used as Hydra's `state` parameter. Prevents CSRF on the callback. |
| `access_token` | Encrypted (AES via `crypto.Encrypt`) Hydra access token. Stored after successful exchange. |
| `refresh_token` | Encrypted Hydra refresh token. |
| `token_expires_in` | Token validity in seconds (from Hydra response). |
| `expires_at` | When this device code expires (`created_at + code_ttl`). |
| `poll_interval` | Recommended poll interval in seconds. Increases on `slow_down`. |
| `last_polled_at` | Updated on each poll. Used for slow_down detection. |

## API Endpoints

All endpoints are **public** (no JWT auth required), mounted alongside existing Hydra endpoints.

### 1. `POST /oauth/device/code` — Device Authorization Request

Creates a new device code + user code pair.

**Request:**
```json
{
  "client_id": "my-cli-tool",
  "scope": "project:inference offline_access"
}
```

**Response (200):**
```json
{
  "device_code": "a1b2c3d4e5f6...64hexchars",
  "user_code": "BCDF-GHJK",
  "verification_uri": "https://codeapi.example.com/oauth/device",
  "verification_uri_complete": "https://codeapi.example.com/oauth/device?user_code=BCDF-GHJK",
  "expires_in": 600,
  "interval": 5
}
```

**Logic:**
1. Generate device_code (32 bytes hex), user_code (8 consonants), verification_nonce (16 bytes hex).
2. Insert into `device_codes` table with `status='pending'`, `expires_at = now + code_ttl`.
3. Return RFC 8628-compliant response.

### 2. `GET /oauth/device` — Verification Page

Renders an HTML page with a text input for the user code. If `?user_code=XXXX-XXXX` is in the query string, auto-fills the input.

### 3. `POST /oauth/device` — Submit User Code

**Form data:** `user_code=BCDF-GHJK`

**Logic:**
1. Normalize the user code (strip dashes, uppercase).
2. Look up in `device_codes` where `status='pending'` and `expires_at > now`.
3. If not found: re-render the form with an error message.
4. If found: redirect the browser to Hydra's authorization endpoint:

```
302 → {hydra_public_url}/oauth2/auth
  ?response_type=code
  &client_id={device_flow_client_id}
  &redirect_uri={base_url}/oauth/device/callback
  &scope={device_code.scopes joined by space}
  &state={device_code.verification_nonce}
```

The user then goes through the normal Hydra login + consent flow (same as auth code flow).

### 4. `GET /oauth/device/callback` — Hydra Callback

Hydra redirects here after the user completes login + consent.

**Query params:** `code=AUTH_CODE&state=NONCE` or `error=...&state=NONCE`

**Logic:**
1. Look up device_code by `verification_nonce = state`.
2. If `error` param present:
   - Update `status = 'denied'`.
   - Render error page.
3. Exchange auth code for tokens via Hydra token endpoint:
   ```
   POST {hydra_public_url}/oauth2/token
   Content-Type: application/x-www-form-urlencoded

   grant_type=authorization_code
   &code=AUTH_CODE
   &client_id={device_flow_client_id}
   &client_secret={device_flow_client_secret}
   &redirect_uri={base_url}/oauth/device/callback
   ```
4. Encrypt access_token and refresh_token using `crypto.Encrypt(encKey, ...)`.
5. Update device_codes row:
   - `status = 'approved'`
   - `access_token`, `refresh_token`, `token_type`, `token_expires_in`
6. Record the OAuth grant (same as consent handler does currently).
7. Render "Authorization successful. You may close this window." page.

### 5. `POST /oauth/device/token` — Token Polling

CLI polls this endpoint.

**Request:**
```json
{
  "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
  "device_code": "a1b2c3d4e5f6...64hexchars",
  "client_id": "my-cli-tool"
}
```

**Response logic:**

| Condition | HTTP Status | Response |
|-----------|-------------|----------|
| device_code not found | 400 | `{"error": "invalid_grant"}` |
| `expires_at` passed | 400 | `{"error": "expired_token"}` |
| `status = 'denied'` | 400 | `{"error": "access_denied"}` |
| `status = 'pending'`, poll too fast | 400 | `{"error": "slow_down"}` |
| `status = 'pending'` | 400 | `{"error": "authorization_pending"}` |
| `status = 'approved'` | 200 | Token response (see below) |

**Approved response (200):**
```json
{
  "access_token": "hydra-issued-access-token",
  "refresh_token": "hydra-issued-refresh-token",
  "token_type": "bearer",
  "expires_in": 900
}
```

**After returning tokens:** Delete the device_codes row (or mark as consumed) to prevent replay.

**slow_down logic:** If `now - last_polled_at < poll_interval`, return `slow_down` and increment `poll_interval` by 5 seconds. Always update `last_polled_at`.

## Flow Diagram

```
CLI                     modelserver                    Hydra              Browser
 │                           │                           │                    │
 │─POST /oauth/device/code──>│                           │                    │
 │  {client_id, scope}       │                           │                    │
 │                           │ gen device_code, user_code │                    │
 │                           │ insert into device_codes   │                    │
 │<──{device_code,user_code} │                           │                    │
 │   verification_uri        │                           │                    │
 │                           │                           │                    │
 │  CLI displays:            │                           │                    │
 │  "Visit: https://xxx/     │                           │                    │
 │   oauth/device            │                           │                    │
 │   Enter code: BCDF-GHJK" │                           │                    │
 │                           │                           │                    │
 │                           │<─GET /oauth/device────────│<────user opens─────│
 │                           │──render user_code form───>│                    │
 │                           │<─POST /oauth/device───────│  user enters code  │
 │                           │  validate user_code       │                    │
 │                           │                           │                    │
 │                           │──302 → /oauth2/auth──────>│                    │
 │                           │  (standard Hydra auth)    │                    │
 │                           │                           │                    │
 │                           │         [existing Hydra login + consent flow]  │
 │                           │         user logs in → selects project → grants│
 │                           │                           │                    │
 │                           │<─/oauth/device/callback───│                    │
 │                           │  ?code=AUTH_CODE&state=.. │                    │
 │                           │                           │                    │
 │                           │─POST /oauth2/token───────>│                    │
 │                           │  grant_type=auth_code     │                    │
 │                           │  code + client_secret     │                    │
 │                           │<─access_token, refresh────│                    │
 │                           │                           │                    │
 │                           │  encrypt + store tokens   │                    │
 │                           │──render "Success" page───>│                    │
 │                           │                           │                    │
 │─POST /oauth/device/token─>│                           │                    │
 │  {device_code, grant_type}│                           │                    │
 │                           │  lookup → status=approved │                    │
 │                           │  decrypt tokens           │                    │
 │                           │  delete device_code row   │                    │
 │<──{access_token, refresh} │                           │                    │
```

## Security

1. **High-entropy device_code:** 32 bytes crypto/rand (64 hex chars). Brute-force infeasible.
2. **Short user_code with limited window:** 8 consonants (~3.4 billion combinations) with 10-minute expiry. Acceptable given the short window.
3. **CSRF protection:** `verification_nonce` is used as Hydra's `state` parameter and verified on callback.
4. **Encrypted token storage:** Tokens stored using AES encryption (`crypto.Encrypt`), consistent with existing upstream token storage.
5. **One-time token retrieval:** Tokens are deleted from DB after the CLI retrieves them. Prevents replay.
6. **Expired code cleanup:** Background goroutine (or lazy cleanup on poll) deletes expired device_codes.
7. **Rate limiting:** `slow_down` mechanism per RFC 8628 prevents polling abuse.

## File Structure

```
internal/admin/
  device_flow.go              — DeviceFlowHandler (all 5 endpoints)
  device_flow_test.go         — Unit tests
  templates/
    device_verify.html        — User code input page
    device_success.html       — "Authorization successful" page
    device_error.html         — Error page
internal/config/
  config.go                   — Add HydraConfig.PublicURL, DeviceFlowConfig
internal/store/
  device_codes.go             — DB CRUD for device_codes table
  migrations/
    007_device_codes.sql      — Create device_codes table
scripts/
  register-device-flow-client.sh — Register dedicated Hydra client
```

## Route Mounting

In `internal/admin/routes.go`:

```go
// Inside the existing hydraClient != nil block:
if cfg.Auth.OAuth.Hydra.DeviceFlow.ClientID != "" {
    deviceHandler := NewDeviceFlowHandler(hydraClient, st, encKey, cfg)
    r.Post("/oauth/device/code", deviceHandler.HandleDeviceAuthorize)
    r.Get("/oauth/device", deviceHandler.HandleVerificationPage)
    r.Post("/oauth/device", deviceHandler.HandleVerifyUserCode)
    r.Get("/oauth/device/callback", deviceHandler.HandleCallback)
    r.Post("/oauth/device/token", deviceHandler.HandleTokenPoll)
}
```

## What Does NOT Change

- **Proxy auth middleware** — Hydra tokens work via existing introspection path
- **Existing login/consent handlers** — Reused as-is by the Hydra redirect
- **OAuth grants table** — Device flow callback creates grants the same way consent does
- **Token refresh** — Clients refresh tokens directly with Hydra (standard OAuth2 refresh)
