# Upstream Codex (ChatGPT subscription) OAuth Support

**Date:** 2026-04-24
**Status:** Draft

## Problem

modelserver currently has two ways to talk to OpenAI Responses API upstreams:

1. `openai` provider — uses an `sk-...` API key, base URL `https://api.openai.com/v1`.
2. `vertex-openai` provider — uses Vertex AI's OpenAI-compatible endpoint with GCP service-account auth.

There is no way to consume the **ChatGPT subscription** Codex backend
(`https://chatgpt.com/backend-api/codex/responses`). That endpoint is what the
official `codex` CLI uses when the user signs in with their ChatGPT account, and
is the cheapest path for users who already pay for a Plus / Pro / Team
subscription. It is the OpenAI-side equivalent of the existing `claudecode`
provider on the Anthropic side.

## Goal

Add a new `codex` upstream provider that:

- Authenticates against ChatGPT via OAuth Authorization Code + PKCE (the same
  flow used by the codex CLI), persists the resulting credentials JSON in the
  existing `upstreams.api_key_encrypted` blob, and auto-refreshes tokens via a
  manager parallel to `OAuthTokenManager`.
- Proxies wire-compatible OpenAI Responses requests (`POST /responses`) to
  `https://chatgpt.com/backend-api/codex` with the headers the ChatGPT backend
  requires (`Authorization`, `ChatGPT-Account-ID`, `OpenAI-Beta`, `originator`,
  `version`, `session_id`, codex-flavored `User-Agent`).
- Recovers from upstream-side token revocation by force-refreshing once on
  401/403, mirroring the claudecode behaviour in `executor.go`.
- Surfaces a complete authorize → paste-callback-URL → exchange UI in the
  Upstreams admin page, together with token-status and "Re-authorize" controls.
- Exposes a per-upstream utilization endpoint for ChatGPT-subscription quota,
  identical in shape to the existing claudecode utilization view.

The implementation **mirrors `claudecode`** intentionally: file names, function
names, route paths, and UI components have a 1-for-1 codex sibling. This keeps
the two OAuth-subscription providers easy to reason about and easy to grep.

## Non-Goals

- **Bare OpenAI API-key mode for codex.** Sending an `sk-...` key to
  `api.openai.com/v1/responses` is already covered by the existing `openai`
  provider; the codex provider is exclusively for ChatGPT-subscription auth.
- **Refactoring the existing `OAuthTokenManager`** to be provider-agnostic.
  Codex credentials carry an extra `chatgpt_account_id` (parsed from the OIDC
  `id_token`), and the refresh response shape differs slightly. A separate
  `CodexOAuthTokenManager` keeps the diff small; consolidation can come later.
- **A local-loopback OAuth callback server.** modelserver typically runs in a
  cloud / container environment where binding `localhost:1455` is unreachable.
  We reuse the manual paste-callback UX from claudecode.
- **Codex-specific request body transforms.** The wire format is stock OpenAI
  Responses API; the existing `OpenAITransformer` body/parser/stream paths work
  unchanged. Only `SetUpstream` is codex-specific.

## High-Level Architecture

```
                      ┌──────────────────────────────────────┐
client (POST /v1/responses)                                  │
                      │                                      │
                      ▼                                      │
              ┌───────────────┐                              │
              │ openai_handler│ (existing)                   │
              └───────┬───────┘                              │
                      │                                      │
                      ▼                                      │
              ┌───────────────┐    Match by model →          │
              │   Executor    │    pick codex upstream       │
              └───────┬───────┘                              │
                      │                                      │
                      ▼                                      │
        ┌─────────────────────────┐                          │
        │ CodexTransformer        │                          │
        │  TransformBody (no-op)  │                          │
        │  SetUpstream:           │                          │
        │   - GetCodexAccessToken │── CodexOAuthTokenManager │
        │   - GetCodexAccountID   │   (refresh, single-      │
        │   - inject codex headers│    flight, persist)      │
        │  WrapStream  (OpenAI)   │                          │
        │  ParseResponse (OpenAI) │                          │
        └────────────┬────────────┘                          │
                     │                                       │
                     ▼                                       │
   chatgpt.com/backend-api/codex/responses (Bearer + acct)   │
                                                             │
   Admin (token lifecycle): /api/v1/upstreams/codex/oauth/{start,exchange},
                            /api/v1/upstreams/{id}/codex/oauth/{status,refresh},
                            /api/v1/upstreams/{id}/codex/utilization
```

