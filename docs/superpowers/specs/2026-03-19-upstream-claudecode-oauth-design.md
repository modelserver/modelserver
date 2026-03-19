# Upstream Claude Code OAuth Authorization & Token Refresh

**Date:** 2026-03-19
**Status:** Draft

## Problem

The upstreams system has backend OAuth endpoints and an `OAuthTokenManager` for Claude Code, but:

1. **No frontend UI** — the dashboard has no OAuth authorization flow for claudecode upstreams. Admins must manually obtain credentials JSON and paste it into the API Key field.
2. **No proxy-level auto-refresh** — `OAuthTokenManager` exists and supports upstreams, but is not wired into the proxy execution path. `ClaudeCodeTransformer.SetUpstream` still calls `ParseClaudeCodeAccessToken(apiKey)` directly instead of using the manager.
3. **No token visibility** — the frontend shows no token status (expiry, refresh availability) for claudecode upstreams.

## Already Implemented

The following are already working in the current codebase:

### Backend Admin API Endpoints (in `routes.go`)

| Method | Path | Handler | Status |
|--------|------|---------|--------|
| `POST` | `/api/v1/upstreams/claudecode/oauth/start` | `handleClaudeCodeOAuthStart()` | Mounted |
| `POST` | `/api/v1/upstreams/claudecode/oauth/exchange` | `handleClaudeCodeOAuthExchange()` | Mounted |
| `GET` | `/api/v1/upstreams/{upstreamID}/oauth/status` | `handleClaudeCodeTokenStatus(st, encKey)` | Mounted |
| `POST` | `/api/v1/upstreams/{upstreamID}/oauth/refresh` | `handleClaudeCodeTokenRefresh(st, encKey)` | Mounted |

All four handlers already operate on upstreams (using `st.GetUpstreamByID`, `st.UpdateUpstream`).

### OAuthTokenManager (`internal/proxy/claudecode_oauth.go`)

Already supports upstreams:
- `LoadCredentials(upstreams []types.Upstream, decryptedKeys map[string]string)` — loads claudecode upstream credentials
- `Reload(upstreams []types.Upstream, decryptedKeys map[string]string)` — reloads with freshness preservation
- `GetAccessToken(upstreamID string) (string, error)` — returns valid token, auto-refreshes if needed
- `refreshToken(upstreamID string)` — exchanges refresh token, persists via `st.UpdateUpstream`
- Uses `singleflight.Group` for concurrent refresh deduplication

## Remaining Work

### 1. Wire `OAuthTokenManager` into Proxy Path

**Current state:**
- `ClaudeCodeTransformer.SetUpstream` (in `provider_claudecode.go:30-35`) calls `ParseClaudeCodeAccessToken(apiKey)` — no auto-refresh
- `handler.go:189-191` (count_tokens proxy) also calls `ParseClaudeCodeAccessToken(selected.APIKey)` directly
- `Router` has `_ *store.Store` parameter (line 90) reserved for this integration but unused

**Design:**

The cleanest approach is to resolve the access token in the executor/handler before calling `SetUpstream`, rather than changing the `ProviderTransformer` interface. This avoids interface changes that affect all providers.

**Option chosen:** Store `*OAuthTokenManager` on `Router`. Before calling `SetUpstream` for claudecode upstreams, resolve the access token via the manager and pass it as the `apiKey` parameter.

Changes:

1. **`router_engine.go`**: Add `oauthMgr *OAuthTokenManager` field to `Router`. Accept it in `NewRouter` (replace the `_ *store.Store` param). Call `oauthMgr.LoadCredentials` in `buildMaps`. Call `oauthMgr.Reload` in `Reload`. Add `GetClaudeCodeAccessToken(upstreamID string) (string, error)` method that delegates to the manager.

2. **`executor.go`** (line ~247): Before calling `transformer.SetUpstream`, if the upstream is claudecode, call `router.GetClaudeCodeAccessToken(upstream.ID)` to get a fresh access token and use that instead of `candidate.APIKey`.

3. **`handler.go`** (line ~189-191): Same pattern for the count_tokens proxy director function.

4. **`provider_claudecode.go`**: Remove the TODO comments. `SetUpstream` still receives the access token as `apiKey` but now it's already been refreshed by the caller.

### 2. Frontend OAuth Flow UI

#### API Hooks (`dashboard/src/api/upstreams.ts`)

