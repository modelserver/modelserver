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
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/proxy"
	"github.com/modelserver/modelserver/internal/store"
)

type utilizationCacheEntry struct {
	body      []byte
	fetchedAt time.Time
}

const (
	defaultClaudeCodeRedirectURI = "http://localhost:54545/callback"
)

func handleClaudeCodeOAuthStart() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RedirectURI string `json:"redirect_uri"`
		}
		// Body is optional; ignore decode errors.
		decodeBody(r, &body)

		redirectURI := body.RedirectURI
		if redirectURI == "" {
			redirectURI = defaultClaudeCodeRedirectURI
		}

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
			"redirect_uri":          {redirectURI},
			"scope":                 {proxy.ClaudeCodeScopes},
			"code_challenge":        {codeChallenge},
			"code_challenge_method": {"S256"},
			"state":                 {state},
		}
		authURL := proxy.ClaudeCodeAuthURL + "?" + params.Encode()

		writeData(w, http.StatusOK, map[string]interface{}{
			"auth_url":      authURL,
			"state":         state,
			"code_verifier": codeVerifier,
			"redirect_uri":  redirectURI,
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
			RedirectURI  string `json:"redirect_uri"`
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

		redirectURI := body.RedirectURI
		if redirectURI == "" {
			redirectURI = defaultClaudeCodeRedirectURI
		}

		// Exchange authorization code for tokens.
		tokenReqBody, _ := json.Marshal(map[string]string{
			"grant_type":    "authorization_code",
			"code":          code,
			"client_id":     proxy.ClaudeCodeClientID,
			"redirect_uri":  redirectURI,
			"code_verifier": body.CodeVerifier,
			"state":         body.State,
		})

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Post(proxy.ClaudeCodeTokenURL, "application/json", bytes.NewReader(tokenReqBody))
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

		// Return credentials JSON that should be used as the upstream's api_key.
		credentials := map[string]interface{}{
			"access_token":  tokenResp.AccessToken,
			"refresh_token": tokenResp.RefreshToken,
			"expires_at":    time.Now().Unix() + tokenResp.ExpiresIn,
			"client_id":     proxy.ClaudeCodeClientID,
		}

		writeData(w, http.StatusOK, credentials)
	}
}

func handleClaudeCodeTokenStatus(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamID := chi.URLParam(r, "upstreamID")
		u, err := st.GetUpstreamByID(upstreamID)
		if err != nil || u == nil {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}
		if u.Provider != "claudecode" {
			writeError(w, http.StatusBadRequest, "bad_request", "upstream is not a claudecode upstream")
			return
		}

		plaintext, err := crypto.Decrypt(encKey, u.APIKeyEncrypted)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to decrypt credentials")
			return
		}

		var creds proxy.ClaudeCodeCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to parse credentials")
			return
		}

		writeData(w, http.StatusOK, map[string]interface{}{
			"expires_at":        creds.ExpiresAt,
			"has_refresh_token": creds.RefreshToken != "",
		})
	}
}

