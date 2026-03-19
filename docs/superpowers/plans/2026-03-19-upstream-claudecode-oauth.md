# Upstream Claude Code OAuth Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the existing `OAuthTokenManager` into the proxy execution path for auto-refresh, and build the frontend OAuth authorization flow UI for claudecode upstreams.

**Architecture:** The backend already has OAuth admin endpoints and `OAuthTokenManager`. This plan wires the manager into `Router` → `Executor` → `Handler` so proxy requests auto-refresh tokens. The frontend adds React Query hooks calling existing endpoints, and a conditional OAuth UI in `UpstreamsPage.tsx` when `provider === "claudecode"`.

**Tech Stack:** Go 1.26, React 19, TypeScript 5.7, TanStack React Query, Tailwind CSS 4, shadcn/ui

**Spec:** `docs/superpowers/specs/2026-03-19-upstream-claudecode-oauth-design.md`

---

### Task 1: Wire `OAuthTokenManager` into `Router`

**Files:**
- Modify: `internal/proxy/router_engine.go:45-61` (Router struct), `internal/proxy/router_engine.go:83-107` (NewRouter), `internal/proxy/router_engine.go:111-197` (buildMaps), `internal/proxy/router_engine.go:406-415` (Reload)
- Modify: `cmd/modelserver/main.go:113` (NewRouter call site)
- Test: `internal/proxy/claudecode_oauth_test.go`

- [ ] **Step 1: Write test for Router-level token resolution**

Add a test to `internal/proxy/claudecode_oauth_test.go` that verifies the router delegates to `OAuthTokenManager`:

```go
func TestRouter_GetClaudeCodeAccessToken(t *testing.T) {
	mgr := NewOAuthTokenManager(nil, nil, nil)

	mgr.mu.Lock()
	mgr.credentials["up1"] = &ClaudeCodeCredentials{
		AccessToken:  "valid-token",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	mgr.mu.Unlock()

	r := &Router{oauthMgr: mgr}
	token, err := r.GetClaudeCodeAccessToken("up1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "valid-token" {
		t.Errorf("token = %s, want valid-token", token)
	}
}

func TestRouter_GetClaudeCodeAccessToken_NoManager(t *testing.T) {
	r := &Router{}
	_, err := r.GetClaudeCodeAccessToken("up1")
	if err == nil {
		t.Error("expected error when oauthMgr is nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestRouter_GetClaudeCodeAccessToken -v`
Expected: FAIL — `Router` has no `oauthMgr` field or `GetClaudeCodeAccessToken` method.

- [ ] **Step 3: Add `oauthMgr` field to `Router` and `GetClaudeCodeAccessToken` method**

In `internal/proxy/router_engine.go`, add to the `Router` struct (after line 61, before `logger`):

```go
	oauthMgr   *OAuthTokenManager
```

Add the method after `GetUpstreamKey` (~line 422):

```go
// GetClaudeCodeAccessToken returns a valid OAuth access token for a claudecode upstream,
// refreshing if necessary. Returns an error if the manager is not configured or
// the upstream has no credentials.
func (r *Router) GetClaudeCodeAccessToken(upstreamID string) (string, error) {
	if r.oauthMgr == nil {
		return "", fmt.Errorf("OAuthTokenManager not configured")
	}
	return r.oauthMgr.GetAccessToken(upstreamID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestRouter_GetClaudeCodeAccessToken -v`
Expected: PASS

- [ ] **Step 5: Wire `OAuthTokenManager` into `NewRouter` and `buildMaps`**

In `internal/proxy/router_engine.go`:

1. Change `NewRouter` parameter from `_ *store.Store` to `oauthMgr *OAuthTokenManager`:

```go
func NewRouter(
	upstreams []types.Upstream,
	groups []store.UpstreamGroupWithMembers,
	routes []types.Route,
	encKey []byte,
	logger *slog.Logger,
	sessionTTL time.Duration,
	oauthMgr *OAuthTokenManager,
) *Router {
	r := &Router{
		sessionTTL: sessionTTL,
		logger:     logger,
		oauthMgr:   oauthMgr,
	}
```

2. At the end of `buildMaps`, after `r.decryptedKeys = keys` (line 195), add:

```go
	// Load/reload OAuth credentials for claudecode upstreams.
	if r.oauthMgr != nil {
		r.oauthMgr.Reload(upstreams, keys)
	}
```

