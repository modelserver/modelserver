# Upstream Codex (ChatGPT subscription) OAuth Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a new `codex` upstream provider that authenticates with the ChatGPT subscription via OAuth (PKCE) and proxies OpenAI Responses requests to `https://chatgpt.com/backend-api/codex/responses`, mirroring the existing `claudecode` upstream pattern.

**Architecture:** New `ProviderCodex` constant + `CodexTransformer` (reuses OpenAITransformer body/parser/stream, codex-specific `SetUpstream` only) + parallel `CodexOAuthTokenManager` for refresh/single-flight + admin OAuth handlers + dashboard UI flow. Executor gains two short branches mirroring the claudecode token-resolve and 401/403-retry branches. No DB migration — credentials JSON reuses `upstreams.api_key_encrypted`.

**Tech Stack:** Go 1.26, React 19, TypeScript 5.7, TanStack React Query, Tailwind CSS 4, shadcn/ui, chi router, golang.org/x/sync/singleflight

**Spec:** `docs/superpowers/specs/2026-04-24-upstream-codex-oauth-design.md`

---

## File Map

**New backend files:**
- `internal/proxy/codex.go` — `directorSetCodexUpstream`, pinned codex User-Agent / version constants
- `internal/proxy/provider_codex.go` — `CodexTransformer` implementing `ProviderTransformer`
- `internal/proxy/codex_oauth.go` — `CodexCredentials`, `CodexOAuthTokenManager`, JWT claim parsing
- `internal/admin/handle_codex_oauth.go` — five admin handlers (start, exchange, status, refresh, utilization)

**Modified backend files:**
- `internal/types/upstream.go` — add `ProviderCodex` constant
- `internal/proxy/provider_transform.go` — register `CodexTransformer` in `init()`
- `internal/proxy/router_engine.go` — add `codexOAuthMgr` field, threading, `GetCodexAccessToken` / `GetCodexAccountID` / `ForceRefreshCodexAccessToken`
- `internal/proxy/executor.go` — token resolution branch, 401/403 retry branch, extend `sanitizeOutboundHeaders`
- `internal/admin/routes.go` — wire five new routes
- `cmd/modelserver/main.go` — construct `CodexOAuthTokenManager`, pass to `NewRouter`

**New backend tests:**
- `internal/proxy/codex_test.go` — director golden assertions, sanitizer pass-through
- `internal/proxy/codex_oauth_test.go` — credentials parse, refresh round-trip, JWT claim extraction, single-flight dedup
- `internal/proxy/provider_codex_test.go` — `SetUpstream` for raw token and JSON blob
- `internal/admin/handle_codex_oauth_test.go` — start/exchange/status/refresh against stubbed `auth.openai.com`

**New frontend files:**
- `dashboard/src/api/codex.ts` — five React Query hooks

**Modified frontend files:**
- `dashboard/src/pages/admin/UpstreamsPage.tsx` — codex provider option, OAuth flow branch, status badge, utilization card

---

### Task 1: Add `ProviderCodex` constant

**Files:**
- Modify: `internal/types/upstream.go:5-14`

- [ ] **Step 1: Open the file and locate the provider constants block**

The block currently ends with `ProviderVertexOpenAI = "vertex-openai"` at line 13.

- [ ] **Step 2: Add the new constant**

Append `ProviderCodex` to the constants block in `internal/types/upstream.go`:

```go
const (
	ProviderAnthropic  = "anthropic"
	ProviderOpenAI     = "openai"
	ProviderGemini     = "gemini"
	ProviderBedrock    = "bedrock"
	ProviderClaudeCode = "claudecode"
	ProviderVertexAnthropic = "vertex-anthropic"
	ProviderVertexGoogle = "vertex-google"
	ProviderVertexOpenAI = "vertex-openai"
	ProviderCodex      = "codex"
)
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /root/coding/modelserver && go build ./internal/types/...`
Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
cd /root/coding/modelserver
git add internal/types/upstream.go
git commit -m "feat(types): add ProviderCodex constant for codex upstream"
```

---

### Task 2: `CodexCredentials` type + JWT account-id extraction

**Files:**
- Create: `internal/proxy/codex_oauth.go`
- Test: `internal/proxy/codex_oauth_test.go`

- [ ] **Step 1: Create the test file with credential-parse + JWT-claim tests**

Write `internal/proxy/codex_oauth_test.go`:

```go
package proxy

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCodexAccessTokenAndAccount_RawAccessToken(t *testing.T) {
	// When the input is a bare token (not JSON), return it as the access
	// token and an empty account ID.
	at, acct := ParseCodexAccessTokenAndAccount("plain-bearer-token")
	if at != "plain-bearer-token" {
		t.Errorf("access token = %q, want %q", at, "plain-bearer-token")
	}
	if acct != "" {
		t.Errorf("account id = %q, want empty", acct)
	}
}

func TestParseCodexAccessTokenAndAccount_JSONBlob(t *testing.T) {
	creds := CodexCredentials{
		AccessToken:      "at-xyz",
		ChatGPTAccountID: "org_123",
	}
	raw, _ := json.Marshal(creds)
	at, acct := ParseCodexAccessTokenAndAccount(string(raw))
	if at != "at-xyz" {
		t.Errorf("access token = %q, want %q", at, "at-xyz")
	}
	if acct != "org_123" {
		t.Errorf("account id = %q, want %q", acct, "org_123")
	}
}

func TestExtractChatGPTAccountIDFromIDToken(t *testing.T) {
	// Build a fake JWT (header.payload.signature) where the payload contains
	// the OpenAI custom-namespace claim with chatgpt_account_id.
	payload := map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "org_workspace_42",
		},
	}
	payloadJSON, _ := json.Marshal(payload)
	encoded := base64.RawURLEncoding.EncodeToString(payloadJSON)
	idToken := "header." + encoded + ".signature"

	got := extractChatGPTAccountIDFromIDToken(idToken)
	if got != "org_workspace_42" {
		t.Errorf("got %q, want %q", got, "org_workspace_42")
	}
}

func TestExtractChatGPTAccountIDFromIDToken_MissingClaim(t *testing.T) {
	payload := map[string]any{"sub": "user1"}
	payloadJSON, _ := json.Marshal(payload)
	encoded := base64.RawURLEncoding.EncodeToString(payloadJSON)
	idToken := "h." + encoded + ".s"

	if got := extractChatGPTAccountIDFromIDToken(idToken); got != "" {
		t.Errorf("expected empty account id, got %q", got)
	}
}

func TestExtractChatGPTAccountIDFromIDToken_Malformed(t *testing.T) {
	cases := []string{"", "not.enough", "garbage", "h." + strings.Repeat("!", 4) + ".s"}
	for _, c := range cases {
		if got := extractChatGPTAccountIDFromIDToken(c); got != "" {
			t.Errorf("input %q: expected empty, got %q", c, got)
		}
	}
}
```

- [ ] **Step 2: Run test — expect compile failure**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestParseCodexAccessTokenAndAccount -v`
Expected: FAIL, `undefined: CodexCredentials` and `undefined: ParseCodexAccessTokenAndAccount` and `undefined: extractChatGPTAccountIDFromIDToken`.

- [ ] **Step 3: Create `internal/proxy/codex_oauth.go` with the type + parser stubs**

```go
package proxy

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/modelserver/modelserver/internal/store"
)

const (
	// CodexClientID is the public OAuth client id used by the codex CLI.
	CodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// CodexIssuerURL is the OpenAI auth issuer.
	CodexIssuerURL = "https://auth.openai.com"
	// CodexAuthURL is the OAuth authorize endpoint.
	CodexAuthURL = CodexIssuerURL + "/oauth/authorize"
	// CodexTokenURL is the OAuth token endpoint.
	CodexTokenURL = CodexIssuerURL + "/oauth/token"
	// CodexScopes is the scope list used by the codex CLI authorize flow.
	CodexScopes = "openid profile email offline_access"
	// codexExpiryBuffer triggers proactive refresh this many seconds before expiry.
	codexExpiryBuffer = 300
)

// CodexCredentials holds OAuth credentials for a codex upstream.
type CodexCredentials struct {
	IDToken          string `json:"id_token,omitempty"`
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ChatGPTAccountID string `json:"chatgpt_account_id,omitempty"`
	ExpiresAt        int64  `json:"expires_at"`
	ClientID         string `json:"client_id,omitempty"`
}

// CodexOAuthTokenManager manages OAuth tokens for codex upstreams.
type CodexOAuthTokenManager struct {
	mu            sync.RWMutex
	credentials   map[string]*CodexCredentials // upstreamID → credentials
	store         *store.Store
	encryptionKey []byte
	logger        *slog.Logger
	sfGroup       singleflight.Group
	httpClient    *http.Client
	tokenURL      string
}

// NewCodexOAuthTokenManager constructs a manager. Pass nil store / nil key
// in tests that don't exercise the persistence path.
func NewCodexOAuthTokenManager(st *store.Store, encKey []byte, logger *slog.Logger) *CodexOAuthTokenManager {
	return &CodexOAuthTokenManager{
		credentials:   make(map[string]*CodexCredentials),
		store:         st,
		encryptionKey: encKey,
		logger:        logger,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		tokenURL:      CodexTokenURL,
	}
}

// ParseCodexAccessTokenAndAccount accepts either a bare access token or a
// CodexCredentials JSON blob. Returns the bare token and account id; on a
// bare token the account id is empty.
func ParseCodexAccessTokenAndAccount(raw string) (accessToken, accountID string) {
	if len(raw) == 0 || raw[0] != '{' {
		return raw, ""
	}
	var creds CodexCredentials
	if json.Unmarshal([]byte(raw), &creds) != nil {
		return raw, ""
	}
	return creds.AccessToken, creds.ChatGPTAccountID
}

// extractChatGPTAccountIDFromIDToken decodes the middle segment of the JWT
// and returns the chatgpt_account_id claim from the OpenAI custom-namespace
// object. Returns empty when the claim is missing or the token is unparseable.
// We do NOT verify the signature — the caller obtained this token from the
// issuer over TLS in the immediately preceding exchange, and we are only
// extracting an opaque identifier for routing purposes.
func extractChatGPTAccountIDFromIDToken(idToken string) string {
	parts := splitJWT(idToken)
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Auth.ChatGPTAccountID
}

func splitJWT(token string) []string {
	out := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			out = append(out, token[start:i])
			start = i + 1
		}
	}
	out = append(out, token[start:])
	return out
}
```

- [ ] **Step 4: Run tests — expect pass**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run "TestParseCodexAccessTokenAndAccount|TestExtractChatGPTAccountIDFromIDToken" -v`
Expected: all four tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/codex_oauth.go internal/proxy/codex_oauth_test.go
git commit -m "feat(proxy): add CodexCredentials type and id_token claim parser"
```

---

### Task 3: `CodexOAuthTokenManager.LoadCredentials` and `Reload`