func handleClaudeCodeUtilization(st *store.Store, encKey []byte) http.HandlerFunc {
	var cache sync.Map // upstreamID → *utilizationCacheEntry
	const cacheTTL = 60 * time.Second

	return func(w http.ResponseWriter, r *http.Request) {
		upstreamID := chi.URLParam(r, "upstreamID")
		u, err := st.GetUpstreamByID(upstreamID)
		if err != nil || u == nil {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}
		if u.Provider != "claudecode" {
			writeError(w, http.StatusBadRequest, "bad_request", "upstream is not a claudecode upstream")
			return
		}

		plaintext, err := crypto.Decrypt(encKey, u.APIKeyEncrypted)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to decrypt credentials")
			return
		}

		var creds proxy.ClaudeCodeCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to parse credentials")
			return
		}

		// If the token is expired, try refreshing it first.
		accessToken := creds.AccessToken
		if time.Now().Unix() > creds.ExpiresAt-300 && creds.RefreshToken != "" {
			clientID := creds.ClientID
			if clientID == "" {
				clientID = proxy.ClaudeCodeClientID
			}
			refreshBody, _ := json.Marshal(map[string]string{
				"grant_type":    "refresh_token",
				"client_id":     clientID,
				"refresh_token": creds.RefreshToken,
				"scope":         proxy.ClaudeCodeScopes,
			})
			refreshClient := &http.Client{Timeout: 15 * time.Second}
			refreshResp, err := refreshClient.Post(proxy.ClaudeCodeTokenURL, "application/json", bytes.NewReader(refreshBody))
			if err == nil {
				defer refreshResp.Body.Close()
				if refreshResp.StatusCode == http.StatusOK {
					var tokenResp struct {
						AccessToken  string `json:"access_token"`
						RefreshToken string `json:"refresh_token"`
						ExpiresIn    int64  `json:"expires_in"`
					}
					if rb, _ := io.ReadAll(io.LimitReader(refreshResp.Body, 8192)); json.Unmarshal(rb, &tokenResp) == nil && tokenResp.AccessToken != "" {
						accessToken = tokenResp.AccessToken
						// Persist refreshed credentials.
						newCreds := proxy.ClaudeCodeCredentials{
							AccessToken:  tokenResp.AccessToken,
							RefreshToken: tokenResp.RefreshToken,
							ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
							ClientID:     clientID,
						}
						if credsJSON, err := json.Marshal(newCreds); err == nil {
							if encrypted, err := crypto.Encrypt(encKey, credsJSON); err == nil {
								_ = st.UpdateUpstream(upstreamID, map[string]interface{}{"api_key_encrypted": encrypted})
							}
						}
					}
				}
			}
		}

		// Return cached response if fresh enough.
		if cached, ok := cache.Load(upstreamID); ok {
			entry := cached.(*utilizationCacheEntry)
			if time.Since(entry.fetchedAt) < cacheTTL {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write(entry.body)
				return
			}
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create request")
			return
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		req.Header.Set("User-Agent", "claude-cli/2.1.76 (external, cli)")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			slog.Error("claudecode utilization fetch failed", "upstream_id", upstreamID, "error", err)
			writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("utilization fetch failed: %v", err))
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		if resp.StatusCode != http.StatusOK {
			slog.Error("claudecode utilization fetch: upstream error", "upstream_id", upstreamID, "status", resp.StatusCode, "body", string(body))
			// On rate limit, serve stale cache if available.
			if resp.StatusCode == http.StatusTooManyRequests {
				if cached, ok := cache.Load(upstreamID); ok {
					entry := cached.(*utilizationCacheEntry)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					w.Write(entry.body)
					return
				}
			}
			writeError(w, http.StatusBadGateway, "upstream_error",
				fmt.Sprintf("utilization API returned %d", resp.StatusCode))
			return
		}

		// Validate that body is valid JSON before wrapping.
		if !json.Valid(body) {
			writeError(w, http.StatusBadGateway, "upstream_error", "upstream returned invalid JSON")
			return
		}

		// Enrich response with local credits for comparison.
		var utilResp map[string]interface{}
		if err := json.Unmarshal(body, &utilResp); err == nil {
			if fh, ok := utilResp["five_hour"].(map[string]interface{}); ok {
				if ra, ok := fh["resets_at"].(string); ok && ra != "" {
					if t, err := time.Parse(time.RFC3339, ra); err == nil {
						if credits, err := st.GetCreditsByUpstreamIDSince(upstreamID, t.Add(-5*time.Hour)); err == nil {
							utilResp["local_credits_5h"] = credits
						}
					}
				}
			}
			if sd, ok := utilResp["seven_day"].(map[string]interface{}); ok {
				if ra, ok := sd["resets_at"].(string); ok && ra != "" {
					if t, err := time.Parse(time.RFC3339, ra); err == nil {
						if credits, err := st.GetCreditsByUpstreamIDSince(upstreamID, t.Add(-7*24*time.Hour)); err == nil {
							utilResp["local_credits_7d"] = credits
						}
					}
				}
			}
			if enriched, err := json.Marshal(utilResp); err == nil {
				body = enriched
			}
		}

		// Cache the enriched response.
		fullResp := []byte(fmt.Sprintf(`{"data":%s}`, string(body)))
		cache.Store(upstreamID, &utilizationCacheEntry{body: fullResp, fetchedAt: time.Now()})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(fullResp)
	}
}

func handleClaudeCodeTokenRefresh(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamID := chi.URLParam(r, "upstreamID")
		u, err := st.GetUpstreamByID(upstreamID)
		if err != nil || u == nil {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}
		if u.Provider != "claudecode" {
			writeError(w, http.StatusBadRequest, "bad_request", "upstream is not a claudecode upstream")
			return
		}

		plaintext, err := crypto.Decrypt(encKey, u.APIKeyEncrypted)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to decrypt credentials")
			return
		}

		var creds proxy.ClaudeCodeCredentials
		if err := json.Unmarshal(plaintext, &creds); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to parse credentials")
			return
		}

		if creds.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "no refresh token available, please re-authorize")
			return
		}

		clientID := creds.ClientID
		if clientID == "" {
			clientID = proxy.ClaudeCodeClientID
		}

		reqBody, _ := json.Marshal(map[string]string{
			"grant_type":    "refresh_token",
			"client_id":     clientID,
			"refresh_token": creds.RefreshToken,
			"scope":         proxy.ClaudeCodeScopes,
		})

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Post(proxy.ClaudeCodeTokenURL, "application/json", bytes.NewReader(reqBody))
		if err != nil {
			slog.Error("claudecode manual token refresh: request failed", "upstream_id", upstreamID, "error", err)
			writeError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("token refresh request failed: %v", err))
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		if resp.StatusCode != http.StatusOK {
			slog.Error("claudecode manual token refresh: upstream error", "upstream_id", upstreamID, "status", resp.StatusCode, "body", string(body))
			writeError(w, http.StatusBadGateway, "upstream_error",
				fmt.Sprintf("token refresh returned %d: %s", resp.StatusCode, string(body)))
			return
		}

		var tokenResp struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
		}
		if err := json.Unmarshal(body, &tokenResp); err != nil {
			slog.Error("claudecode manual token refresh: parse error", "upstream_id", upstreamID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to parse token response")
			return
		}

		newCreds := proxy.ClaudeCodeCredentials{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
			ClientID:     clientID,
		}

		credsJSON, err := json.Marshal(newCreds)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to marshal credentials")
			return
		}

		encrypted, err := crypto.Encrypt(encKey, credsJSON)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to encrypt credentials")
			return
		}

		if err := st.UpdateUpstream(upstreamID, map[string]interface{}{
			"api_key_encrypted": encrypted,
		}); err != nil {
			slog.Error("claudecode manual token refresh: db update failed", "upstream_id", upstreamID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to persist refreshed credentials")
			return
		}

		slog.Info("claudecode token manually refreshed", "upstream_id", upstreamID, "expires_at", newCreds.ExpiresAt)

		writeData(w, http.StatusOK, map[string]interface{}{
			"expires_at":        newCreds.ExpiresAt,
			"has_refresh_token": newCreds.RefreshToken != "",
		})
	}
}
