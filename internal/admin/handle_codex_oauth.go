package admin

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/proxy"
	"github.com/modelserver/modelserver/internal/store"
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
			AccessToken:      tokenResp.AccessToken,
			RefreshToken:     tokenResp.RefreshToken,
			ChatGPTAccountID: extractCodexAccountID(tokenResp.IDToken),
			ExpiresAt:        time.Now().Unix() + tokenResp.ExpiresIn,
			ClientID:         proxy.CodexClientID,
		}
		// IDToken is intentionally omitted: account_id is already extracted above,
		// and the signed JWT should not be forwarded to the browser.

		writeData(w, http.StatusOK, creds)
	}
}

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
			slog.Error("codex manual token refresh: request failed", "upstream_id", upstreamID, "error", err)
			writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("codex refresh request failed: %v", err))
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		if resp.StatusCode != http.StatusOK {
			slog.Error("codex manual token refresh: upstream error", "upstream_id", upstreamID, "status", resp.StatusCode, "body", string(respBody))
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
			slog.Error("codex manual token refresh: parse error", "upstream_id", upstreamID, "error", err)
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
			slog.Error("codex manual token refresh: db update failed", "upstream_id", upstreamID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to persist credentials")
			return
		}
		slog.Info("codex token manually refreshed", "upstream_id", upstreamID, "expires_at", newCreds.ExpiresAt)
		writeData(w, http.StatusOK, map[string]interface{}{
			"expires_at":         newCreds.ExpiresAt,
			"has_refresh_token":  newCreds.RefreshToken != "",
			"chatgpt_account_id": newCreds.ChatGPTAccountID,
		})
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
		req.Header.Set("Originator", "codex_cli_rs")
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
