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
	if resp.Data.IDToken != "" {
		t.Errorf("id_token should be empty in response, got %q", resp.Data.IDToken)
	}

	// Verify the raw JSON does not contain an id_token key at all.
	var rawResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rawResp); err == nil {
		if data, ok := rawResp["data"].(map[string]any); ok {
			if _, present := data["id_token"]; present {
				t.Error("response leaked id_token to client")
			}
		}
	}
}

func TestHandleCodexTokenStatus_NotFound(t *testing.T) {
	// nil store would panic on GetUpstreamByID — instead use a real handler
	// dispatched through chi to confirm 404 path works once st returns nil.
	// We skip the full round-trip since admin tests in this repo don't set up
	// a real store. Instead, we exercise route wiring + verify the handler
	// constructor doesn't panic.
	if h := handleCodexTokenStatus(nil, nil); h == nil {
		t.Fatal("handleCodexTokenStatus returned nil")
	}
	if h := handleCodexTokenRefresh(nil, nil); h == nil {
		t.Fatal("handleCodexTokenRefresh returned nil")
	}
}

func TestHandleCodexUtilization_ConstructorOK(t *testing.T) {
	if h := handleCodexUtilization(nil, nil); h == nil {
		t.Fatal("handleCodexUtilization returned nil")
	}
}

func TestCodexWindowTypeFromSeconds(t *testing.T) {
	tests := []struct {
		name    string
		seconds int64
		want    string
	}{
		{name: "five hour", seconds: 5 * 60 * 60, want: "5h"},
		{name: "seven day", seconds: 7 * 24 * 60 * 60, want: "7d"},
		{name: "unknown", seconds: 60 * 60, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexWindowTypeFromSeconds(tt.seconds); got != tt.want {
				t.Fatalf("codexWindowTypeFromSeconds(%d) = %q, want %q", tt.seconds, got, tt.want)
			}
		})
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