## Components

### Provider constant

`internal/types/upstream.go` adds:

```go
const ProviderCodex = "codex"
```

### CodexTransformer (`internal/proxy/provider_codex.go`)

```go
type CodexTransformer struct{}

func (t *CodexTransformer) TransformBody(body []byte, _ string, _ bool, _ http.Header) ([]byte, error) {
    // Same as OpenAITransformer: pass-through.
    return body, nil
}

func (t *CodexTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
    // apiKey is either a raw access_token (resolved by Executor via
    // CodexOAuthTokenManager) or a raw credentials JSON (legacy fallback).
    accessToken := apiKey
    accountID := ""
    if len(apiKey) > 0 && apiKey[0] == '{' {
        accessToken, accountID = ParseCodexAccessTokenAndAccount(apiKey)
    }
    directorSetCodexUpstream(r, upstream.BaseURL, accessToken, accountID, upstream.ID)
    return nil
}

func (t *CodexTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
    // Identical to OpenAITransformer.WrapStream.
}

func (t *CodexTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
    // Identical to OpenAITransformer.ParseResponse.
}
```

Registered in `provider_transform.go`'s `init()`:

```go
providerTransformers[types.ProviderCodex] = &CodexTransformer{}
```

### Outbound director (`internal/proxy/codex.go`)

Mirrors `claudecode.go`:

```go
const (
    codexDefaultBaseURL = "https://chatgpt.com/backend-api/codex"
    // Pinned to a recent codex CLI release at implementation time. Bumping
    // these is a deliberate maintenance task (analogous to `fixedUserAgent`
    // in normalize_identity.go for claudecode); we do not chase upstream.
    codexUserAgent      = "codex_cli_rs/<version> (<os>; <arch>) Codex"
    codexOriginator     = "codex_cli_rs"
    codexVersion        = "<version>"
    codexOpenAIBeta     = "responses=experimental"
)

func directorSetCodexUpstream(req *http.Request, baseURL, accessToken, accountID, upstreamID string) {
    // 1. Set scheme/host/path from baseURL (default codexDefaultBaseURL).
    // 2. Bearer token, ChatGPT-Account-ID (only if non-empty).
    // 3. Strip x-api-key.
    // 4. Set OpenAI-Beta, originator, version, User-Agent (always overwrite).
    // 5. session_id: preserve client's value if present, else derive a fresh UUID.
    // 6. Defensive Connection: keep-alive.
}

// deriveCodexFallbackSessionID returns a random UUIDv4-style string for use
// when the client did not supply a Session-Id header. It is NOT derived from
// upstream.ID — that would make every request look like one giant session to
// the ChatGPT backend and risk being mistaken for a runaway session or having
// prompt-cache reuse fail.
func deriveCodexFallbackSessionID() string { ... }
```

### CodexCredentials & CodexOAuthTokenManager (`internal/proxy/codex_oauth.go`)

```go
const (
    CodexClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
    CodexIssuerURL   = "https://auth.openai.com"
    CodexAuthURL     = CodexIssuerURL + "/oauth/authorize"
    CodexTokenURL    = CodexIssuerURL + "/oauth/token"
    CodexScopes      = "openid profile email offline_access"
    codexExpiryBuffer = 300 // seconds
)

type CodexCredentials struct {
    IDToken           string `json:"id_token"`
    AccessToken       string `json:"access_token"`
    RefreshToken      string `json:"refresh_token"`
    ChatGPTAccountID  string `json:"chatgpt_account_id,omitempty"`
    ExpiresAt         int64  `json:"expires_at"`
    ClientID          string `json:"client_id,omitempty"`
}

type CodexOAuthTokenManager struct {
    // Same shape as OAuthTokenManager:
    //   credentials   map[string]*CodexCredentials  (keyed by upstream.ID)
    //   sfGroup       singleflight.Group
    //   store         *store.Store
    //   encryptionKey []byte
    //   httpClient    *http.Client
    //   tokenURL      string
}

func (m *CodexOAuthTokenManager) LoadCredentials(...)
func (m *CodexOAuthTokenManager) Reload(...)
func (m *CodexOAuthTokenManager) GetAccessToken(upstreamID string) (string, error)
func (m *CodexOAuthTokenManager) GetAccountID(upstreamID string) (string, error)
func (m *CodexOAuthTokenManager) ForceRefreshAccessToken(upstreamID string) (string, error)
func (m *CodexOAuthTokenManager) refreshToken(upstreamID string) error

func ParseCodexAccessTokenAndAccount(raw string) (accessToken, accountID string)
```