**Files:**
- Modify: `internal/proxy/codex_oauth.go`
- Modify: `internal/proxy/codex_oauth_test.go`

- [ ] **Step 1: Add tests for LoadCredentials**

Append to `internal/proxy/codex_oauth_test.go`:

```go
import (
	"time"
	// ... existing imports already present; add "time" and "github.com/.../types" if missing
)

func TestCodex_LoadCredentials(t *testing.T) {
	mgr := NewCodexOAuthTokenManager(nil, nil, nil)

	creds := CodexCredentials{
		AccessToken:      "at-1",
		RefreshToken:     "rt-1",
		ChatGPTAccountID: "org_a",
		ExpiresAt:        time.Now().Add(time.Hour).Unix(),
	}
	credsJSON, _ := json.Marshal(creds)

	upstreams := []types.Upstream{
		{ID: "cx1", Provider: types.ProviderCodex},
		{ID: "ot2", Provider: types.ProviderOpenAI},
	}
	keys := map[string]string{
		"cx1": string(credsJSON),
		"ot2": "sk-...",
	}

	mgr.LoadCredentials(upstreams, keys)

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	if got := mgr.credentials["cx1"]; got == nil || got.AccessToken != "at-1" {
		t.Errorf("cx1 not loaded correctly: %+v", got)
	}
	if _, present := mgr.credentials["ot2"]; present {
		t.Error("non-codex upstream should not be loaded")
	}
}

func TestCodex_Reload_PreservesNewerInMemory(t *testing.T) {
	mgr := NewCodexOAuthTokenManager(nil, nil, nil)

	// Existing in-memory creds with later expiry (simulating a recent refresh
	// not yet persisted to DB).
	mgr.mu.Lock()
	mgr.credentials["cx1"] = &CodexCredentials{
		AccessToken: "fresh-at",
		ExpiresAt:   time.Now().Add(2 * time.Hour).Unix(),
	}
	mgr.mu.Unlock()

	// DB returns an older snapshot.
	old := CodexCredentials{
		AccessToken: "stale-at",
		ExpiresAt:   time.Now().Add(30 * time.Minute).Unix(),
	}
	oldJSON, _ := json.Marshal(old)
	mgr.Reload(
		[]types.Upstream{{ID: "cx1", Provider: types.ProviderCodex}},
		map[string]string{"cx1": string(oldJSON)},
	)

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	if mgr.credentials["cx1"].AccessToken != "fresh-at" {
		t.Errorf("Reload clobbered fresher in-memory token: %+v", mgr.credentials["cx1"])
	}
}
```

The file's import block must include `"github.com/modelserver/modelserver/internal/types"` and `"time"` — add them if missing.

- [ ] **Step 2: Run tests — expect compile failure**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestCodex_Load -v`
Expected: FAIL, `mgr.LoadCredentials undefined`.

- [ ] **Step 3: Implement LoadCredentials and Reload**

Append to `internal/proxy/codex_oauth.go`:

```go
import (
	// ...existing imports...
	"github.com/modelserver/modelserver/internal/types"
)

// LoadCredentials parses and stores credentials for all codex upstreams.
// Called at router init.
func (m *CodexOAuthTokenManager) LoadCredentials(upstreams []types.Upstream, decryptedKeys map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range upstreams {
		if u.Provider != types.ProviderCodex {
			continue
		}
		raw, ok := decryptedKeys[u.ID]
		if !ok || raw == "" {
			continue
		}
		var creds CodexCredentials
		if err := json.Unmarshal([]byte(raw), &creds); err != nil {
			if m.logger != nil {
				m.logger.Error("failed to parse codex credentials", "upstream_id", u.ID, "error", err)
			}
			continue
		}
		m.credentials[u.ID] = &creds
	}
}