Remove the `store` import if it becomes unused (it shouldn't — `store.UpstreamGroupWithMembers` is still used).

- [ ] **Step 6: Update call site in `cmd/modelserver/main.go`**

At line 113, change:

```go
	router := proxy.NewRouter(upstreams, groups, routingRoutes, encryptionKey, logger, cfg.Trace.SessionTTL, st)
```

To:

```go
	oauthMgr := proxy.NewOAuthTokenManager(st, encryptionKey, logger)
	router := proxy.NewRouter(upstreams, groups, routingRoutes, encryptionKey, logger, cfg.Trace.SessionTTL, oauthMgr)
```

- [ ] **Step 7: Run full test suite to verify no regressions**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/proxy/ -v`
Expected: All tests PASS, build succeeds.

- [ ] **Step 8: Commit**

```bash
git add internal/proxy/router_engine.go internal/proxy/claudecode_oauth_test.go cmd/modelserver/main.go
git commit -m "feat: wire OAuthTokenManager into Router for claudecode auto-refresh"
```

---

### Task 2: Resolve OAuth token in Executor before `SetUpstream`

**Files:**
- Modify: `internal/proxy/executor.go:240-250`
- Test: `internal/proxy/claudecode_oauth_test.go`

- [ ] **Step 1: Write test for executor-level token resolution**

Add to `internal/proxy/claudecode_oauth_test.go`:

```go
func TestExecutor_ClaudeCodeTokenResolution(t *testing.T) {
	// Verify that when a claudecode upstream is selected, the executor resolves
	// a fresh OAuth token via the Router's OAuthTokenManager rather than using
	// the raw credentials JSON.
	mgr := NewOAuthTokenManager(nil, nil, nil)
	mgr.mu.Lock()
	mgr.credentials["up-cc"] = &ClaudeCodeCredentials{
		AccessToken:  "resolved-oauth-token",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	mgr.mu.Unlock()

	r := &Router{oauthMgr: mgr}
	// Simulate what the executor does: resolve token for claudecode upstream.
	token, err := r.GetClaudeCodeAccessToken("up-cc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "resolved-oauth-token" {
		t.Errorf("token = %s, want resolved-oauth-token", token)
	}

	// For non-existent upstream, should return error (executor falls back to raw key).
	_, err = r.GetClaudeCodeAccessToken("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent upstream")
	}
}
```

- [ ] **Step 2: Run test to verify it passes (depends on Task 1)**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestExecutor_ClaudeCodeTokenResolution -v`
Expected: PASS

- [ ] **Step 3: Add claudecode token resolution in executor retry loop**

In `internal/proxy/executor.go`, after the Bedrock params block (line 244) and before the `SetUpstream` call (line 247), add:

```go
		// For Claude Code upstreams, resolve a fresh OAuth access token
		// via the OAuthTokenManager instead of using the raw credentials JSON.
		apiKeyForUpstream := candidate.APIKey
		if upstream.Provider == types.ProviderClaudeCode {
			if token, err := e.router.GetClaudeCodeAccessToken(upstream.ID); err == nil {
				apiKeyForUpstream = token
			} else {
				logger.Warn("claudecode token resolution failed, falling back to stored key", "error", err)
			}
		}
```

Then change line 247 from:

```go
		if err := transformer.SetUpstream(outReq, upstream, candidate.APIKey); err != nil {
```

To:

```go
		if err := transformer.SetUpstream(outReq, upstream, apiKeyForUpstream); err != nil {
```

- [ ] **Step 4: Run tests**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/proxy/ -v`
Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/executor.go internal/proxy/claudecode_oauth_test.go
git commit -m "feat: resolve claudecode OAuth token in executor before SetUpstream"
```

---

### Task 3: Update `handler.go` count_tokens proxy and clean up `provider_claudecode.go`

**Files:**
- Modify: `internal/proxy/handler.go:187-195`
- Modify: `internal/proxy/provider_claudecode.go:24-35`
- Test: `internal/proxy/claudecode_oauth_test.go`

- [ ] **Step 1: Write test for SetUpstream handling both raw token and JSON blob**

Add to `internal/proxy/claudecode_oauth_test.go`:

```go
func TestClaudeCodeTransformer_SetUpstream_RawToken(t *testing.T) {
	transformer := &ClaudeCodeTransformer{}
	req, _ := http.NewRequest("POST", "https://example.com/v1/messages", nil)
	upstream := &types.Upstream{BaseURL: "https://api.anthropic.com"}

	// When passed a raw token (pre-resolved by OAuthTokenManager), SetUpstream should use it directly.
	err := transformer.SetUpstream(req, upstream, "raw-access-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := req.Header.Get("Authorization")
	if got != "Bearer raw-access-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer raw-access-token")
	}
}

func TestClaudeCodeTransformer_SetUpstream_JSONBlob(t *testing.T) {
	transformer := &ClaudeCodeTransformer{}
	req, _ := http.NewRequest("POST", "https://example.com/v1/messages", nil)
	upstream := &types.Upstream{BaseURL: "https://api.anthropic.com"}

	// When passed a JSON blob (fallback), SetUpstream should extract access_token.
	err := transformer.SetUpstream(req, upstream, `{"access_token":"extracted-token","refresh_token":"rt","expires_at":9999999999}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := req.Header.Get("Authorization")
	if got != "Bearer extracted-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer extracted-token")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail (current SetUpstream always parses as JSON)**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestClaudeCodeTransformer_SetUpstream -v`
Expected: `TestClaudeCodeTransformer_SetUpstream_RawToken` FAILS (raw token gets parsed as JSON, returns empty).

- [ ] **Step 3: Update count_tokens proxy in `handler.go`**

In `internal/proxy/handler.go`, replace lines 187-195 (the `Director` function):

```go
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			if selected.Upstream.Provider == types.ProviderClaudeCode {
				// Resolve fresh OAuth token via the manager.
				accessToken := ParseClaudeCodeAccessToken(selected.APIKey)
				if token, err := h.router.GetClaudeCodeAccessToken(selected.Upstream.ID); err == nil {
					accessToken = token
				}
				directorSetClaudeCodeUpstream(req, selected.Upstream.BaseURL, accessToken)
			} else {
				directorSetUpstream(req, selected.Upstream.BaseURL, selected.APIKey)
			}
		},
