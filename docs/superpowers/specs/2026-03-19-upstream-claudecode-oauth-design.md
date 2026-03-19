# Upstream Claude Code OAuth Authorization & Token Refresh

**Date:** 2026-03-19
**Status:** Draft

## Problem

The new **upstreams** system has no OAuth support for Claude Code provider. Admins must manually obtain and paste a credentials JSON blob into the API Key field. There is no token status visibility or automatic/manual refresh capability.

The legacy **channels** system already has complete OAuth support (`oauth/start`, `oauth/exchange`, `oauth/status`, `oauth/refresh`) and proxy-level auto-refresh via `OAuthTokenManager`, but none of this is available for upstreams.

## Solution

Add Claude Code OAuth authorization flow and token refresh to the upstreams system, covering three layers:

1. **Admin API** — new endpoints for OAuth start/exchange/status/refresh under `/api/v1/upstreams/`
2. **Proxy auto-refresh** — extend `OAuthTokenManager` to support upstreams alongside channels
3. **Frontend** — OAuth-aware UI in `UpstreamsPage.tsx` for claudecode upstreams

## Design

### 1. Backend Admin API

#### New Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/upstreams/claudecode/oauth/start` | Generate PKCE params + auth URL. Reuse existing `handleClaudeCodeOAuthStart()` |
| `POST` | `/api/v1/upstreams/claudecode/oauth/exchange` | Exchange authorization code for tokens. Reuse existing `handleClaudeCodeOAuthExchange()` |
| `GET` | `/api/v1/upstreams/{upstreamID}/oauth/status` | Decrypt credentials, return `expires_at` and `has_refresh_token` |
| `POST` | `/api/v1/upstreams/{upstreamID}/oauth/refresh` | Refresh token and persist updated credentials |

#### Route Registration (in `routes.go`)

```go
// Inside the upstreams route group (superadmin only):
r.Route("/upstreams", func(r chi.Router) {
    r.Use(RequireSuperadmin)
    // Existing CRUD...
    r.Post("/claudecode/oauth/start", handleClaudeCodeOAuthStart())
    r.Post("/claudecode/oauth/exchange", handleClaudeCodeOAuthExchange())
    r.Route("/{upstreamID}", func(r chi.Router) {
        // Existing CRUD + test...
        r.Get("/oauth/status", handleUpstreamClaudeCodeTokenStatus(st, encKey))
        r.Post("/oauth/refresh", handleUpstreamClaudeCodeTokenRefresh(st, encKey))
    })
})
```

#### New Handlers (`handle_claudecode_oauth.go`)

`handleUpstreamClaudeCodeTokenStatus` — identical to `handleClaudeCodeTokenStatus` but uses `st.GetUpstreamByID` instead of `st.GetChannelByID`.

`handleUpstreamClaudeCodeTokenRefresh` — identical to `handleClaudeCodeTokenRefresh` but uses `st.GetUpstreamByID` / `st.UpdateUpstream` instead of channel equivalents.

### 2. Proxy Auto-Refresh (`OAuthTokenManager`)

The existing `OAuthTokenManager` maps `channelID → *ClaudeCodeCredentials` and calls `st.UpdateChannel` to persist refreshed tokens. Extend it to also handle upstreams.

#### Changes to `claudecode_oauth.go`

**Unified key namespace:** Use prefixed keys internally:
- Channel credentials: `"ch:"+channelID`
- Upstream credentials: `"up:"+upstreamID`

**New methods:**
- `LoadUpstreamCredentials(upstreams []types.Upstream, decryptedKeys map[string]string)` — parse and store credentials for claudecode upstreams
- `ReloadUpstreams(upstreams []types.Upstream, decryptedKeys map[string]string)` — reload with freshness preservation (same logic as `Reload`)
- `GetUpstreamAccessToken(upstreamID string) (string, error)` — like `GetAccessToken` but uses upstream prefix and `st.UpdateUpstream` for persistence

**Refactor `refreshToken`:** Accept a generic key + a persistence callback, so the refresh logic is shared between channels and upstreams. Only the persist step differs (`UpdateChannel` vs `UpdateUpstream`).

#### Integration with `Router` / `ClaudeCodeTransformer`

The `Router` constructor already has a reserved `*store.Store` parameter (line 90: `reserved for future OAuthTokenManager integration`). Wire it up:

1. `Router` stores an `*OAuthTokenManager` field
2. On `NewRouter` / `Reload`, call `mgr.LoadUpstreamCredentials` / `mgr.ReloadUpstreams`
3. `ClaudeCodeTransformer.SetUpstream` receives the `OAuthTokenManager` and calls `GetUpstreamAccessToken(upstreamID)` instead of `ParseClaudeCodeAccessToken(apiKey)`

### 3. Frontend

#### API Layer (`dashboard/src/api/upstreams.ts`)

New hooks:

