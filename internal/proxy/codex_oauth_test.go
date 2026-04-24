package proxy

import (
	"encoding/base64"
	"encoding/json"
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