```

- [ ] **Step 2: Clean up `provider_claudecode.go` comments**

In `internal/proxy/provider_claudecode.go`, replace lines 24-35:

```go
// SetUpstream configures the outbound request for a Claude Code upstream.
// The apiKey parameter is expected to be either a raw access token (when resolved
// by the OAuthTokenManager via the executor) or a JSON credentials blob (fallback).
func (t *ClaudeCodeTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
	accessToken := apiKey
	// If the apiKey looks like JSON (starts with '{'), extract the access_token field.
	if len(apiKey) > 0 && apiKey[0] == '{' {
		if parsed := ParseClaudeCodeAccessToken(apiKey); parsed != "" {
			accessToken = parsed
		}
	}
	directorSetClaudeCodeUpstream(r, upstream.BaseURL, accessToken)
	return nil
}
```

- [ ] **Step 3: Run tests**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/proxy/ -v`
Expected: All tests PASS.

- [ ] **Step 5: Run all tests including new ones**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run "TestClaudeCodeTransformer_SetUpstream|TestRouter_GetClaudeCode|TestExecutor_ClaudeCode" -v`
Expected: All PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/handler.go internal/proxy/provider_claudecode.go internal/proxy/claudecode_oauth_test.go
git commit -m "feat: use OAuthTokenManager in count_tokens proxy, clean up SetUpstream"
```

---

### Task 4: Add frontend OAuth API hooks

**Files:**
- Modify: `dashboard/src/api/upstreams.ts`

- [ ] **Step 1: Add OAuth hooks to `upstreams.ts`**

Append to `dashboard/src/api/upstreams.ts`, before the `// --- Upstream Groups ---` section (line 45):

```typescript
// --- Claude Code OAuth ---
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

export function useClaudeCodeOAuthExchange() {
  return useMutation({
    mutationFn: (body: {
      callback_url: string;
      code_verifier: string;
      state: string;
      redirect_uri: string;
    }) =>
      api.post<DataResponse<{
        access_token: string;
        refresh_token: string;
        expires_at: number;
        client_id: string;
      }>>("/api/v1/upstreams/claudecode/oauth/exchange", body),
  });
}

export function useUpstreamOAuthStatus(upstreamId: string | undefined) {
  return useQuery({
    queryKey: ["admin", "upstreams", upstreamId, "oauth-status"],
    queryFn: () =>
      api.get<DataResponse<{ expires_at: number; has_refresh_token: boolean }>>(
        `/api/v1/upstreams/${upstreamId}/oauth/status`,
      ),
    enabled: !!upstreamId,
    refetchInterval: 60_000,
  });
}

export function useUpstreamOAuthRefresh() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (upstreamId: string) =>
      api.post<DataResponse<{ expires_at: number; has_refresh_token: boolean }>>(
        `/api/v1/upstreams/${upstreamId}/oauth/refresh`,
      ),
    onSuccess: (_, upstreamId) => {
      qc.invalidateQueries({ queryKey: ["admin", "upstreams", upstreamId, "oauth-status"] });
    },
  });
}
```