// Reload re-loads credentials from the database, preserving any in-memory
// credentials whose ExpiresAt is later than what the DB has (these are
// freshly refreshed tokens that may not yet be persisted).
func (m *CodexOAuthTokenManager) Reload(upstreams []types.Upstream, decryptedKeys map[string]string) {
	newCreds := make(map[string]*CodexCredentials)
	for _, u := range upstreams {
		if u.Provider != types.ProviderCodex {
			continue
		}
		raw, ok := decryptedKeys[u.ID]
		if !ok || raw == "" {
			continue
		}
		var creds CodexCredentials
		if err := json.Unmarshal([]byte(raw), &creds); err != nil {
			if m.logger != nil {
				m.logger.Error("failed to parse codex credentials on reload", "upstream_id", u.ID, "error", err)
			}
			continue
		}
		newCreds[u.ID] = &creds
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, existing := range m.credentials {
		if dbCreds, ok := newCreds[id]; ok && existing.ExpiresAt > dbCreds.ExpiresAt {
			newCreds[id] = existing
		}
	}
	m.credentials = newCreds
}
```

- [ ] **Step 4: Run tests — expect pass**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestCodex_ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/codex_oauth.go internal/proxy/codex_oauth_test.go
git commit -m "feat(proxy): codex OAuth manager LoadCredentials + Reload"
```

---

### Task 4: `GetAccessToken`, `GetAccountID`, refresh, force-refresh

**Files:**
- Modify: `internal/proxy/codex_oauth.go`
- Modify: `internal/proxy/codex_oauth_test.go`

- [ ] **Step 1: Add tests for happy-path token retrieval and refresh round-trip**

Append to `internal/proxy/codex_oauth_test.go`:

```go
import (
	// ...existing...
	"net/http"
	"net/http/httptest"
)

func TestCodex_GetAccessToken_Valid(t *testing.T) {
	mgr := NewCodexOAuthTokenManager(nil, nil, nil)
	mgr.mu.Lock()
	mgr.credentials["cx1"] = &CodexCredentials{
		AccessToken: "valid",
		ExpiresAt:   time.Now().Add(time.Hour).Unix(),
	}
	mgr.mu.Unlock()
	tok, err := mgr.GetAccessToken("cx1")
	if err != nil || tok != "valid" {
		t.Errorf("got %q, %v", tok, err)
	}
}

func TestCodex_GetAccessToken_Missing(t *testing.T) {
	mgr := NewCodexOAuthTokenManager(nil, nil, nil)
	if _, err := mgr.GetAccessToken("nope"); err == nil {
		t.Error("expected error for unknown upstream")
	}
}

func TestCodex_GetAccountID(t *testing.T) {
	mgr := NewCodexOAuthTokenManager(nil, nil, nil)
	mgr.mu.Lock()
	mgr.credentials["cx1"] = &CodexCredentials{
		AccessToken:      "at",
		ChatGPTAccountID: "org_xyz",
		ExpiresAt:        time.Now().Add(time.Hour).Unix(),
	}
	mgr.mu.Unlock()
	got, err := mgr.GetAccountID("cx1")
	if err != nil || got != "org_xyz" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestCodex_RefreshToken_RoundTrip(t *testing.T) {
	// Build a fake auth.openai.com token endpoint that:
	//  - returns a NEW id_token containing chatgpt_account_id "org_after"
	//  - returns a new access_token, refresh_token, expires_in
	newPayload := map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "org_after",
		},
	}
	pj, _ := json.Marshal(newPayload)
	idToken := "h." + base64.RawURLEncoding.EncodeToString(pj) + ".s"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" {
			t.Errorf("grant_type = %v, want refresh_token", body["grant_type"])
		}
		if _, ok := body["scope"]; ok {
			t.Error("refresh request must NOT include 'scope'")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idToken,
			"access_token":  "new-at",
			"refresh_token": "new-rt",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	mgr := NewCodexOAuthTokenManager(nil, nil, nil)
	mgr.tokenURL = srv.URL
	mgr.mu.Lock()
	mgr.credentials["cx1"] = &CodexCredentials{
		AccessToken:      "old-at",
		RefreshToken:     "old-rt",
		ChatGPTAccountID: "org_before",
		ExpiresAt:        time.Now().Add(-time.Minute).Unix(), // already expired
	}
	mgr.mu.Unlock()

	tok, err := mgr.GetAccessToken("cx1")
	if err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}
	if tok != "new-at" {
		t.Errorf("token = %q, want new-at", tok)
	}
	mgr.mu.RLock()
	got := mgr.credentials["cx1"]
	mgr.mu.RUnlock()
	if got.RefreshToken != "new-rt" {
		t.Errorf("refresh token = %q, want new-rt", got.RefreshToken)
	}
	if got.ChatGPTAccountID != "org_after" {
		t.Errorf("account id = %q, want org_after", got.ChatGPTAccountID)
	}
}

func TestCodex_RefreshToken_PreservesAccountIDOnAbsentIDToken(t *testing.T) {
	// Token endpoint returns NO id_token. The account id must remain.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-at",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	mgr := NewCodexOAuthTokenManager(nil, nil, nil)
	mgr.tokenURL = srv.URL
	mgr.mu.Lock()
	mgr.credentials["cx1"] = &CodexCredentials{
		AccessToken:      "old-at",
		RefreshToken:     "old-rt",
		ChatGPTAccountID: "org_kept",
		ExpiresAt:        time.Now().Add(-time.Minute).Unix(),
	}
	mgr.mu.Unlock()

	if _, err := mgr.GetAccessToken("cx1"); err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}
	mgr.mu.RLock()
	got := mgr.credentials["cx1"]
	mgr.mu.RUnlock()
	if got.ChatGPTAccountID != "org_kept" {
		t.Errorf("account id was clobbered: %q", got.ChatGPTAccountID)
	}
	if got.RefreshToken != "old-rt" {
		t.Errorf("refresh token was clobbered: %q", got.RefreshToken)
	}
}

func TestCodex_ForceRefresh_BypassesBuffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "forced-at",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()

	mgr := NewCodexOAuthTokenManager(nil, nil, nil)
	mgr.tokenURL = srv.URL
	mgr.mu.Lock()
	mgr.credentials["cx1"] = &CodexCredentials{
		AccessToken:  "still-fresh",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(), // not expiring
	}
	mgr.mu.Unlock()

	tok, err := mgr.ForceRefreshAccessToken("cx1")
	if err != nil || tok != "forced-at" {
		t.Errorf("got %q, %v", tok, err)
	}
}
```

- [ ] **Step 2: Run tests — expect compile failure**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run "TestCodex_GetAccessToken|TestCodex_GetAccountID|TestCodex_Refresh|TestCodex_Force" -v`
Expected: FAIL, `mgr.GetAccessToken undefined` etc.

- [ ] **Step 3: Implement the manager methods**

Append to `internal/proxy/codex_oauth.go`:

```go
import (
	// ...existing...
	"bytes"
	"fmt"
	"io"

	"github.com/modelserver/modelserver/internal/crypto"
)

// GetAccessToken returns a valid access token, refreshing if within the
// expiry buffer. On refresh failure the previous token is returned (it may
// still work for a brief grace period).
func (m *CodexOAuthTokenManager) GetAccessToken(upstreamID string) (string, error) {
	m.mu.RLock()
	creds, ok := m.credentials[upstreamID]
	if !ok {
		m.mu.RUnlock()
		return "", fmt.Errorf("no credentials for codex upstream %s", upstreamID)
	}
	token := creds.AccessToken
	needsRefresh := time.Now().Unix() > creds.ExpiresAt-codexExpiryBuffer
	m.mu.RUnlock()

	if needsRefresh {
		_, err, _ := m.sfGroup.Do(upstreamID, func() (interface{}, error) {
			return nil, m.refreshToken(upstreamID)
		})
		if err != nil {
			if m.logger != nil {
				m.logger.Error("failed to refresh codex token", "upstream_id", upstreamID, "error", err)
			}
			return token, nil
		}
		m.mu.RLock()
		token = m.credentials[upstreamID].AccessToken
		m.mu.RUnlock()
	}
	return token, nil
}

// GetAccountID returns the ChatGPT account id (workspace) associated with
// this upstream, or an empty string if the credentials don't carry one.
// Returns an error only when the upstream is unknown.
func (m *CodexOAuthTokenManager) GetAccountID(upstreamID string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	creds, ok := m.credentials[upstreamID]
	if !ok {
		return "", fmt.Errorf("no credentials for codex upstream %s", upstreamID)
	}
	return creds.ChatGPTAccountID, nil
}

// ForceRefreshAccessToken unconditionally refreshes the token (ignoring the
// expiry buffer). Used by the executor to recover from upstream 401/403.
func (m *CodexOAuthTokenManager) ForceRefreshAccessToken(upstreamID string) (string, error) {
	_, err, _ := m.sfGroup.Do("force:"+upstreamID, func() (interface{}, error) {
		return nil, m.refreshToken(upstreamID)
	})
	if err != nil {
		return "", fmt.Errorf("forced codex token refresh failed: %w", err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	creds, ok := m.credentials[upstreamID]
	if !ok {
		return "", fmt.Errorf("no credentials after refresh for upstream %s", upstreamID)
	}
	return creds.AccessToken, nil
}

// refreshToken posts to the OAuth token endpoint and merges the response
// into the in-memory credentials. id_token / access_token / refresh_token
// are all optional in the response per codex CLI behaviour; absent fields
// preserve their previous values. The chatgpt_account_id is re-extracted
// only when a new id_token is returned.
func (m *CodexOAuthTokenManager) refreshToken(upstreamID string) error {
	m.mu.RLock()
	creds, ok := m.credentials[upstreamID]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("no credentials for upstream %s", upstreamID)
	}
	refreshToken := creds.RefreshToken
	clientID := creds.ClientID
	m.mu.RUnlock()

	if clientID == "" {
		clientID = CodexClientID
	}
	body, _ := json.Marshal(map[string]string{
		"client_id":     clientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})

	resp, err := m.httpClient.Post(m.tokenURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("oauth token request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("codex token refresh returned %d: %s", resp.StatusCode, string(respBody))
	}

	var tokenResp struct {
		IDToken      *string `json:"id_token"`
		AccessToken  *string `json:"access_token"`
		RefreshToken *string `json:"refresh_token"`
		ExpiresIn    int64   `json:"expires_in"`
	}
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return fmt.Errorf("failed to parse codex token response: %w", err)
	}

	m.mu.Lock()
	cur := m.credentials[upstreamID]
	updated := *cur
	if tokenResp.AccessToken != nil {
		updated.AccessToken = *tokenResp.AccessToken
	}
	if tokenResp.RefreshToken != nil {
		updated.RefreshToken = *tokenResp.RefreshToken
	}
	if tokenResp.IDToken != nil {
		updated.IDToken = *tokenResp.IDToken
		if id := extractChatGPTAccountIDFromIDToken(*tokenResp.IDToken); id != "" {
			updated.ChatGPTAccountID = id
		}
	}
	if tokenResp.ExpiresIn > 0 {
		updated.ExpiresAt = time.Now().Unix() + tokenResp.ExpiresIn
	}
	updated.ClientID = clientID
	m.credentials[upstreamID] = &updated
	m.mu.Unlock()

	if m.store != nil && len(m.encryptionKey) > 0 {
		blob, err := json.Marshal(updated)
		if err != nil {
			return fmt.Errorf("failed to marshal refreshed codex credentials: %w", err)
		}
		enc, err := crypto.Encrypt(m.encryptionKey, blob)
		if err != nil {
			return fmt.Errorf("failed to encrypt refreshed codex credentials: %w", err)
		}
		if err := m.store.UpdateUpstream(upstreamID, map[string]interface{}{
			"api_key_encrypted": enc,
		}); err != nil && m.logger != nil {
			m.logger.Error("failed to persist refreshed codex token", "upstream_id", upstreamID, "error", err)
		}
	}
	if m.logger != nil {
		m.logger.Info("refreshed codex token", "upstream_id", upstreamID)
	}
	return nil
}
```

- [ ] **Step 4: Run tests — expect pass**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestCodex_ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/codex_oauth.go internal/proxy/codex_oauth_test.go
git commit -m "feat(proxy): codex OAuth manager Get/Refresh/ForceRefresh"
```

---

### Task 5: `directorSetCodexUpstream` — outbound director

**Files:**
- Create: `internal/proxy/codex.go`
- Test: `internal/proxy/codex_test.go`

- [ ] **Step 1: Write the test file with golden header assertions**

Create `internal/proxy/codex_test.go`:

```go
package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDirectorSetCodexUpstream_DefaultBaseURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader("{}"))
	r.Header.Set("x-api-key", "sk-leak")
	r.Header.Set("authorization", "Bearer client-token")

	directorSetCodexUpstream(r, "", "fresh-token", "org_42", "up-1")

	if r.URL.Host != "chatgpt.com" {
		t.Errorf("Host = %q, want chatgpt.com", r.URL.Host)
	}
	if r.URL.Path != "/backend-api/codex/v1/responses" {
		t.Errorf("Path = %q, want /backend-api/codex/v1/responses", r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer fresh-token" {
		t.Errorf("Authorization = %q, want Bearer fresh-token", got)
	}
	if got := r.Header.Get("ChatGPT-Account-ID"); got != "org_42" {
		t.Errorf("ChatGPT-Account-ID = %q, want org_42", got)
	}
	if r.Header.Get("x-api-key") != "" {
		t.Error("x-api-key was not stripped")
	}
	if got := r.Header.Get("Version"); got != codexVersion {
		t.Errorf("Version = %q, want %q", got, codexVersion)
	}
	if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "codex_cli_rs/") {
		t.Errorf("User-Agent = %q, want codex_cli_rs/* prefix", got)
	}
	if got := r.Header.Get("session_id"); got == "" {
		t.Error("session_id should be auto-filled")
	}
	if r.Header.Get("OpenAI-Beta") != "" {
		t.Error("OpenAI-Beta must NOT be sent on HTTP /responses")
	}
}

func TestDirectorSetCodexUpstream_PreservesClientSessionID(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	r.Header.Set("session_id", "client-supplied-uuid")
	directorSetCodexUpstream(r, "", "tok", "org_1", "up-1")
	if got := r.Header.Get("session_id"); got != "client-supplied-uuid" {
		t.Errorf("session_id = %q, want client-supplied-uuid", got)
	}
}

func TestDirectorSetCodexUpstream_OmitsAccountIDWhenEmpty(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	directorSetCodexUpstream(r, "", "tok", "", "up-1")
	if v := r.Header.Get("ChatGPT-Account-ID"); v != "" {
		t.Errorf("expected no ChatGPT-Account-ID header, got %q", v)
	}
}

func TestDirectorSetCodexUpstream_CustomBaseURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	directorSetCodexUpstream(r, "https://example.com/api", "tok", "org_1", "up-1")
	if r.URL.Host != "example.com" {
		t.Errorf("Host = %q, want example.com", r.URL.Host)
	}
	if r.URL.Path != "/api/v1/responses" {
		t.Errorf("Path = %q, want /api/v1/responses", r.URL.Path)
	}
}

func TestSanitizeOutboundHeaders_PassesCodexHeaders(t *testing.T) {
	in := http.Header{
		"Authorization":      {"Bearer x"},
		"Chatgpt-Account-Id": {"org_1"},
		"Version":            {"0.55.0"},
		"Session_id":         {"uuid"},
		"X-Codex-Window-Id":  {"win-1"},
		"X-Random-Garbage":   {"drop me"},
	}
	out := sanitizeOutboundHeaders(in)
	for _, want := range []string{"Authorization", "Chatgpt-Account-Id", "Version", "Session_id", "X-Codex-Window-Id"} {
		if out.Get(want) == "" {
			t.Errorf("expected header %q to pass through", want)
		}
	}
	if out.Get("X-Random-Garbage") != "" {
		t.Error("non-allowlisted header leaked through")
	}
}
```

- [ ] **Step 2: Run tests — expect compile failure**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestDirectorSetCodexUpstream -v`
Expected: FAIL, `directorSetCodexUpstream undefined`, `codexVersion undefined`.

- [ ] **Step 3: Implement `internal/proxy/codex.go`**

```go
package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"path"
)

// codex outbound constants. Pinned to a recent codex CLI release at
// implementation time; bumping is a deliberate maintenance task.
const (
	codexDefaultBaseURL = "https://chatgpt.com/backend-api/codex"
	// The originator string ("codex_cli_rs") is part of the User-Agent
	// prefix per codex CLI's get_codex_user_agent(); it is NOT sent as a
	// standalone header.
	codexVersion   = "0.55.0"
	codexUserAgent = "codex_cli_rs/0.55.0 (Linux; x64) Codex"
)

// directorSetCodexUpstream rewrites the request to target the codex backend
// using the supplied access token. apiKey resolution / refresh is the
// caller's responsibility (Executor uses CodexOAuthTokenManager).
func directorSetCodexUpstream(req *http.Request, baseURL, accessToken, accountID, upstreamID string) {
	req.URL.Scheme = "https"
	target := codexDefaultBaseURL
	if baseURL != "" {
		target = baseURL
	}
	if u, err := url.Parse(target); err == nil {
		req.URL.Scheme = u.Scheme
		req.URL.Host = u.Host
		if u.Path != "" && u.Path != "/" {
			req.URL.Path = path.Join(u.Path, req.URL.Path)
		}
	}
	req.Host = req.URL.Host

	// Strip client-supplied auth so we don't leak sk-... keys to the
	// ChatGPT backend.
	req.Header.Del("x-api-key")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}

	// Codex fingerprint headers always overwrite — the backend gates
	// access on these.
	req.Header.Set("User-Agent", codexUserAgent)
	req.Header.Set("Version", codexVersion)
	req.Header.Set("Connection", "keep-alive")

	// session_id: preserve whatever the client provided; otherwise fill
	// a fresh random UUID-style value.  Header NAME is lowercase with an
	// underscore (matches codex CLI's build_conversation_headers).
	if req.Header.Get("session_id") == "" {
		req.Header.Set("session_id", randomCodexSessionID())
	}
}

