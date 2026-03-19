package proxy

import (
	"bytes"
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
	// ClaudeCodeTokenURL is the OAuth token endpoint for Claude Code.
	ClaudeCodeTokenURL = "https://platform.claude.com/v1/oauth/token"
	// ClaudeCodeAuthURL is the OAuth authorization endpoint for Claude Code.
	ClaudeCodeAuthURL = "https://claude.ai/oauth/authorize"
	// ClaudeCodeClientID is the public OAuth client ID for Claude Code.
	ClaudeCodeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// ClaudeCodeScopes is the full set of OAuth scopes used by Claude Code CLI.
	ClaudeCodeScopes = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	// Refresh tokens 5 minutes before expiry (matches CLI behavior).
	tokenExpiryBuffer = 300
)

// ClaudeCodeCredentials holds OAuth credentials for a Claude Code upstream.
type ClaudeCodeCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	ClientID     string `json:"client_id,omitempty"`
}

// OAuthTokenManager manages OAuth tokens for Claude Code upstreams.
type OAuthTokenManager struct {
	mu            sync.RWMutex
	credentials   map[string]*ClaudeCodeCredentials // upstreamID → credentials
	store         *store.Store
	encryptionKey []byte
	logger        *slog.Logger
	sfGroup       singleflight.Group
	httpClient    *http.Client
	tokenURL      string
}

// NewOAuthTokenManager creates a new OAuth token manager.
func NewOAuthTokenManager(st *store.Store, encKey []byte, logger *slog.Logger) *OAuthTokenManager {
	return &OAuthTokenManager{
		credentials:   make(map[string]*ClaudeCodeCredentials),
		store:         st,
		encryptionKey: encKey,
		logger:        logger,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		tokenURL:      ClaudeCodeTokenURL,
	}
}

// ParseClaudeCodeAccessToken extracts the access_token from a raw Claude Code
// credentials JSON string. Returns empty string on failure.
func ParseClaudeCodeAccessToken(raw string) string {
	var creds struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal([]byte(raw), &creds) != nil {
		return ""
	}
	return creds.AccessToken
}

// LoadCredentials parses and stores credentials for all claudecode upstreams.
func (m *OAuthTokenManager) LoadCredentials(upstreams []types.Upstream, decryptedKeys map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, u := range upstreams {
		if u.Provider != types.ProviderClaudeCode {
			continue
		}
		raw, ok := decryptedKeys[u.ID]
		if !ok || raw == "" {
			continue
		}
		var creds ClaudeCodeCredentials
		if err := json.Unmarshal([]byte(raw), &creds); err != nil {
			if m.logger != nil {
				m.logger.Error("failed to parse claudecode credentials", "upstream_id", u.ID, "error", err)
			}
			continue
		}
		m.credentials[u.ID] = &creds
	}
}

// Reload re-loads credentials, preserving recently refreshed tokens that
// may not yet be persisted to the database.
func (m *OAuthTokenManager) Reload(upstreams []types.Upstream, decryptedKeys map[string]string) {
	newCreds := make(map[string]*ClaudeCodeCredentials)

	for _, u := range upstreams {
		if u.Provider != types.ProviderClaudeCode {
			continue
		}
		raw, ok := decryptedKeys[u.ID]
		if !ok || raw == "" {
			continue
		}
		var creds ClaudeCodeCredentials
		if err := json.Unmarshal([]byte(raw), &creds); err != nil {
			if m.logger != nil {
				m.logger.Error("failed to parse claudecode credentials on reload", "upstream_id", u.ID, "error", err)
			}
			continue
		}
		newCreds[u.ID] = &creds
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Preserve in-memory credentials that are newer than what the DB has.
	for id, existing := range m.credentials {
		if dbCreds, ok := newCreds[id]; ok {
			if existing.ExpiresAt > dbCreds.ExpiresAt {
				newCreds[id] = existing
			}
		}
	}
	m.credentials = newCreds
}

// GetAccessToken returns a valid access token for the given upstream,
// refreshing if necessary.
func (m *OAuthTokenManager) GetAccessToken(upstreamID string) (string, error) {
	m.mu.RLock()
	creds, ok := m.credentials[upstreamID]
	if !ok {
		m.mu.RUnlock()
		return "", fmt.Errorf("no credentials for claudecode upstream %s", upstreamID)
	}
	token := creds.AccessToken
	needsRefresh := time.Now().Unix() > creds.ExpiresAt-tokenExpiryBuffer
	m.mu.RUnlock()

	if needsRefresh {
		_, err, _ := m.sfGroup.Do(upstreamID, func() (interface{}, error) {
			return nil, m.refreshToken(upstreamID)
		})
		if err != nil {
			if m.logger != nil {
				m.logger.Error("failed to refresh claudecode token", "upstream_id", upstreamID, "error", err)
			}
			// Return the existing token as fallback; it might still work.
			return token, nil
		}
		m.mu.RLock()
		token = m.credentials[upstreamID].AccessToken
		m.mu.RUnlock()
	}

	return token, nil
}

// refreshToken exchanges a refresh token for new access/refresh tokens.
func (m *OAuthTokenManager) refreshToken(upstreamID string) error {
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
		clientID = ClaudeCodeClientID
	}

	reqBody, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"refresh_token": refreshToken,
		"scope":         ClaudeCodeScopes,
	})

	resp, err := m.httpClient.Post(m.tokenURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("oauth token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oauth token refresh returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("failed to parse token response: %w", err)
	}

	newCreds := &ClaudeCodeCredentials{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tokenResp.ExpiresIn,
		ClientID:     clientID,
	}

	// Update in-memory credentials.
	m.mu.Lock()
	m.credentials[upstreamID] = newCreds
	m.mu.Unlock()

	// Persist to database.
	if m.store != nil && len(m.encryptionKey) > 0 {
		credsJSON, err := json.Marshal(newCreds)
		if err != nil {
			return fmt.Errorf("failed to marshal new credentials: %w", err)
		}
		encrypted, err := crypto.Encrypt(m.encryptionKey, credsJSON)
		if err != nil {
			return fmt.Errorf("failed to encrypt new credentials: %w", err)
		}
		if err := m.store.UpdateUpstream(upstreamID, map[string]interface{}{
			"api_key_encrypted": encrypted,
		}); err != nil {
			if m.logger != nil {
				m.logger.Error("failed to persist refreshed claudecode token", "upstream_id", upstreamID, "error", err)
			}
		}
	}

	if m.logger != nil {
		m.logger.Info("refreshed claudecode token", "upstream_id", upstreamID)
	}

	return nil
}
