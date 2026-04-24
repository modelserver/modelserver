package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
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
	CodexScopes = "openid profile email offline_access api.connectors.read api.connectors.invoke"
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

// ParseCodexAccessTokenAndAccount parses raw into an access token and account
// id using the following rules:
//
//   - Empty input or non-JSON input (raw[0] != '{'): returned unchanged as the
//     access token; account id is empty.
//   - Valid JSON blob: parsed as CodexCredentials; access token and account id
//     are taken from the blob.
//   - JSON-shaped input (starts with '{') that fails to unmarshal: returns
//     ("", "") to signal the input is unusable. The caller is expected to
//     error or fall back rather than forwarding the garbage as a bearer token.
func ParseCodexAccessTokenAndAccount(raw string) (accessToken, accountID string) {
	if len(raw) == 0 || raw[0] != '{' {
		return raw, ""
	}
	var creds CodexCredentials
	if json.Unmarshal([]byte(raw), &creds) != nil {
		return "", ""
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