`refreshToken` POSTs JSON `{grant_type:"refresh_token", client_id, refresh_token, scope}` to
`CodexTokenURL`. The response includes new `access_token`, `id_token`,
`refresh_token`, `expires_in`. After a successful refresh, the manager:

1. Re-parses `chatgpt_account_id` from the new `id_token` if present (the
   account id is stable across refreshes; we only re-extract to keep the
   stored blob complete).
2. Persists the encrypted credentials JSON via `store.UpdateUpstream`.

OIDC parsing of the `id_token` extracts `chatgpt_account_id` from the
`https://api.openai.com/auth.chatgpt_account_id` claim. Implementation is a
minimal base64-decode of the JWT payload (no signature verification — the
token was just minted by the issuer we exchanged with, and we are only
extracting an opaque identifier).

### Router wiring (`internal/proxy/router_engine.go`)

Add a parallel field and threading:

```go
type Router struct {
    // ...existing fields...
    oauthMgr      *OAuthTokenManager
    codexOAuthMgr *CodexOAuthTokenManager   // NEW
}

func NewRouter(... codexMgr *CodexOAuthTokenManager ...) *Router

func (r *Router) GetCodexAccessToken(upstreamID string) (string, error)
func (r *Router) GetCodexAccountID(upstreamID string) (string, error)
func (r *Router) ForceRefreshCodexAccessToken(upstreamID string) (string, error)
```

`buildMaps` calls `codexOAuthMgr.LoadCredentials(...)` and `Reload(...)`
alongside the existing claudecode call.

### Executor wiring (`internal/proxy/executor.go`)

Two minimal additions, both mirroring the existing claudecode branches:

1. **Token resolution** before `transformer.SetUpstream`:

```go
apiKeyForUpstream := candidate.APIKey
switch upstream.Provider {
case types.ProviderClaudeCode:
    if token, err := e.router.GetClaudeCodeAccessToken(upstream.ID); err == nil {
        apiKeyForUpstream = token
    } else { ... }
case types.ProviderCodex:
    if token, err := e.router.GetCodexAccessToken(upstream.ID); err == nil {
        accountID, _ := e.router.GetCodexAccountID(upstream.ID)
        apiKeyForUpstream = encodeCodexAuthBlob(token, accountID) // tiny JSON the transformer can split
    } else { ... }
}
```

`encodeCodexAuthBlob` returns `{"access_token":"...","chatgpt_account_id":"..."}`
which `CodexTransformer.SetUpstream` already knows how to parse. This avoids
adding a new parameter to the `ProviderTransformer` interface.

2. **401/403 force-refresh-and-retry**, gated by `codexOAuthRetried bool`,
   parallel to `claudeCodeOAuthRetried`. After force-refresh succeeds, rebuild
   `outReq` with new `Authorization` and `ChatGPT-Account-ID` headers.

### Header sanitization (`sanitizeOutboundHeaders` in `executor.go`)

Add codex-related keys to the allowed list:

```go
canon == "Openai-Beta",
canon == "Chatgpt-Account-Id",
canon == "Originator",
canon == "Session-Id",
canon == "Version",
// Codex CLI fingerprint headers, if/when the client provides them.
strings.HasPrefix(canon, "X-Codex-")
```

### Admin endpoints (`internal/admin/handle_codex_oauth.go`, `routes.go`)

