package proxy

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/types"
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

func TestParseCodexAccessTokenAndAccount_MalformedJSON(t *testing.T) {
	// A '{'-prefixed string that doesn't parse should return ("", "")
	// rather than passing the garbage through as a bearer token.
	at, acct := ParseCodexAccessTokenAndAccount("{not valid json")
	if at != "" || acct != "" {
		t.Errorf("got (%q, %q), want (\"\", \"\")", at, acct)
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
		if orig := r.Header.Get("Originator"); orig != "codex_cli_rs" {
			t.Errorf("Originator = %q, want codex_cli_rs", orig)
		}
		if ua := r.Header.Get("User-Agent"); ua != codexUserAgent {
			t.Errorf("User-Agent = %q, want %q", ua, codexUserAgent)
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