- [ ] **Step 2: Verify TypeScript compiles**

Run: `cd /root/coding/modelserver/dashboard && npx tsc --noEmit`
Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add dashboard/src/api/upstreams.ts
git commit -m "feat: add frontend OAuth hooks for claudecode upstreams"
```

---

### Task 5: Build OAuth authorization flow UI in UpstreamsPage

**Files:**
- Modify: `dashboard/src/pages/admin/UpstreamsPage.tsx`

This is the largest task. The changes are:
1. When `provider === "claudecode"` in the create/edit dialog, show an OAuth flow instead of the API Key input
2. Add token status display and refresh/re-authorize actions for existing claudecode upstreams
3. Add a "Refresh Token" action in the upstream dropdown menu for claudecode upstreams

- [ ] **Step 1: Add imports and OAuth state to `UpstreamsPage.tsx`**

Add to the existing imports at the top of the file:

```typescript
import {
  useClaudeCodeOAuthStart,
  useClaudeCodeOAuthExchange,
  useUpstreamOAuthStatus,
  useUpstreamOAuthRefresh,
} from "@/api/upstreams";
import { RefreshCw, ExternalLink, KeyRound, Clock } from "lucide-react";
```

Inside `UpstreamsPage()`, after the existing `useTestUpstream()` hook call (line 40), add:

```typescript
  const oauthStart = useClaudeCodeOAuthStart();
  const oauthExchange = useClaudeCodeOAuthExchange();
  const oauthRefresh = useUpstreamOAuthRefresh();
```

After the existing form state (line 55), add OAuth flow state:

```typescript
  const [oauthStep, setOauthStep] = useState<"idle" | "started" | "complete">("idle");
  const [oauthData, setOauthData] = useState<{
    auth_url: string;
    state: string;
    code_verifier: string;
    redirect_uri: string;
  } | null>(null);
  const [callbackUrl, setCallbackUrl] = useState("");
  const [oauthCredentials, setOauthCredentials] = useState<string | null>(null);
```

- [ ] **Step 2: Add OAuth helper functions**

After the existing `openCreate` function, add:

```typescript
  function resetOauthState() {
    setOauthStep("idle");
    setOauthData(null);
    setCallbackUrl("");
    setOauthCredentials(null);
  }
```

Update `openCreate` to also call `resetOauthState()`:

```typescript
  function openCreate() {
    setEditingId(null);
    setForm({
      provider: "anthropic",
      name: "",
      base_url: "",
      api_key: "",
      supported_models: "",
      weight: "1",
      max_concurrent: "10",
      test_model: "",
      status: "active",
    });
    resetOauthState();
    setDialogOpen(true);
  }
```

Update `openEdit` similarly — add `resetOauthState()` at the end.

Add OAuth action handlers:

```typescript
  async function handleOAuthStart() {
    try {
      const res = await oauthStart.mutateAsync({});
      setOauthData(res.data);
      setOauthStep("started");
    } catch {
      toast.error("Failed to start OAuth flow");
    }
  }

  async function handleOAuthExchange() {
    if (!oauthData || !callbackUrl) return;
    try {
      const res = await oauthExchange.mutateAsync({
        callback_url: callbackUrl,
        code_verifier: oauthData.code_verifier,
        state: oauthData.state,
        redirect_uri: oauthData.redirect_uri,
      });
      const credsJson = JSON.stringify(res.data);
      setOauthCredentials(credsJson);
      setOauthStep("complete");
      setForm((p) => ({ ...p, api_key: credsJson }));
      toast.success("OAuth authorization successful");
    } catch {
      toast.error("Failed to exchange OAuth code");
    }
  }

  async function handleOAuthRefresh(upstreamId: string, upstreamName: string) {
    try {
      const res = await oauthRefresh.mutateAsync(upstreamId);
      const expiresAt = new Date(res.data.expires_at * 1000);
      toast.success(`${upstreamName}: token refreshed, expires ${expiresAt.toLocaleString()}`);
    } catch {
      toast.error(`${upstreamName}: token refresh failed`);
    }
  }