| Method | Path | Handler |
|--------|------|---------|
| POST | `/api/v1/upstreams/codex/oauth/start` | `handleCodexOAuthStart()` |
| POST | `/api/v1/upstreams/codex/oauth/exchange` | `handleCodexOAuthExchange()` |
| GET | `/api/v1/upstreams/{upstreamID}/codex/oauth/status` | `handleCodexTokenStatus(st, encKey)` |
| POST | `/api/v1/upstreams/{upstreamID}/codex/oauth/refresh` | `handleCodexTokenRefresh(st, encKey)` |
| GET | `/api/v1/upstreams/{upstreamID}/codex/utilization` | `handleCodexUtilization(st, encKey)` |

`handleCodexOAuthStart` returns:

```json
{
  "auth_url": "https://auth.openai.com/oauth/authorize?...",
  "state": "...",
  "code_verifier": "...",
  "redirect_uri": "http://localhost:1455/auth/callback"
}
```

The authorize URL params:

```
response_type=code
client_id=app_EMoamEEZ73f0CkXaXp7hrann
redirect_uri=http://localhost:1455/auth/callback
scope=openid profile email offline_access
code_challenge=<S256(verifier)>
code_challenge_method=S256
state=<random>
id_token_add_organizations=true
codex_cli_simplified_flow=true
originator=codex_cli_rs
```

`handleCodexOAuthExchange` posts form-urlencoded to `CodexTokenURL`:

```
grant_type=authorization_code
code=<from callback>
client_id=app_EMoamEEZ73f0CkXaXp7hrann
redirect_uri=<echoed>
code_verifier=<from start>
```

It then parses the returned `id_token` for `chatgpt_account_id` and returns
the credentials JSON to be stored as the upstream's `api_key`:

```json
{
  "id_token": "...",
  "access_token": "...",
  "refresh_token": "...",
  "chatgpt_account_id": "org_...",
  "expires_at": 1764060000,
  "client_id": "app_EMoamEEZ73f0CkXaXp7hrann"
}
```

`handleCodexUtilization` calls `GET https://chatgpt.com/backend-api/wham/usage`
with the refreshed bearer + `ChatGPT-Account-ID`. (The ChatGPT-subscription
usage endpoint is `wham/usage`, not `codex/usage` — `codex/usage` is the
API-key path. Confirmed against `codex-rs/backend-client/src/client.rs`'s
`PathStyle::ChatGptApi` branch.) The handler applies the same 60-second
cache and auto-snapshot logic as `handleClaudeCodeUtilization`. The returned
JSON is served verbatim under `{"data": ...}`.

### Dashboard (`dashboard/src/api/upstreams.ts`, `dashboard/src/pages/admin/UpstreamsPage.tsx`)

New hooks parallel to the claudecode ones:

- `useCodexOAuthStart()`
- `useCodexOAuthExchange()`
- `useUpstreamCodexOAuthStatus(upstreamId)`
- `useUpstreamCodexOAuthRefresh()`
- `useCodexUtilization(upstreamId)`