New React Query hooks calling the existing backend endpoints:

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
export function useUpstreamOAuthStatus(upstreamId: string | undefined) {
  return useQuery({
    queryKey: ["admin", "upstreams", upstreamId, "oauth-status"],
    queryFn: () => api.get<DataResponse<{
      expires_at: number;
      has_refresh_token: boolean;
    }>>(`/api/v1/upstreams/${upstreamId}/oauth/status`),
    enabled: !!upstreamId,
    refetchInterval: 60_000,
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

1. Hide the standard API Key text input
2. Show a multi-step OAuth flow:
   - **Step 1:** "Start Authorization" button → calls `oauth/start`, displays the auth URL as a clickable link (opens in new tab). The `redirect_uri` uses a random high port on localhost, matching Claude Code CLI behavior.
   - **Step 2:** Input field for user to paste back the full callback URL from their browser address bar (the localhost URL that didn't resolve)
   - **Step 3:** "Complete Authorization" button → calls `oauth/exchange` with the callback URL + PKCE params stored from step 1
   - On success: the returned credentials JSON is sent as the `api_key` field when creating/updating the upstream
3. When editing an existing claudecode upstream: show current token status (expires_at, formatted as relative time) and a "Re-authorize" option

**Upstream List Table:**

For claudecode upstreams, show token status in an additional column or badge:
- Token valid: show relative expiry time (e.g., "3h 20m")
- Token expiring soon (< 5min): yellow warning
- Token expired: red indicator
- "Refresh" action in the dropdown menu for claudecode upstreams

**Token Status Display:**

A section within the upstream detail/edit view:
- Time until expiry
- "Refresh Token" button
- "Re-authorize" button (when refresh token is missing or refresh fails)

## OAuth Flow Sequence

```
Admin                   Dashboard              Backend              Anthropic OAuth
  |                        |                      |                       |
  |-- Click "Authorize" -->|                      |                       |
  |                        |-- POST oauth/start -->|                      |
  |                        |<- auth_url + PKCE ----|                      |
  |<-- Show auth URL ------|                      |                       |
  |                        |                      |                       |
  |-- Click link, authorize in browser ---------------------------------------->|
  |<-- Redirect to localhost:PORT/callback?code=xxx&state=yyy ------------|
  |   (page doesn't load — just copy the URL)     |                       |
  |                        |                      |                       |
  |-- Paste callback URL ->|                      |                       |
  |                        |-- POST oauth/exchange -->|                    |
  |                        |   (callback_url,         |-- token request -->|
  |                        |    code_verifier,         |<-- tokens --------|
  |                        |    state, redirect_uri)   |                   |
  |                        |<-- credentials JSON ------|                   |
  |                        |                      |                       |
  |                        |-- POST/PUT upstream ->|                      |
  |                        |   (api_key=creds JSON)|                      |
  |                        |<-- upstream created --|                      |
```

## Files to Modify

### Backend (proxy integration)
| File | Change |
|------|--------|
| `internal/proxy/router_engine.go` | Add `oauthMgr` field, accept in `NewRouter`, wire load/reload, add `GetClaudeCodeAccessToken` |
| `internal/proxy/executor.go` | Resolve claudecode access token via router before `SetUpstream` |
| `internal/proxy/handler.go` | Same for count_tokens proxy |
| `internal/proxy/provider_claudecode.go` | Remove TODO comments, `SetUpstream` now receives pre-resolved token |

### Frontend (new)
| File | Change |
|------|--------|
| `dashboard/src/api/upstreams.ts` | Add 4 OAuth hooks |
| `dashboard/src/pages/admin/UpstreamsPage.tsx` | OAuth flow UI, token status display, conditional UI for claudecode |

### Tests
| File | Change |
|------|--------|
| `internal/proxy/claudecode_oauth_test.go` | Add integration test for router-level token resolution |

## Edge Cases

- **Token expired, no refresh token:** Show "Re-authorize" button, disable "Refresh"
- **Refresh fails (e.g., refresh token revoked):** Show error toast, prompt re-authorization
- **Concurrent refresh (proxy + manual):** `singleflight.Group` in `OAuthTokenManager` deduplicates
- **Upstream deleted while OAuth in progress:** Frontend should handle 404 gracefully
- **Multiple claudecode upstreams:** Each has independent credentials, no interference
- **Proxy request when token is expired and refresh fails:** Fallback to existing token (current behavior), request may still succeed if token grace period applies

## Non-Goals

- Changing the `ProviderTransformer` interface
- Adding `HTTPS_PROXY` support to `OAuthTokenManager`'s HTTP client (follow-up item)
- Frontend auto-polling for OAuth completion (manual paste workflow)