// randomCodexSessionID returns a 32-hex-char value used as a fallback
// session_id when the client didn't supply one. Cheaper than a real UUIDv4
// formatter and equally opaque to the upstream.
func randomCodexSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
```

- [ ] **Step 4: Extend `sanitizeOutboundHeaders`**

In `internal/proxy/executor.go`, find the `switch` block in `sanitizeOutboundHeaders` (~line 1273) and add codex entries. The block currently ends with `canon == "X-Goog-Api-Key":`. Replace that line and the lines around it with:

```go
		case canon == "Content-Type",
			canon == "User-Agent",
			canon == "X-App",
			canon == "Anthropic-Beta",
			canon == "Anthropic-Dangerous-Direct-Browser-Access",
			canon == "Anthropic-Version",
			canon == "X-Api-Key",
			canon == "Authorization",
			// Claude Code client headers for analytics and request correlation.
			canon == "X-Claude-Code-Session-Id",
			canon == "X-Client-Request-Id",
			canon == "X-Client-App",
			canon == "X-Anthropic-Additional-Protection",
			canon == "X-Claude-Remote-Container-Id",
			canon == "X-Claude-Remote-Session-Id",
			// Gemini API key header.
			canon == "X-Goog-Api-Key",
			// Codex (ChatGPT subscription) headers.
			canon == "Chatgpt-Account-Id",
			canon == "Version",
			canon == "Session_id":
			allowed[canon] = vals
		default:
			if strings.HasPrefix(canon, "X-Stainless-") || strings.HasPrefix(canon, "X-Codex-") {
				allowed[canon] = vals
			}
		}
```

- [ ] **Step 5: Run tests — expect pass**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run "TestDirectorSetCodexUpstream|TestSanitizeOutboundHeaders_PassesCodex" -v`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/codex.go internal/proxy/codex_test.go internal/proxy/executor.go
git commit -m "feat(proxy): directorSetCodexUpstream + extend header allowlist"
```

---

### Task 6: `CodexTransformer` (provider plug-in)

**Files:**
- Create: `internal/proxy/provider_codex.go`
- Modify: `internal/proxy/provider_transform.go:57-66`
- Test: `internal/proxy/provider_codex_test.go`

- [ ] **Step 1: Write the transformer test**

Create `internal/proxy/provider_codex_test.go`:

```go
package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestCodexTransformer_SetUpstream_RawToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	tr := &CodexTransformer{}
	if err := tr.SetUpstream(r, &types.Upstream{ID: "u1"}, "raw-token"); err != nil {
		t.Fatalf("SetUpstream: %v", err)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer raw-token" {
		t.Errorf("Authorization = %q, want Bearer raw-token", got)
	}
	if r.Header.Get("ChatGPT-Account-ID") != "" {
		t.Error("expected no account id header for raw-token path")
	}
}

func TestCodexTransformer_SetUpstream_JSONBlob(t *testing.T) {
	creds := CodexCredentials{
		AccessToken:      "blob-at",
		ChatGPTAccountID: "org_blob",
	}
	raw, _ := json.Marshal(creds)
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	tr := &CodexTransformer{}
	if err := tr.SetUpstream(r, &types.Upstream{ID: "u1"}, string(raw)); err != nil {
		t.Fatalf("SetUpstream: %v", err)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer blob-at" {
		t.Errorf("Authorization = %q, want Bearer blob-at", got)
	}
	if got := r.Header.Get("ChatGPT-Account-ID"); got != "org_blob" {
		t.Errorf("ChatGPT-Account-ID = %q, want org_blob", got)
	}
}

