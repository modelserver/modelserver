package admin

import (
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
			IDToken:      tokenResp.IDToken,
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
			ClientID:     proxy.CodexClientID,
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
