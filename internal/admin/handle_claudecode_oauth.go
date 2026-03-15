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
	"time"

	"github.com/modelserver/modelserver/internal/proxy"
)

const (
	claudeCodeAuthURL     = "https://claude.ai/oauth/authorize"
	claudeCodeTokenURL    = "https://console.anthropic.com/v1/oauth/token"
	claudeCodeRedirectURI = "http://localhost:54545/callback"
	claudeCodeScopes      = "org:create_api_key user:profile user:inference"
)

func handleClaudeCodeOAuthStart() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Generate PKCE code_verifier (64 bytes, base64url-encoded).
		verifierBytes := make([]byte, 64)
		if _, err := rand.Read(verifierBytes); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to generate code verifier")
			return
		}
		codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

		// Compute code_challenge = base64url(SHA256(code_verifier)).
		h := sha256.Sum256([]byte(codeVerifier))
		codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

		// Generate random state.
		stateBytes := make([]byte, 32)
		if _, err := rand.Read(stateBytes); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to generate state")
			return
		}
		state := base64.RawURLEncoding.EncodeToString(stateBytes)

		// Build authorization URL.
		params := url.Values{
			"response_type":         {"code"},
			"client_id":             {proxy.ClaudeCodeClientID},
			"redirect_uri":          {claudeCodeRedirectURI},
			"scope":                 {claudeCodeScopes},
			"code_challenge":        {codeChallenge},
			"code_challenge_method": {"S256"},
			"state":                 {state},
		}
		authURL := claudeCodeAuthURL + "?" + params.Encode()

		writeData(w, http.StatusOK, map[string]string{
			"auth_url":      authURL,
			"state":         state,
			"code_verifier": codeVerifier,
		})
	}
}

func handleClaudeCodeOAuthExchange() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code         string `json:"code"`
			CallbackURL  string `json:"callback_url"`
			State        string `json:"state"`
			CodeVerifier string `json:"code_verifier"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		// Extract code from callback_url if not provided directly.
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

		// Exchange authorization code for tokens.
		tokenReqBody, _ := json.Marshal(map[string]string{
			"grant_type":    "authorization_code",
			"code":          code,
			"client_id":     proxy.ClaudeCodeClientID,
			"redirect_uri":  claudeCodeRedirectURI,
			"code_verifier": body.CodeVerifier,
			"state":         body.State,
		})

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Post(claudeCodeTokenURL, "application/json", bytes.NewReader(tokenReqBody))
		if err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("token exchange failed: %v", err))
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

		if resp.StatusCode != http.StatusOK {
			writeError(w, http.StatusBadGateway, "upstream_error",
				fmt.Sprintf("token exchange returned %d: %s", resp.StatusCode, string(respBody)))
			return
		}

		var tokenResp struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		}
		if err := json.Unmarshal(respBody, &tokenResp); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to parse token response")
			return
		}

		// Return credentials JSON that should be used as the channel's api_key.
		credentials := map[string]interface{}{
			"access_token":  tokenResp.AccessToken,
			"refresh_token": tokenResp.RefreshToken,
			"expires_at":    time.Now().Unix() + tokenResp.ExpiresIn,
			"client_id":     proxy.ClaudeCodeClientID,
		}

		writeData(w, http.StatusOK, credentials)
	}
}