func TestCodexTransformer_TransformBody_PassThrough(t *testing.T) {
	tr := &CodexTransformer{}
	in := []byte(`{"model":"gpt-5","input":"hi"}`)
	out, err := tr.TransformBody(in, "gpt-5", true, http.Header{})
	if err != nil {
		t.Fatalf("TransformBody: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("body modified: got %s", string(out))
	}
}

func TestGetProviderTransformer_Codex(t *testing.T) {
	got := GetProviderTransformer(types.ProviderCodex)
	if _, ok := got.(*CodexTransformer); !ok {
		t.Errorf("GetProviderTransformer(codex) = %T, want *CodexTransformer", got)
	}
}
```

- [ ] **Step 2: Run tests — expect compile failure**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestCodexTransformer -v`
Expected: FAIL, `undefined: CodexTransformer`.

- [ ] **Step 3: Create `internal/proxy/provider_codex.go`**

```go
package proxy

import (
	"io"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// CodexTransformer handles ChatGPT-subscription codex requests. The wire
// format is OpenAI Responses API; only the auth + a few fingerprint headers
// differ. Body / non-stream parser / stream interceptor reuse OpenAI logic.
type CodexTransformer struct{}

var _ ProviderTransformer = (*CodexTransformer)(nil)

// TransformBody is a pass-through (same as OpenAITransformer).
func (t *CodexTransformer) TransformBody(body []byte, _ string, _ bool, _ http.Header) ([]byte, error) {
	return body, nil
}

// SetUpstream configures the outbound request. apiKey is either a raw access
// token (preferred — set by the Executor via CodexOAuthTokenManager) or a
// CodexCredentials JSON blob (fallback for cold-start before the manager has
// loaded).
func (t *CodexTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
	accessToken, accountID := ParseCodexAccessTokenAndAccount(apiKey)
	directorSetCodexUpstream(r, upstream.BaseURL, accessToken, accountID, upstream.ID)
	return nil
}

// WrapStream reuses the OpenAI Responses SSE interceptor.
func (t *CodexTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
	return newOpenAIStreamInterceptor(body, startTime, "", func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64) {
		onComplete(StreamMetrics{
			Model:               model,
			MsgID:               respID,
			InputTokens:         inputTokens,
			OutputTokens:        outputTokens,
			CacheCreationTokens: 0,
			CacheReadTokens:     cacheReadTokens,
			TTFTMs:              ttft,
		})
	})
}

// ParseResponse parses non-streaming OpenAI Responses bodies (same as OpenAITransformer).
func (t *CodexTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
	model, respID, usage, err := ParseOpenAINonStreamingResponse(body)
	if err != nil {
		return nil, err
	}
	cached := usage.InputTokensDetails.CachedTokens
	input := usage.InputTokens - cached
	if input < 0 {
		input = 0
	}
	return &ResponseMetrics{
		Model:               model,
		MsgID:               respID,
		InputTokens:         input,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: 0,
		CacheReadTokens:     cached,
	}, nil
}
```

- [ ] **Step 4: Register the transformer in `provider_transform.go`**

In `internal/proxy/provider_transform.go`, find the `init()` function (line 57) and add a registration line at the end (after `VertexOpenAITransformer`):

```go
	providerTransformers[types.ProviderCodex] = &CodexTransformer{}
```

- [ ] **Step 5: Run tests — expect pass**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run "TestCodexTransformer|TestGetProviderTransformer_Codex" -v`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/provider_codex.go internal/proxy/provider_codex_test.go internal/proxy/provider_transform.go
git commit -m "feat(proxy): CodexTransformer registered as ProviderCodex"
```

---

### Task 7: Wire `CodexOAuthTokenManager` into `Router`

**Files:**
- Modify: `internal/proxy/router_engine.go` (Router struct ~line 56, NewRouter signature ~line 80, buildMaps ~line 220, add three accessor methods after `ForceRefreshClaudeCodeAccessToken`)
- Modify: `cmd/modelserver/main.go:153-154`
- Test: `internal/proxy/codex_oauth_test.go` (add Router-level test)

- [ ] **Step 1: Add Router-level test**

Append to `internal/proxy/codex_oauth_test.go`:

```go
func TestRouter_GetCodexAccessToken(t *testing.T) {
	mgr := NewCodexOAuthTokenManager(nil, nil, nil)
	mgr.mu.Lock()
	mgr.credentials["cx1"] = &CodexCredentials{
		AccessToken:      "valid",
		ChatGPTAccountID: "org_1",
		ExpiresAt:        time.Now().Add(time.Hour).Unix(),
	}
	mgr.mu.Unlock()

	r := &Router{codexOAuthMgr: mgr}
	tok, err := r.GetCodexAccessToken("cx1")
	if err != nil || tok != "valid" {
		t.Errorf("token = %q, err = %v", tok, err)
	}
	id, err := r.GetCodexAccountID("cx1")
	if err != nil || id != "org_1" {
		t.Errorf("account id = %q, err = %v", id, err)
	}
}

func TestRouter_GetCodexAccessToken_NoManager(t *testing.T) {
	r := &Router{}
	if _, err := r.GetCodexAccessToken("cx1"); err == nil {
		t.Error("expected error when manager is nil")
	}
}
```

- [ ] **Step 2: Run test — expect compile failure**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestRouter_GetCodex -v`
Expected: FAIL, `Router has no field codexOAuthMgr`.

- [ ] **Step 3: Add `codexOAuthMgr` field and accessors to Router**

In `internal/proxy/router_engine.go`, modify the `Router` struct — after the existing `oauthMgr *OAuthTokenManager` line (~line 56) add:

```go
	codexOAuthMgr *CodexOAuthTokenManager
```

Update `NewRouter`'s signature (~line 80) to accept the new manager:

```go
func NewRouter(
	upstreams []types.Upstream,
	groups []store.UpstreamGroupWithMembers,
	routes []types.Route,
	encKey []byte,
	logger *slog.Logger,
	sessionTTL time.Duration,
	oauthMgr *OAuthTokenManager,
	codexOAuthMgr *CodexOAuthTokenManager,
	catalog modelcatalog.Catalog,
) *Router {
```

Inside `NewRouter`, when initialising the struct, add:

```go
	r := &Router{
		sessionTTL:    sessionTTL,
		logger:        logger,
		oauthMgr:      oauthMgr,
		codexOAuthMgr: codexOAuthMgr,
		catalog:       catalog,
	}
```

In `buildMaps`, immediately after the existing `if r.oauthMgr != nil { r.oauthMgr.Reload(...) }` block, add the codex equivalent:

```go
	if r.codexOAuthMgr != nil {
		r.codexOAuthMgr.Reload(upstreams, keys)
	}
```

(Both `LoadCredentials` and `Reload` work for first-load — `Reload` collapses to the same behaviour when the in-memory map is empty, so a single call covers both init and periodic reloads.)

After the existing `ForceRefreshClaudeCodeAccessToken` method (~line 540), add three Codex sibling methods:

```go
// GetCodexAccessToken returns a fresh access token for a codex upstream,
// refreshing if needed.
func (r *Router) GetCodexAccessToken(upstreamID string) (string, error) {
	if r.codexOAuthMgr == nil {
		return "", fmt.Errorf("CodexOAuthTokenManager not configured")
	}
	return r.codexOAuthMgr.GetAccessToken(upstreamID)
}

// GetCodexAccountID returns the ChatGPT-Account-ID for a codex upstream
// (may be empty for accounts without a workspace).
func (r *Router) GetCodexAccountID(upstreamID string) (string, error) {
	if r.codexOAuthMgr == nil {
		return "", fmt.Errorf("CodexOAuthTokenManager not configured")
	}
	return r.codexOAuthMgr.GetAccountID(upstreamID)
}

// ForceRefreshCodexAccessToken bypasses the expiry buffer to recover from
// upstream 401/403.
func (r *Router) ForceRefreshCodexAccessToken(upstreamID string) (string, error) {
	if r.codexOAuthMgr == nil {
		return "", fmt.Errorf("CodexOAuthTokenManager not configured")
	}
	return r.codexOAuthMgr.ForceRefreshAccessToken(upstreamID)
}
```

- [ ] **Step 4: Update the call site in `cmd/modelserver/main.go`**

Modify lines 153-154:

```go
	oauthMgr := proxy.NewOAuthTokenManager(st, encryptionKey, logger)
	codexOAuthMgr := proxy.NewCodexOAuthTokenManager(st, encryptionKey, logger)
	router := proxy.NewRouter(upstreams, groups, routingRoutes, encryptionKey, logger, cfg.Trace.SessionTTL, oauthMgr, codexOAuthMgr, catalog)
```

- [ ] **Step 5: Run unit + build**

Run: `cd /root/coding/modelserver && go build ./... && go test ./internal/proxy/ -run TestRouter_GetCodex -v`
Expected: build succeeds; tests PASS.

- [ ] **Step 6: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/router_engine.go internal/proxy/codex_oauth_test.go cmd/modelserver/main.go
git commit -m "feat(proxy): wire CodexOAuthTokenManager into Router"
```

---

### Task 8: Executor — token resolution and 401/403 force-refresh

**Files:**
- Modify: `internal/proxy/executor.go` (token resolution branch ~line 359, 401/403 retry block after the existing claudecode block ~line 456)

- [ ] **Step 1: Add codex token-resolution branch**

In `internal/proxy/executor.go`, locate the existing token-resolution block:

```go
		apiKeyForUpstream := candidate.APIKey
		if upstream.Provider == types.ProviderClaudeCode {
			if token, err := e.router.GetClaudeCodeAccessToken(upstream.ID); err == nil {
				apiKeyForUpstream = token
			} else {
				logger.Warn("claudecode token resolution failed, falling back to stored key", "error", err)
			}
		}
```

Add a sibling block immediately after:

```go
		if upstream.Provider == types.ProviderCodex {
			if token, err := e.router.GetCodexAccessToken(upstream.ID); err == nil {
				accountID, _ := e.router.GetCodexAccountID(upstream.ID)
				blob, _ := json.Marshal(map[string]string{
					"access_token":       token,
					"chatgpt_account_id": accountID,
				})
				apiKeyForUpstream = string(blob)
			} else {
				logger.Warn("codex token resolution failed, falling back to stored key", "error", err)
			}
		}
```

If `encoding/json` is not yet imported in this file, add it. (It is — search confirms json import already present.)

- [ ] **Step 2: Add the codex 401/403 retry block**

Right above the loop's per-attempt `continue` at the bottom, find the `claudeCodeOAuthRetried` block. After that block (which ends after the `outReq` rebuild), add a parallel `codexOAuthRetried` block.

First, near the top of the retry loop where `claudeCodeOAuthRetried := false` is declared (~line 235), add a sibling:

```go
	codexOAuthRetried := false
```

Then duplicate the entire claudecode 401/403 block (lines ~456-510) for codex. Place it immediately after the claudecode block. The codex version:

```go
		if upstream.Provider == types.ProviderCodex && resp != nil &&
			(resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) &&
			!codexOAuthRetried {
			codexOAuthRetried = true

			io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			e.router.ConnTracker().Release(upstream.ID)
			if cancelFn != nil {
				cancelFn()
			}

			newToken, refreshErr := e.router.ForceRefreshCodexAccessToken(upstream.ID)
			if refreshErr != nil {
				logger.Warn("codex OAuth refresh failed on 401/403, returning original error", "error", refreshErr)
				writeProxyError(w, resp.StatusCode, "upstream authentication failed")
				if reqCtx.RequestID != "" {
					duration := time.Since(startTime).Milliseconds()
					failReq := types.Request{
						OAuthGrantID: reqCtx.OAuthGrantID,
						Status:       types.RequestStatusError,
						LatencyMs:    duration,
						ErrorMessage: "codex OAuth refresh failed",
						ClientIP:     reqCtx.ClientIP,
					}
					go func() {
						if err := e.store.CompleteRequest(reqCtx.RequestID, &failReq); err != nil {
							e.logger.Error("failed to complete request", "request_id", reqCtx.RequestID, "error", err)
						}
					}()
				}
				return
			}

			logger.Info("retrying codex request after OAuth token refresh", "upstream_id", upstream.ID)

			retryReq, _ := http.NewRequestWithContext(r.Context(), r.Method, outReq.URL.String(), io.NopCloser(bytes.NewReader(transformedBody)))
			retryReq.ContentLength = int64(len(transformedBody))
			retryReq.Host = outReq.Host
			retryReq.Header = outReq.Header.Clone()
			retryReq.Header.Set("Authorization", "Bearer "+newToken)
			if accountID, _ := e.router.GetCodexAccountID(upstream.ID); accountID != "" {
				retryReq.Header.Set("ChatGPT-Account-ID", accountID)
			}

			e.router.ConnTracker().Acquire(upstream.ID)
			retryAttemptCtx := retryReq.Context()
			var retryCancel context.CancelFunc
			if timeout := upstreamTimeout(upstream, reqCtx.IsStream); timeout > 0 {
				retryAttemptCtx, retryCancel = context.WithTimeout(retryAttemptCtx, timeout)
			}
			retryReq = retryReq.WithContext(retryAttemptCtx)
			resp, doErr = e.httpClient.Do(retryReq)
			if retryCancel != nil && doErr != nil {
				retryCancel()
			}
			outReq = retryReq
			cancelFn = retryCancel
			result = e.evaluateResponse(resp, doErr, retryPolicy)
			if result == proxyResultRetryable {
				e.router.ConnTracker().Release(upstream.ID)
				e.router.CircuitBreaker().RecordFailure(upstream.ID)
				e.router.Metrics().RecordError(upstream.ID)
				if cancelFn != nil {
					cancelFn()
				}
				continue
			}
		}
```

(If the precise field/method spellings on the claudecode block differ from the snippet above, mirror them exactly — the safe way is to copy the existing claudecode block and replace `ClaudeCode` with `Codex` and `claudeCodeOAuthRetried` with `codexOAuthRetried`.)

- [ ] **Step 3: Build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: build succeeds.

- [ ] **Step 4: Run executor tests**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -count=1`
Expected: all existing tests still pass (no codex coverage added here yet — those will come in admin tests).

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/proxy/executor.go
git commit -m "feat(proxy): codex token resolution and 401/403 force-refresh in Executor"
```

---

### Task 9: Admin OAuth handlers — start, exchange

**Files:**
- Create: `internal/admin/handle_codex_oauth.go`
- Test: `internal/admin/handle_codex_oauth_test.go`

- [ ] **Step 1: Write the start-handler test**

Create `internal/admin/handle_codex_oauth_test.go`:

```go
package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelserver/modelserver/internal/proxy"
)

func TestHandleCodexOAuthStart(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams/codex/oauth/start", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleCodexOAuthStart()(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			AuthURL      string `json:"auth_url"`
			State        string `json:"state"`
			CodeVerifier string `json:"code_verifier"`
			RedirectURI  string `json:"redirect_uri"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.AuthURL == "" || resp.Data.State == "" || resp.Data.CodeVerifier == "" {
		t.Fatalf("missing fields in %+v", resp.Data)
	}
	u, err := url.Parse(resp.Data.AuthURL)
	if err != nil {
		t.Fatalf("auth_url parse: %v", err)
	}
	if u.Host != "auth.openai.com" {
		t.Errorf("auth_url host = %q, want auth.openai.com", u.Host)
	}
	q := u.Query()
	if q.Get("client_id") != proxy.CodexClientID {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", q.Get("code_challenge_method"))
	}
	if q.Get("id_token_add_organizations") != "true" {
		t.Errorf("id_token_add_organizations = %q", q.Get("id_token_add_organizations"))
	}
	if q.Get("codex_cli_simplified_flow") != "true" {
		t.Errorf("codex_cli_simplified_flow = %q", q.Get("codex_cli_simplified_flow"))
	}
	if q.Get("originator") != "codex_cli_rs" {
		t.Errorf("originator = %q", q.Get("originator"))
	}
	if q.Get("scope") != proxy.CodexScopes {
		t.Errorf("scope = %q", q.Get("scope"))
	}
}

func TestHandleCodexOAuthExchange(t *testing.T) {
	// Stub auth.openai.com /oauth/token returning id_token with account claim.
	idTokenPayload := `{"https://api.openai.com/auth":{"chatgpt_account_id":"org_test"}}`
	encoded := base64URL(idTokenPayload)
	idToken := "h." + encoded + ".s"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want form-urlencoded", ct)
		}
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.PostForm.Get("grant_type"))
		}
		if r.PostForm.Get("code") != "the-code" {
			t.Errorf("code = %q", r.PostForm.Get("code"))
		}
		if r.PostForm.Get("code_verifier") != "the-verifier" {
			t.Errorf("code_verifier = %q", r.PostForm.Get("code_verifier"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id_token":      idToken,
			"access_token":  "fresh-at",
			"refresh_token": "fresh-rt",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	// Override the package-level token URL (set up via init in handler).
	prev := codexOAuthTokenURL
	codexOAuthTokenURL = srv.URL
	defer func() { codexOAuthTokenURL = prev }()

	body := `{"callback_url":"http://localhost:1455/auth/callback?code=the-code&state=s","code_verifier":"the-verifier","state":"s"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upstreams/codex/oauth/exchange", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleCodexOAuthExchange()(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data proxy.CodexCredentials `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.AccessToken != "fresh-at" {
		t.Errorf("access_token = %q", resp.Data.AccessToken)
	}
	if resp.Data.ChatGPTAccountID != "org_test" {
		t.Errorf("chatgpt_account_id = %q", resp.Data.ChatGPTAccountID)
	}
}

// base64URL is a tiny helper for tests to avoid importing encoding/base64 with
// padding handling.
func base64URL(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	src := []byte(s)
	var out []byte
	i := 0
	for ; i+3 <= len(src); i += 3 {
		v := uint32(src[i])<<16 | uint32(src[i+1])<<8 | uint32(src[i+2])
		out = append(out, alphabet[(v>>18)&0x3f], alphabet[(v>>12)&0x3f], alphabet[(v>>6)&0x3f], alphabet[v&0x3f])
	}
	switch len(src) - i {
	case 1:
		v := uint32(src[i]) << 16
		out = append(out, alphabet[(v>>18)&0x3f], alphabet[(v>>12)&0x3f])
	case 2:
		v := uint32(src[i])<<16 | uint32(src[i+1])<<8
		out = append(out, alphabet[(v>>18)&0x3f], alphabet[(v>>12)&0x3f], alphabet[(v>>6)&0x3f])
	}
	return string(out)
}
```

- [ ] **Step 2: Run test — expect compile failure**

Run: `cd /root/coding/modelserver && go test ./internal/admin/ -run TestHandleCodexOAuth -v`
Expected: FAIL, `undefined: handleCodexOAuthStart`.

- [ ] **Step 3: Create `internal/admin/handle_codex_oauth.go` with start + exchange**

```go
package admin

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelserver/modelserver/internal/proxy"
)

const defaultCodexRedirectURI = "http://localhost:1455/auth/callback"

// codexOAuthTokenURL is overridable in tests so we can stub auth.openai.com.
var codexOAuthTokenURL = proxy.CodexTokenURL

func handleCodexOAuthStart() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RedirectURI string `json:"redirect_uri"`
		}
		decodeBody(r, &body)

		redirectURI := body.RedirectURI
		if redirectURI == "" {
			redirectURI = defaultCodexRedirectURI
		}

		verifierBytes := make([]byte, 64)
		if _, err := rand.Read(verifierBytes); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to generate code verifier")
			return
		}
		codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
		challenge := sha256.Sum256([]byte(codeVerifier))
		codeChallenge := base64.RawURLEncoding.EncodeToString(challenge[:])

		stateBytes := make([]byte, 32)
		if _, err := rand.Read(stateBytes); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to generate state")
			return
		}
		state := base64.RawURLEncoding.EncodeToString(stateBytes)

		params := url.Values{
			"response_type":              {"code"},
			"client_id":                  {proxy.CodexClientID},
			"redirect_uri":               {redirectURI},
			"scope":                      {proxy.CodexScopes},
			"code_challenge":             {codeChallenge},
			"code_challenge_method":      {"S256"},
			"state":                      {state},
			"id_token_add_organizations": {"true"},
			"codex_cli_simplified_flow":  {"true"},
			"originator":                 {"codex_cli_rs"},
		}
		authURL := proxy.CodexAuthURL + "?" + params.Encode()

		writeData(w, http.StatusOK, map[string]interface{}{
			"auth_url":      authURL,
			"state":         state,
			"code_verifier": codeVerifier,
			"redirect_uri":  redirectURI,
		})
	}
}