```

- [ ] **Step 3: Add token status helper**

Add a helper function for formatting token expiry:

```typescript
  function formatTokenExpiry(expiresAt: number): { text: string; status: "valid" | "warning" | "expired" } {
    const now = Math.floor(Date.now() / 1000);
    const diff = expiresAt - now;
    if (diff <= 0) return { text: "Expired", status: "expired" };
    if (diff < 300) return { text: `Expires in ${Math.floor(diff / 60)}m`, status: "warning" };
    const hours = Math.floor(diff / 3600);
    const mins = Math.floor((diff % 3600) / 60);
    return { text: hours > 0 ? `${hours}h ${mins}m` : `${mins}m`, status: "valid" };
  }
```

- [ ] **Step 4: Update the dialog form — conditional Claude Code OAuth UI**

In the dialog's `<div className="space-y-4 py-4">`, replace the API Key field (the `<div className="space-y-2">` block containing the `<Label>{editingId ? "API Key (leave blank..." ...}` around lines 311-319) with a conditional render:

```tsx
            {form.provider === "claudecode" ? (
              <div className="space-y-3">
                <Label>OAuth Authorization</Label>
                {oauthStep === "idle" && !editingId && (
                  <div className="space-y-2">
                    <Button
                      type="button"
                      variant="outline"
                      className="w-full"
                      onClick={handleOAuthStart}
                      disabled={oauthStart.isPending}
                    >
                      <KeyRound className="mr-2 h-4 w-4" />
                      {oauthStart.isPending ? "Starting..." : "Start OAuth Authorization"}
                    </Button>
                    <p className="text-xs text-muted-foreground">
                      Authorize via Anthropic OAuth to get access tokens automatically.
                    </p>
                  </div>
                )}
                {oauthStep === "idle" && editingId && (
                  <div className="space-y-2">
                    <p className="text-sm text-muted-foreground">
                      This upstream already has OAuth credentials. Use "Re-authorize" to get new tokens.
                    </p>
                    <Button
                      type="button"
                      variant="outline"
                      className="w-full"
                      onClick={handleOAuthStart}
                      disabled={oauthStart.isPending}
                    >
                      <RefreshCw className="mr-2 h-4 w-4" />
                      Re-authorize
                    </Button>
                  </div>
                )}
                {oauthStep === "started" && oauthData && (
                  <div className="space-y-3">
                    <div className="rounded-md bg-muted p-3 text-sm space-y-2">
                      <p className="font-medium">Step 1: Click the link below to authorize</p>
                      <a
                        href={oauthData.auth_url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-primary underline inline-flex items-center gap-1 break-all"
                      >
                        Open Anthropic Authorization Page
                        <ExternalLink className="h-3 w-3 flex-shrink-0" />
                      </a>
                    </div>
                    <div className="space-y-2">
                      <p className="text-sm font-medium">Step 2: Paste the callback URL here</p>
                      <p className="text-xs text-muted-foreground">
                        After authorizing, your browser will redirect to a localhost URL that won't load.
                        Copy the full URL from your browser's address bar and paste it below.
                      </p>
                      <Input
                        value={callbackUrl}
                        onChange={(e) => setCallbackUrl(e.target.value)}
                        placeholder="http://localhost:PORT/callback?code=...&state=..."
                      />
                    </div>
                    <Button
                      type="button"
                      className="w-full"
                      onClick={handleOAuthExchange}
                      disabled={!callbackUrl || oauthExchange.isPending}
                    >
                      {oauthExchange.isPending ? "Exchanging..." : "Complete Authorization"}
                    </Button>
                  </div>
                )}
                {oauthStep === "complete" && (
                  <div className="rounded-md bg-green-50 dark:bg-green-950 p-3 text-sm text-green-700 dark:text-green-300">
                    OAuth credentials obtained successfully. Click Save to create the upstream.
                  </div>
                )}
              </div>
            ) : (
              <div className="space-y-2">
                <Label>{editingId ? "API Key (leave blank to keep current)" : "API Key"}</Label>
                <Input
                  type="password"
                  value={form.api_key}
                  onChange={(e) => setForm((p) => ({ ...p, api_key: e.target.value }))}
                  placeholder="sk-..."
                />
              </div>
            )}
```

- [ ] **Step 5: Update the Save button disabled condition**

The current condition on the Save button (line 377) requires `!form.api_key` when not editing. For claudecode, the api_key is set via OAuth. Update the disabled condition:

```tsx
            <Button
              onClick={handleSave}
              disabled={
                !form.name ||
                !form.base_url ||
                (!editingId && !form.api_key && form.provider !== "claudecode") ||
                (!editingId && form.provider === "claudecode" && oauthStep !== "complete") ||
                isSaving
              }
            >
```

- [ ] **Step 6: Add "Refresh Token" action to dropdown menu for claudecode upstreams**

In the `columns` definition, inside the dropdown menu (after the "Test Connection" `DropdownMenuItem`, around line 207), add:

```tsx
            {u.provider === "claudecode" && (
              <DropdownMenuItem
                onClick={() => handleOAuthRefresh(u.id, u.name)}
                disabled={oauthRefresh.isPending}
              >
                <RefreshCw className="mr-2 h-4 w-4" />
                Refresh Token
              </DropdownMenuItem>
            )}
```

- [ ] **Step 7: Verify TypeScript compiles and dev server runs**

Run: `cd /root/coding/modelserver/dashboard && npx tsc --noEmit`
Expected: No errors.

- [ ] **Step 8: Commit**

```bash
git add dashboard/src/pages/admin/UpstreamsPage.tsx
git commit -m "feat: add OAuth authorization flow UI for claudecode upstreams"
```

---

### Task 6: Add token status display in upstream list

**Files:**
- Modify: `dashboard/src/pages/admin/UpstreamsPage.tsx`

- [ ] **Step 1: Create a `TokenStatusBadge` component**

Add inside the `UpstreamsPage.tsx` file, before the `UpstreamsPage` function:

```typescript
function TokenStatusBadge({ upstreamId }: { upstreamId: string }) {
  const { data, isLoading } = useUpstreamOAuthStatus(upstreamId);

  if (isLoading) return <span className="text-xs text-muted-foreground">...</span>;

  const status = data?.data;
  if (!status) return null;

  const now = Math.floor(Date.now() / 1000);
  const diff = status.expires_at - now;

  let color: string;
  let text: string;
  if (diff <= 0) {
    color = "text-red-600 dark:text-red-400";
    text = "Token expired";
  } else if (diff < 300) {
    color = "text-yellow-600 dark:text-yellow-400";
    text = `Expiring (${Math.floor(diff / 60)}m)`;
  } else {
    const hours = Math.floor(diff / 3600);
    const mins = Math.floor((diff % 3600) / 60);
    color = "text-green-600 dark:text-green-400";
    text = hours > 0 ? `Token OK (${hours}h ${mins}m)` : `Token OK (${mins}m)`;
  }

  return (
    <span className={`inline-flex items-center gap-1 text-xs ${color}`}>
      <Clock className="h-3 w-3" />
      {text}
    </span>
  );
}
```

- [ ] **Step 2: Add token status column to the table**

In the `columns` array, after the "Status" column (around line 172), add:

```typescript
    {
      header: "Token",
      accessor: (u) =>
        u.provider === "claudecode" ? (
          <TokenStatusBadge upstreamId={u.id} />
        ) : (
          <span className="text-xs text-muted-foreground">—</span>
        ),
    },
```

- [ ] **Step 3: Verify TypeScript compiles**

Run: `cd /root/coding/modelserver/dashboard && npx tsc --noEmit`
Expected: No errors.

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/pages/admin/UpstreamsPage.tsx
git commit -m "feat: show token expiry status for claudecode upstreams in list"
```

---

### Task 7: Final verification

- [ ] **Step 1: Run Go tests**

Run: `cd /root/coding/modelserver && go test ./... 2>&1 | tail -30`
Expected: All tests PASS.

- [ ] **Step 2: Run frontend type check**

Run: `cd /root/coding/modelserver/dashboard && npx tsc --noEmit`
Expected: No errors.

- [ ] **Step 3: Verify Go build**

Run: `cd /root/coding/modelserver && go build ./cmd/modelserver/`
Expected: Build succeeds.