```typescript
// Start OAuth flow — returns auth_url, state, code_verifier, redirect_uri
export function useClaudeCodeOAuthStart() {
  return useMutation({
    mutationFn: (body?: { redirect_uri?: string }) =>
      api.post<DataResponse<{
        auth_url: string;
        state: string;
        code_verifier: string;
        redirect_uri: string;
      }>>("/api/v1/upstreams/claudecode/oauth/start", body ?? {}),
  });
}

// Exchange callback URL for credentials
export function useClaudeCodeOAuthExchange() {
  return useMutation({
    mutationFn: (body: {
      callback_url: string;
      code_verifier: string;
      state: string;
      redirect_uri: string;
    }) => api.post<DataResponse<{
      access_token: string;
      refresh_token: string;
      expires_at: number;
      client_id: string;
    }>>("/api/v1/upstreams/claudecode/oauth/exchange", body),
  });
}

// Get token status for a claudecode upstream
export function useUpstreamOAuthStatus(upstreamId: string) {
  return useQuery({
    queryKey: ["admin", "upstreams", upstreamId, "oauth-status"],
    queryFn: () => api.get<DataResponse<{
      expires_at: number;
      has_refresh_token: boolean;
    }>>(`/api/v1/upstreams/${upstreamId}/oauth/status`),
    enabled: !!upstreamId,
    refetchInterval: 60_000, // Poll every minute
  });
}

// Manually refresh token
export function useUpstreamOAuthRefresh() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (upstreamId: string) =>
      api.post<DataResponse<{
        expires_at: number;
        has_refresh_token: boolean;
      }>>(`/api/v1/upstreams/${upstreamId}/oauth/refresh`),
    onSuccess: (_, upstreamId) => {
      qc.invalidateQueries({ queryKey: ["admin", "upstreams", upstreamId, "oauth-status"] });
    },
  });
}
```

#### UI Changes (`UpstreamsPage.tsx`)

**Create/Edit Dialog — when `provider === "claudecode"`:**

1. Hide the API Key text input
2. Show a multi-step OAuth flow:
   - **Step 1:** "Start Authorization" button → calls `oauth/start`, displays the auth URL as a clickable link (opens in new tab)
   - **Step 2:** Input field for user to paste back the callback URL from their browser
   - **Step 3:** "Complete Authorization" button → calls `oauth/exchange` with the callback URL + PKCE params
   - On success: the credentials JSON is sent as the `api_key` field in the create/update upstream call
3. When editing an existing claudecode upstream: show current token status (expires_at, formatted as relative time) and a "Re-authorize" option

**Upstream List Table:**

For claudecode upstreams, add a token status indicator in the Status column or as a separate badge:
- Green: token valid (expires_at > now + 5min)
- Yellow: token expiring soon (expires_at within 5min)
- Red: token expired
- Plus a "Refresh" quick action in the dropdown menu

**Token Status Component:**

A small component that shows for claudecode upstreams:
- Time until expiry (e.g., "Expires in 3h 20m")
- "Refresh Token" button
- "Re-authorize" button (if refresh token is missing or refresh fails)

## Files to Modify

### Backend
| File | Change |
|------|--------|
| `internal/admin/routes.go` | Mount new OAuth endpoints under `/upstreams` |
| `internal/admin/handle_claudecode_oauth.go` | Add `handleUpstreamClaudeCodeTokenStatus` and `handleUpstreamClaudeCodeTokenRefresh` |
| `internal/proxy/claudecode_oauth.go` | Extend `OAuthTokenManager` with upstream support (prefixed keys, `LoadUpstreamCredentials`, `ReloadUpstreams`, `GetUpstreamAccessToken`) |
| `internal/proxy/provider_claudecode.go` | Wire `OAuthTokenManager` into `SetUpstream` |
| `internal/proxy/router_engine.go` | Accept and store `*OAuthTokenManager`, call load/reload |

### Frontend
| File | Change |
|------|--------|
| `dashboard/src/api/upstreams.ts` | Add OAuth hooks |
| `dashboard/src/api/types.ts` | Add OAuth response types (if needed) |
| `dashboard/src/pages/admin/UpstreamsPage.tsx` | OAuth flow UI for claudecode provider, token status display |

### Tests
| File | Change |
|------|--------|
| `internal/proxy/claudecode_oauth_test.go` | Add tests for upstream credential loading, reload, and refresh |
| `internal/admin/handle_claudecode_oauth_test.go` | Add tests for upstream token status and refresh handlers (if test file exists) |

## OAuth Flow Sequence

```
Admin                   Dashboard             Backend            Anthropic OAuth
  |                        |                     |                     |
  |-- Click "Authorize" -->|                     |                     |
  |                        |-- POST oauth/start ->|                    |
  |                        |<- auth_url + PKCE ---|                    |
  |<-- Open auth URL ------|                     |                     |
  |                        |                     |                     |
  |-- Authorize in browser -------------------------------------------------->|
  |<-- Redirect to localhost:PORT/callback?code=xxx&state=yyy --------|
  |                        |                     |                     |
  |-- Paste callback URL ->|                     |                     |
  |                        |-- POST oauth/exchange ->|                 |
  |                        |   (callback_url,        |-- token req --->|
  |                        |    code_verifier,        |<- tokens ------|
  |                        |    state, redirect_uri)  |                |
  |                        |<- credentials JSON ------|                |
  |                        |                     |                     |
  |                        |-- POST/PUT upstream -->|                  |
  |                        |   (api_key=creds JSON) |                  |
  |                        |<- upstream created ----|                  |
```

## Edge Cases

- **Token expired, no refresh token:** Show "Re-authorize" button, disable "Refresh"
- **Refresh fails (e.g., refresh token revoked):** Show error, prompt re-authorization
- **Concurrent refresh (proxy + manual):** `singleflight.Group` in `OAuthTokenManager` deduplicates
- **Upstream deleted while OAuth in progress:** Frontend should handle 404 gracefully
- **Multiple claudecode upstreams:** Each has independent credentials, no interference