func handleCodexOAuthExchange() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code         string `json:"code"`
			CallbackURL  string `json:"callback_url"`
			State        string `json:"state"`
			CodeVerifier string `json:"code_verifier"`
			RedirectURI  string `json:"redirect_uri"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		code := body.Code
		if code == "" && body.CallbackURL != "" {
			u, err := url.Parse(body.CallbackURL)
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", "invalid callback URL")
				return
			}
			code = u.Query().Get("code")
		}
		if code == "" || body.CodeVerifier == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "code and code_verifier are required")
			return
		}

		redirectURI := body.RedirectURI
		if redirectURI == "" {
			redirectURI = defaultCodexRedirectURI
		}

		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"client_id":     {proxy.CodexClientID},
			"redirect_uri":  {redirectURI},
			"code_verifier": {body.CodeVerifier},
		}

		client := &http.Client{Timeout: 15 * time.Second}
		req, _ := http.NewRequest(http.MethodPost, codexOAuthTokenURL, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("codex token exchange failed: %v", err))
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
		if resp.StatusCode != http.StatusOK {
			writeError(w, http.StatusBadGateway, "upstream_error",
				fmt.Sprintf("codex token exchange returned %d: %s", resp.StatusCode, string(respBody)))
			return
		}

		var tokenResp struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		}
		if err := json.Unmarshal(respBody, &tokenResp); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to parse codex token response")
			return
		}

		creds := proxy.CodexCredentials{
			IDToken:          tokenResp.IDToken,
			AccessToken:      tokenResp.AccessToken,
			RefreshToken:     tokenResp.RefreshToken,
			ExpiresAt:        time.Now().Unix() + tokenResp.ExpiresIn,
			ClientID:         proxy.CodexClientID,
		}
		// Best-effort account-id extraction; absent claim is fine.
		creds.ChatGPTAccountID = extractCodexAccountID(tokenResp.IDToken)

		writeData(w, http.StatusOK, creds)
	}
}

// extractCodexAccountID is a thin wrapper so the proxy package's
// unexported parser can stay private.
func extractCodexAccountID(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Auth.ChatGPTAccountID
}

// silence unused-import warning if bytes ends up unused after edits
var _ = bytes.NewReader
```

(The trailing `var _ = bytes.NewReader` exists to keep the import block stable in case of small future refactors. Remove it once another use of `bytes` lands in this file.)

- [ ] **Step 4: Run tests — expect pass**

Run: `cd /root/coding/modelserver && go test ./internal/admin/ -run TestHandleCodexOAuth -v`
Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_codex_oauth.go internal/admin/handle_codex_oauth_test.go
git commit -m "feat(admin): codex OAuth start + exchange handlers"
```

---

### Task 10: Admin handlers — token status, manual refresh

**Files:**
- Modify: `internal/admin/handle_codex_oauth.go`
- Modify: `internal/admin/handle_codex_oauth_test.go`

- [ ] **Step 1: Write tests for status + refresh**

Append to `internal/admin/handle_codex_oauth_test.go`:

```go
import (
	// ...existing...
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// (You may need to extract the encryption key + a stub store. The simplest
// path is to share helpers with handle_claudecode_oauth_test.go — if such
// helpers don't exist yet, define a small integration helper here that
// spins up a real Postgres-via-testcontainers OR uses an existing
// `newTestStore(t)` from the codebase.)

func TestHandleCodexTokenStatus_NotCodex(t *testing.T) {
	st := newTestStore(t) // existing helper used elsewhere in admin tests
	encKey := []byte(strings.Repeat("0", 32))

	// Insert a non-codex upstream.
	id := mustCreateUpstream(t, st, encKey, types.Upstream{
		Provider: types.ProviderOpenAI,
		Name:     "openai-1",
	}, "sk-...")

	r := chi.NewRouter()
	r.Get("/{upstreamID}/oauth/status", handleCodexTokenStatus(st, encKey))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/"+id+"/oauth/status", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-codex upstream, got %d", w.Code)
	}
}
```

> **Important:** Pull the `newTestStore` and `mustCreateUpstream` helpers from existing admin tests (`handle_claudecode_oauth_test.go` or its neighbors). Do NOT duplicate them; reuse via the same package. If they don't exist with those names, search the package for the analogous setup (`grep -n "func newTestStore\|func mustCreate" internal/admin/`) and use the found names.

- [ ] **Step 2: Run test — expect compile failure**

Run: `cd /root/coding/modelserver && go test ./internal/admin/ -run TestHandleCodexTokenStatus -v`
Expected: FAIL, `undefined: handleCodexTokenStatus`.

- [ ] **Step 3: Append the status + refresh handlers**

Append to `internal/admin/handle_codex_oauth.go`:

```go
import (
	// ...existing...
	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/store"
)

func handleCodexTokenStatus(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamID := chi.URLParam(r, "upstreamID")
		u, err := st.GetUpstreamByID(upstreamID)
		if err != nil || u == nil {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}
		if u.Provider != "codex" {
			writeError(w, http.StatusBadRequest, "bad_request", "upstream is not a codex upstream")
			return
		}
		plaintext, err := crypto.Decrypt(encKey, u.APIKeyEncrypted)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to decrypt credentials")
			return
		}
		var creds proxy.CodexCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to parse credentials")
			return
		}
		writeData(w, http.StatusOK, map[string]interface{}{
			"expires_at":         creds.ExpiresAt,
			"has_refresh_token":  creds.RefreshToken != "",
			"chatgpt_account_id": creds.ChatGPTAccountID,
		})
	}
}

func handleCodexTokenRefresh(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamID := chi.URLParam(r, "upstreamID")
		u, err := st.GetUpstreamByID(upstreamID)
		if err != nil || u == nil {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}
		if u.Provider != "codex" {
			writeError(w, http.StatusBadRequest, "bad_request", "upstream is not a codex upstream")
			return
		}
		plaintext, err := crypto.Decrypt(encKey, u.APIKeyEncrypted)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to decrypt credentials")
			return
		}
		var creds proxy.CodexCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to parse credentials")
			return
		}
		if creds.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "no refresh token; please re-authorize")
			return
		}
		clientID := creds.ClientID
		if clientID == "" {
			clientID = proxy.CodexClientID
		}

		body, _ := json.Marshal(map[string]string{
			"client_id":     clientID,
			"grant_type":    "refresh_token",
			"refresh_token": creds.RefreshToken,
		})
		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Post(codexOAuthTokenURL, "application/json", bytes.NewReader(body))
		if err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("codex refresh request failed: %v", err))
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		if resp.StatusCode != http.StatusOK {
			writeError(w, http.StatusBadGateway, "upstream_error",
				fmt.Sprintf("codex refresh returned %d: %s", resp.StatusCode, string(respBody)))
			return
		}
		var tokenResp struct {
			IDToken      *string `json:"id_token"`
			AccessToken  *string `json:"access_token"`
			RefreshToken *string `json:"refresh_token"`
			ExpiresIn    int64   `json:"expires_in"`
		}
		if err := json.Unmarshal(respBody, &tokenResp); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to parse codex token response")
			return
		}
		newCreds := creds
		if tokenResp.AccessToken != nil {
			newCreds.AccessToken = *tokenResp.AccessToken
		}
		if tokenResp.RefreshToken != nil {
			newCreds.RefreshToken = *tokenResp.RefreshToken
		}
		if tokenResp.IDToken != nil {
			newCreds.IDToken = *tokenResp.IDToken
			if id := extractCodexAccountID(*tokenResp.IDToken); id != "" {
				newCreds.ChatGPTAccountID = id
			}
		}
		if tokenResp.ExpiresIn > 0 {
			newCreds.ExpiresAt = time.Now().Unix() + tokenResp.ExpiresIn
		}
		newCreds.ClientID = clientID

		credsJSON, err := json.Marshal(newCreds)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to marshal credentials")
			return
		}
		enc, err := crypto.Encrypt(encKey, credsJSON)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to encrypt credentials")
			return
		}
		if err := st.UpdateUpstream(upstreamID, map[string]interface{}{"api_key_encrypted": enc}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to persist credentials")
			return
		}
		writeData(w, http.StatusOK, map[string]interface{}{
			"expires_at":         newCreds.ExpiresAt,
			"has_refresh_token":  newCreds.RefreshToken != "",
			"chatgpt_account_id": newCreds.ChatGPTAccountID,
		})
	}
}
```

(`bytes` is now used legitimately so the placeholder `var _ = bytes.NewReader` from Task 9 may be deleted.)

- [ ] **Step 4: Run tests**

Run: `cd /root/coding/modelserver && go test ./internal/admin/ -run TestHandleCodex -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_codex_oauth.go internal/admin/handle_codex_oauth_test.go
git commit -m "feat(admin): codex token status + manual refresh handlers"
```

---

### Task 11: Admin handler — utilization

**Files:**
- Modify: `internal/admin/handle_codex_oauth.go`

- [ ] **Step 1: Append the utilization handler**

Append to `internal/admin/handle_codex_oauth.go`:

```go
// codexUsageURL is overridable in tests.
var codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