`UpstreamsPage.tsx`:
- Add `codex` to the provider `<SelectItem>` list
- Add a `provider === "codex"` branch in the create/edit dialog that renders
  the same three-step OAuth flow widget used for claudecode (extracted into a
  shared `<OAuthSetupSteps>` component if it's not already shared)
- Add a token-status badge on the upstream row when `provider === "codex"`
- Add a utilization mini-card mirroring the claudecode one

## OAuth Flow Sequence

```
Admin              Dashboard            Backend           OpenAI Auth
  |-- "Authorize"-->|                     |                    |
  |                 |-- POST start ------>|                    |
  |                 |<- auth_url+PKCE ----|                    |
  |<-- show URL ----|                     |                    |
  |                                                             |
  |-- click & authorize in browser --------------------------->|
  |<-- redirect to localhost:1455/auth/callback?code=…&state=… (page won't load)
  |                                                             |
  |-- paste full URL -->|                                       |
  |                 |-- POST exchange --->|                    |
  |                 |   (callback, state, |-- form POST ------>|
  |                 |    verifier)        |<- id+access+refresh|
  |                 |                     | parse account_id   |
  |                 |<- credentials JSON -|                    |
  |                 |-- POST/PUT upstream |                    |
  |                 |   (api_key=blob)    |                    |
  |                 |<- 201/200 ----------|                    |
```

## Files to Add / Modify

### Backend — new files
| File | Purpose |
|------|---------|
| `internal/proxy/codex.go` | `directorSetCodexUpstream` and pinned constants |
| `internal/proxy/provider_codex.go` | `CodexTransformer` |
| `internal/proxy/codex_oauth.go` | `CodexCredentials`, `CodexOAuthTokenManager`, `ParseCodexAccessTokenAndAccount` |
| `internal/admin/handle_codex_oauth.go` | five admin handlers |

### Backend — modified
| File | Change |
|------|--------|
| `internal/types/upstream.go` | Add `ProviderCodex` constant |
| `internal/proxy/provider_transform.go` | Register `CodexTransformer` in `init()` |
| `internal/proxy/router_engine.go` | New `codexOAuthMgr` field, getters, load/reload calls |
| `internal/proxy/executor.go` | Two new branches (token resolve + 401/403 retry); extend `sanitizeOutboundHeaders` |
| `internal/admin/routes.go` | Wire the five new routes |
| `cmd/modelserver/main.go` (or wherever `NewRouter` is built) | Construct `CodexOAuthTokenManager` and pass it in |

### Frontend — new
| File | Purpose |
|------|---------|
| (extracted) `dashboard/src/components/OAuthSetupSteps.tsx` | Shared three-step UI used by claudecode + codex |
| `dashboard/src/api/codex.ts` (or new section in `upstreams.ts`) | Five React Query hooks |

### Frontend — modified
| File | Change |
|------|--------|
| `dashboard/src/pages/admin/UpstreamsPage.tsx` | Add `codex` provider option, OAuth UI branch, status badge, utilization card |

### Tests — new
| File | Purpose |
|------|---------|
| `internal/proxy/codex_test.go` | `directorSetCodexUpstream` golden assertions; `sanitizeOutboundHeaders` whitelists codex headers |
| `internal/proxy/codex_oauth_test.go` | id_token claim extraction, refresh round-trip with mock token endpoint, single-flight dedup, force-refresh path |
| `internal/proxy/provider_codex_test.go` | `SetUpstream` accepts both raw access token and JSON blob |
| `internal/admin/handle_codex_oauth_test.go` | start/exchange happy-path against a stubbed `auth.openai.com`; status/refresh round-trip |

### Tests — modified
| File | Change |
|------|--------|
| `internal/proxy/executor_test.go` (and any provider-matrix test) | Codex branch in token-resolution and 401/403 retry |

## Edge Cases

- **Account-id missing**: some accounts have no workspace; the id_token claim
  is then absent. Fallback: send `Authorization` only, no `ChatGPT-Account-ID`.
- **Refresh-token reuse / revoked**: surface "Re-authorize" in dashboard; same
  pattern as claudecode. Persist nothing on permanent refresh failure.
- **id_token unparseable**: log warning during exchange; still persist the
  credentials with `chatgpt_account_id=""`. The proxy path treats empty
  account-id as "send no header".
- **Concurrent refresh** (proxy + manual button + 401-retry): `singleflight.Group`
  keyed by `upstream.ID`, mirroring `OAuthTokenManager`.
- **Client supplies its own `Session-Id`**: preserved verbatim; backend only
  fills in a random UUID when missing. Pinning to `upstream.ID` was rejected
  because the ChatGPT backend would see all proxied requests as one runaway
  session.
- **Multiple codex upstreams** (e.g., one per ChatGPT workspace): independent
  credentials; independent account-ids; no cross-contamination because all
  manager state is keyed by `upstream.ID`.
- **Header allow-list regression**: extending `sanitizeOutboundHeaders` is the
  one cross-cutting change. The codex_test golden assertions will catch a
  missing entry on day one.

## Open Questions (resolved during brainstorming)

| Question | Resolution |
|----------|------------|
| Auth modes to support | ChatGPT OAuth only |
| Wire format / client entrypoint | Existing `/v1/responses` (OpenAI Responses API) |
| OAuth UX | Manual paste-callback URL (matches claudecode) |
| Utilization endpoint in this spec | Yes |
| Identity-fingerprint headers (originator, version, etc.) | Backend always injects `codex_cli_rs` fingerprint |
| `Session-Id` derivation | Preserve client value; backend fills random UUID if absent |
| Implementation approach | Mirror claudecode template (Approach A) |