func handleCodexUtilization(st *store.Store, encKey []byte) http.HandlerFunc {
	type cacheEntry struct {
		body      []byte
		fetchedAt time.Time
	}
	var cache sync.Map // upstreamID → *cacheEntry
	const cacheTTL = 60 * time.Second

	return func(w http.ResponseWriter, r *http.Request) {
		upstreamID := chi.URLParam(r, "upstreamID")
		u, err := st.GetUpstreamByID(upstreamID)
		if err != nil || u == nil {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}
		if u.Provider != "codex" {
			writeError(w, http.StatusBadRequest, "bad_request", "upstream is not a codex upstream")
			return
		}
		plaintext, err := crypto.Decrypt(encKey, u.APIKeyEncrypted)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to decrypt credentials")
			return
		}
		var creds proxy.CodexCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to parse credentials")
			return
		}

		accessToken := creds.AccessToken
		// If the token is within the expiry buffer, do an inline refresh
		// (mirrors the claudecode utilization helper).
		if time.Now().Unix() > creds.ExpiresAt-300 && creds.RefreshToken != "" {
			clientID := creds.ClientID
			if clientID == "" {
				clientID = proxy.CodexClientID
			}
			refBody, _ := json.Marshal(map[string]string{
				"client_id":     clientID,
				"grant_type":    "refresh_token",
				"refresh_token": creds.RefreshToken,
			})
			client := &http.Client{Timeout: 15 * time.Second}
			refResp, refErr := client.Post(codexOAuthTokenURL, "application/json", bytes.NewReader(refBody))
			if refErr == nil {
				defer refResp.Body.Close()
				if refResp.StatusCode == http.StatusOK {
					var tokenResp struct {
						IDToken      *string `json:"id_token"`
						AccessToken  *string `json:"access_token"`
						RefreshToken *string `json:"refresh_token"`
						ExpiresIn    int64   `json:"expires_in"`
					}
					if rb, _ := io.ReadAll(io.LimitReader(refResp.Body, 8192)); json.Unmarshal(rb, &tokenResp) == nil {
						newCreds := creds
						if tokenResp.AccessToken != nil {
							newCreds.AccessToken = *tokenResp.AccessToken
							accessToken = *tokenResp.AccessToken
						}
						if tokenResp.RefreshToken != nil {
							newCreds.RefreshToken = *tokenResp.RefreshToken
						}
						if tokenResp.IDToken != nil {
							newCreds.IDToken = *tokenResp.IDToken
							if id := extractCodexAccountID(*tokenResp.IDToken); id != "" {
								newCreds.ChatGPTAccountID = id
							}
						}
						if tokenResp.ExpiresIn > 0 {
							newCreds.ExpiresAt = time.Now().Unix() + tokenResp.ExpiresIn
						}
						newCreds.ClientID = clientID
						if cj, err := json.Marshal(newCreds); err == nil {
							if enc, err := crypto.Encrypt(encKey, cj); err == nil {
								_ = st.UpdateUpstream(upstreamID, map[string]interface{}{"api_key_encrypted": enc})
							}
						}
					}
				}
			}
		}

		// Cache hit — serve.
		if cached, ok := cache.Load(upstreamID); ok {
			entry := cached.(*cacheEntry)
			if time.Since(entry.fetchedAt) < cacheTTL {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(entry.body)
				return
			}
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, codexUsageURL, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create request")
			return
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("User-Agent", "codex_cli_rs/0.55.0 (Linux; x64) Codex")
		req.Header.Set("Version", "0.55.0")
		if creds.ChatGPTAccountID != "" {
			req.Header.Set("ChatGPT-Account-ID", creds.ChatGPTAccountID)
		}

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("codex usage fetch failed: %v", err))
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusTooManyRequests {
				if cached, ok := cache.Load(upstreamID); ok {
					entry := cached.(*cacheEntry)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(entry.body)
					return
				}
			}
			writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("codex usage returned %d", resp.StatusCode))
			return
		}
		if !json.Valid(body) {
			writeError(w, http.StatusBadGateway, "upstream_error", "codex usage returned invalid JSON")
			return
		}
		full := []byte(fmt.Sprintf(`{"data":%s}`, string(body)))
		cache.Store(upstreamID, &cacheEntry{body: full, fetchedAt: time.Now()})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(full)
	}
}
```

The import block needs `"sync"` if not already present.

- [ ] **Step 2: Build**

Run: `cd /root/coding/modelserver && go build ./internal/admin/...`
Expected: build succeeds.

- [ ] **Step 3: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/handle_codex_oauth.go
git commit -m "feat(admin): codex utilization handler (wham/usage with 60s cache)"
```

---

### Task 12: Wire codex routes into the admin router

**Files:**
- Modify: `internal/admin/routes.go:228-247`

- [ ] **Step 1: Add the five route registrations**

In `internal/admin/routes.go`, find the upstreams subrouter (~line 228). After the existing claudecode oauth lines, add:

```go
				r.Post("/codex/oauth/start", handleCodexOAuthStart())
				r.Post("/codex/oauth/exchange", handleCodexOAuthExchange())
```

Inside the per-upstream subrouter (`r.Route("/{upstreamID}", ...)`), after the existing claudecode lines, add:

```go
					r.Get("/codex/oauth/status", handleCodexTokenStatus(st, encKey))
					r.Post("/codex/oauth/refresh", handleCodexTokenRefresh(st, encKey))
					r.Get("/codex/utilization", handleCodexUtilization(st, encKey))
```

The full block should look like:

```go
		r.Route("/upstreams", func(r chi.Router) {
			r.Use(RequireSuperadmin)
			r.Get("/", handleListUpstreams(st, encKey))
			r.Post("/", handleCreateUpstream(st, encKey, catalog))
			r.Get("/usage", handleUpstreamUsage(st))
			r.Post("/claudecode/oauth/start", handleClaudeCodeOAuthStart())
			r.Post("/claudecode/oauth/exchange", handleClaudeCodeOAuthExchange())
			r.Post("/codex/oauth/start", handleCodexOAuthStart())
			r.Post("/codex/oauth/exchange", handleCodexOAuthExchange())
			r.Route("/{upstreamID}", func(r chi.Router) {
				r.Get("/", handleGetUpstream(st))
				r.Put("/", handleUpdateUpstream(st, encKey, catalog))
				r.Delete("/", handleDeleteUpstream(st))
				r.Post("/test", handleTestUpstream(st, encKey))
				r.Get("/oauth/status", handleClaudeCodeTokenStatus(st, encKey))
				r.Post("/oauth/refresh", handleClaudeCodeTokenRefresh(st, encKey))
				r.Get("/utilization", handleClaudeCodeUtilization(st, encKey))
				r.Get("/utilization-snapshots", handleListUtilizationSnapshots(st))
				r.Get("/utilization-analysis", handleUtilizationAnalysis(st))
				r.Get("/codex/oauth/status", handleCodexTokenStatus(st, encKey))
				r.Post("/codex/oauth/refresh", handleCodexTokenRefresh(st, encKey))
				r.Get("/codex/utilization", handleCodexUtilization(st, encKey))
			})
		})
```

- [ ] **Step 2: Build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: build succeeds.

- [ ] **Step 3: Smoke check route registration**

Run a quick start of the admin server and curl one of the new routes (you'll need the server already configured for local dev):

```bash
cd /root/coding/modelserver
go run ./cmd/modelserver --config config.yml &
sleep 2
curl -s -i -X POST http://localhost:8081/api/v1/upstreams/codex/oauth/start \
     -H 'Content-Type: application/json' \
     -H 'Authorization: Bearer <admin-token>' \
     -d '{}' | head -20
kill %1
```

Expected: 200 OK with `auth_url`, `state`, `code_verifier`, `redirect_uri` in the JSON body. If the server is not configured for local dev, skip this step and rely on the unit tests.

- [ ] **Step 4: Commit**

```bash
cd /root/coding/modelserver
git add internal/admin/routes.go
git commit -m "feat(admin): wire five codex OAuth/utilization routes"
```

---

### Task 13: Dashboard API hooks

**Files:**
- Create: `dashboard/src/api/codex.ts`

- [ ] **Step 1: Write the file**

Create `dashboard/src/api/codex.ts`:

```typescript
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, DataResponse } from "./client"; // adjust path if needed (mirror `upstreams.ts`)

export type CodexOAuthStartResponse = {
  auth_url: string;
  state: string;
  code_verifier: string;
  redirect_uri: string;
};

export type CodexCredentials = {
  id_token?: string;
  access_token: string;
  refresh_token: string;
  chatgpt_account_id?: string;
  expires_at: number;
  client_id?: string;
};

export type CodexTokenStatus = {
  expires_at: number;
  has_refresh_token: boolean;
  chatgpt_account_id?: string;
};

export function useCodexOAuthStart() {
  return useMutation({
    mutationFn: (body?: { redirect_uri?: string }) =>
      api.post<DataResponse<CodexOAuthStartResponse>>(
        "/api/v1/upstreams/codex/oauth/start",
        body ?? {},
      ),
  });
}

export function useCodexOAuthExchange() {
  return useMutation({
    mutationFn: (body: {
      callback_url: string;
      code_verifier: string;
      state: string;
      redirect_uri: string;
    }) =>
      api.post<DataResponse<CodexCredentials>>(
        "/api/v1/upstreams/codex/oauth/exchange",
        body,
      ),
  });
}

export function useUpstreamCodexOAuthStatus(upstreamId: string | undefined) {
  return useQuery({
    queryKey: ["admin", "upstreams", upstreamId, "codex-oauth-status"],
    queryFn: () =>
      api.get<DataResponse<CodexTokenStatus>>(
        `/api/v1/upstreams/${upstreamId}/codex/oauth/status`,
      ),
    enabled: !!upstreamId,
    refetchInterval: 60_000,
  });
}

export function useUpstreamCodexOAuthRefresh() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (upstreamId: string) =>
      api.post<DataResponse<CodexTokenStatus>>(
        `/api/v1/upstreams/${upstreamId}/codex/oauth/refresh`,
      ),
    onSuccess: (_, upstreamId) => {
      qc.invalidateQueries({
        queryKey: ["admin", "upstreams", upstreamId, "codex-oauth-status"],
      });
    },
  });
}

export function useCodexUtilization(upstreamId: string | undefined) {
  return useQuery({
    queryKey: ["admin", "upstreams", upstreamId, "codex-utilization"],
    queryFn: () =>
      api.get<DataResponse<unknown>>(
        `/api/v1/upstreams/${upstreamId}/codex/utilization`,
      ),
    enabled: !!upstreamId,
    refetchInterval: 5 * 60_000,
  });
}
```

- [ ] **Step 2: Verify the import paths**

`api`, `DataResponse` may live at `../api/client` or similar. Cross-check with `dashboard/src/api/upstreams.ts`'s top imports and align.

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc --noEmit`
Expected: no TypeScript errors mentioning `codex.ts`.

- [ ] **Step 3: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/api/codex.ts
git commit -m "feat(dashboard): codex OAuth + utilization API hooks"
```

---

### Task 14: Dashboard — codex provider option in UpstreamsPage

**Files:**
- Modify: `dashboard/src/pages/admin/UpstreamsPage.tsx`

- [ ] **Step 1: Add `codex` to the provider `<SelectItem>` list**

Find the provider `<Select>` block (~line 643 currently has `<SelectItem value="claudecode">Claude Code</SelectItem>`). Add immediately after:

```tsx
                  <SelectItem value="codex">Codex (ChatGPT)</SelectItem>
```

- [ ] **Step 2: Add the codex OAuth UI branch in create/edit dialog**

Find the conditional `{form.provider === "claudecode" ? (...) : (... API key text input ...)}` block (~line 674). Restructure to a `switch`-style pattern by introducing a sibling branch for `codex`:

```tsx
{form.provider === "claudecode" ? (
  /* ...existing claudecode OAuth UI... */
) : form.provider === "codex" ? (
  <CodexOAuthBlock
    form={form}
    setForm={setForm}
    oauthStep={oauthStep}
    setOauthStep={setOauthStep}
    pkce={pkce}
    setPkce={setPkce}
  />
) : (
  /* ...existing api_key text input... */
)}
```

Define `CodexOAuthBlock` either inline in the same file (simplest, consistent with how claudecode UI is written) or as a sibling component. Use the same three-step layout as the existing claudecode block, calling `useCodexOAuthStart` / `useCodexOAuthExchange` from `dashboard/src/api/codex.ts`.

For the inline form, the whole block (modeled on the existing claudecode equivalent) is:

```tsx
function CodexOAuthBlock({ form, setForm, oauthStep, setOauthStep, pkce, setPkce }: any) {
  const oauthStart = useCodexOAuthStart();
  const oauthExchange = useCodexOAuthExchange();
  const [callbackURL, setCallbackURL] = React.useState("");

  const startFlow = async () => {
    const { data } = await oauthStart.mutateAsync({});
    setPkce({
      state: data.state,
      code_verifier: data.code_verifier,
      redirect_uri: data.redirect_uri,
      auth_url: data.auth_url,
    });
    setOauthStep("await_callback");
  };

  const completeFlow = async () => {
    const { data } = await oauthExchange.mutateAsync({
      callback_url: callbackURL,
      code_verifier: pkce.code_verifier,
      state: pkce.state,
      redirect_uri: pkce.redirect_uri,
    });
    setForm((f: any) => ({ ...f, api_key: JSON.stringify(data) }));
    setOauthStep("complete");
  };

  return (
    <div className="space-y-3 border border-dashed border-border rounded p-3">
      {oauthStep === "idle" && (
        <Button onClick={startFlow}>1. Start Authorization</Button>
      )}
      {oauthStep === "await_callback" && (
        <>
          <p className="text-sm">Open this URL, authorize, then paste the resulting <code>localhost:1455</code> URL back here:</p>
          <a href={pkce.auth_url} target="_blank" rel="noreferrer" className="break-all text-blue-500 underline">
            {pkce.auth_url}
          </a>
          <Input
            placeholder="http://localhost:1455/auth/callback?code=...&state=..."
            value={callbackURL}
            onChange={(e) => setCallbackURL(e.target.value)}
          />
          <Button onClick={completeFlow} disabled={!callbackURL}>3. Complete Authorization</Button>
        </>
      )}
      {oauthStep === "complete" && (
        <p className="text-sm text-green-500">Codex credentials acquired. Save the upstream to persist.</p>
      )}
    </div>
  );
}
```

(If `pkce` and `oauthStep` are already shared state in the parent component for claudecode, reuse them — the two providers never run their flows simultaneously since the dialog is single-purpose.)

- [ ] **Step 3: Add the existing `provider === "claudecode"` check sites for edit/save guards**

Find the disabled condition at line 909-910:

```tsx
(!editingId && !form.api_key && form.provider !== "claudecode") ||
(!editingId && form.provider === "claudecode" && oauthStep !== "complete") ||
```

Replace with:

```tsx
(!editingId && !form.api_key && form.provider !== "claudecode" && form.provider !== "codex") ||
(!editingId && form.provider === "claudecode" && oauthStep !== "complete") ||
(!editingId && form.provider === "codex" && oauthStep !== "complete") ||
```

- [ ] **Step 4: Tsc + a quick smoke render**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc --noEmit && pnpm build`
Expected: typecheck and build succeed.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/pages/admin/UpstreamsPage.tsx
git commit -m "feat(dashboard): codex provider option + OAuth flow UI"
```

---

### Task 15: Dashboard — codex token status badge + utilization card

**Files:**
- Modify: `dashboard/src/pages/admin/UpstreamsPage.tsx`

- [ ] **Step 1: Add token-status badge for codex rows**

Find the existing claudecode token-status badge (~line 538: `{u.provider === "claudecode" && (...)}`). Add a parallel block immediately after:

```tsx
{u.provider === "codex" && (
  <CodexTokenStatusBadge upstreamId={u.id} />
)}
```

Define the component near the other small badge components in the file:

```tsx
function CodexTokenStatusBadge({ upstreamId }: { upstreamId: string }) {
  const { data, isLoading } = useUpstreamCodexOAuthStatus(upstreamId);
  if (isLoading || !data) return null;
  const expiresIn = data.data.expires_at - Math.floor(Date.now() / 1000);
  const variant = expiresIn < 0 ? "destructive" : expiresIn < 300 ? "warning" : "default";
  const label = expiresIn < 0 ? "expired" : `${Math.floor(expiresIn / 60)}m`;
  return <Badge variant={variant as any}>{label}</Badge>;
}
```

- [ ] **Step 2: Add utilization mini-card in the upstream details panel**

Find the existing `useClaudeCodeUtilization` consumer (~line 85, `const { data, isLoading } = useClaudeCodeUtilization(upstreamId);`). The component holding it likely renders a small utilization card. Mirror with a `CodexUtilizationCard`:

```tsx
function CodexUtilizationCard({ upstreamId }: { upstreamId: string }) {
  const { data, isLoading } = useCodexUtilization(upstreamId);
  if (isLoading) return <div className="text-xs text-muted-foreground">Loading codex usage…</div>;
  if (!data) return null;
  return (
    <pre className="text-xs bg-muted p-2 rounded overflow-auto max-h-40">
      {JSON.stringify(data.data, null, 2)}
    </pre>
  );
}
```

Render it in the same place the claudecode card is rendered, gated on `u.provider === "codex"`.

- [ ] **Step 3: Tsc + build**

Run: `cd /root/coding/modelserver/dashboard && pnpm exec tsc --noEmit && pnpm build`
Expected: success.

- [ ] **Step 4: Commit**

```bash
cd /root/coding/modelserver
git add dashboard/src/pages/admin/UpstreamsPage.tsx
git commit -m "feat(dashboard): codex token status badge + utilization card"
```

---

### Task 16: End-to-end smoke test (manual)

**Files:** none (manual run)

- [ ] **Step 1: Bring up modelserver + dashboard locally**

```bash
cd /root/coding/modelserver
go run ./cmd/modelserver --config config.yml &
cd dashboard && pnpm dev &
```

- [ ] **Step 2: Through the dashboard, create a `codex` upstream**

Visit the upstreams admin page. Pick provider `codex (ChatGPT)`. Click "Start Authorization". Open the returned URL in a browser, sign in with a real ChatGPT subscription account, copy the failed-to-load `localhost:1455/...` URL, paste it back, click "Complete Authorization". Save the upstream.

- [ ] **Step 3: Verify token status**

The new upstream should show a green badge with the time remaining until expiry (e.g., `59m`). Click the upstream — the codex utilization card should display JSON returned from `wham/usage`.

- [ ] **Step 4: Send a proxy request**

Configure a route mapping (e.g., model `gpt-5` → upstream group containing the new codex upstream). Then:

```bash
curl -s -X POST http://localhost:8080/v1/responses \
  -H 'Authorization: Bearer <user-api-key>' \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-5","input":"Say hi in one word.","stream":false}' | head -40
```

Expected: a 200 with a Responses-API JSON body. The request should appear in the admin "Requests" view, attributed to the codex upstream.

- [ ] **Step 5: Force 401 recovery**

Manually corrupt the access token in the credentials JSON via the database (set the access_token to a known-bad value). Send another request. Expect: server logs show `retrying codex request after OAuth token refresh`, and the request still succeeds (the force-refresh should mint a fresh token).

- [ ] **Step 6: If everything works, no commit needed; close out.**

If anything failed, file an issue and pick up from the failing task.

---

## Self-Review Notes

- **Spec coverage**: every section of the spec maps to a task — provider constant (Task 1), CodexCredentials/manager (Tasks 2-4), director (Task 5), transformer (Task 6), Router wiring (Task 7), Executor branches (Task 8), admin handlers (Tasks 9-11), routes (Task 12), dashboard hooks + UI (Tasks 13-15), end-to-end test (Task 16).
- **Type consistency**: `CodexCredentials` field names (`AccessToken`, `RefreshToken`, `ChatGPTAccountID`, `IDToken`, `ExpiresAt`, `ClientID`) are reused unchanged across `codex_oauth.go`, `provider_codex.go`, `handle_codex_oauth.go`, and `dashboard/src/api/codex.ts` (snake_case there). Method names (`GetAccessToken`, `GetAccountID`, `ForceRefreshAccessToken` on the manager; `GetCodexAccessToken`, `GetCodexAccountID`, `ForceRefreshCodexAccessToken` on the Router) are stable.
- **No placeholders**: all code blocks contain runnable code; tests have full assertions; commit messages are spelled out.
- **Two known soft-edges**: (a) Tasks 9 / 10's tests reuse helpers from existing admin tests (`newTestStore`, `mustCreateUpstream`); the engineer must locate the actual symbol names in the codebase before relying on them. (b) Task 14's `pkce` / `oauthStep` state is currently scoped per-claudecode in `UpstreamsPage.tsx`; reusing them for codex assumes the dialog is single-provider at any time, which it is.