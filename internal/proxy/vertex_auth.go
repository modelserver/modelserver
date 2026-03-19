package proxy

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const vertexOAuthScope = "https://www.googleapis.com/auth/cloud-platform"

// VertexTokenManager manages OAuth2 access tokens for Vertex AI upstreams.
// Each upstream has its own service account and independently cached token.
type VertexTokenManager struct {
	mu     sync.RWMutex
	tokens map[string]*vertexToken
}

type vertexToken struct {
	source oauth2.TokenSource
}

// NewVertexTokenManager creates a new token manager.
func NewVertexTokenManager() *VertexTokenManager {
	return &VertexTokenManager{
		tokens: make(map[string]*vertexToken),
	}
}

// Register parses a service account JSON key and creates a token source for
// the given upstream. The token source handles caching and automatic refresh.
func (m *VertexTokenManager) Register(upstreamID string, serviceAccountJSON []byte) error {
	creds, err := google.CredentialsFromJSON(context.Background(), serviceAccountJSON, vertexOAuthScope)
	if err != nil {
		return fmt.Errorf("parsing service account JSON for upstream %s: %w", upstreamID, err)
	}
	source := oauth2.ReuseTokenSource(nil, creds.TokenSource)
	m.mu.Lock()
	m.tokens[upstreamID] = &vertexToken{source: source}
	m.mu.Unlock()
	return nil
}

// registerWithSource is a test helper that registers an upstream with a custom
// token source, bypassing JSON key parsing.
func (m *VertexTokenManager) registerWithSource(upstreamID string, source oauth2.TokenSource) {
	m.mu.Lock()
	m.tokens[upstreamID] = &vertexToken{source: oauth2.ReuseTokenSource(nil, source)}
	m.mu.Unlock()
}

// GetToken returns a valid access token for the given upstream.
// The underlying ReuseTokenSource handles caching and refresh.
func (m *VertexTokenManager) GetToken(upstreamID string) (string, error) {
	m.mu.RLock()
	entry, ok := m.tokens[upstreamID]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("no vertex token source registered for upstream %s", upstreamID)
	}
	tok, err := entry.source.Token()
	if err != nil {
		return "", fmt.Errorf("getting token for upstream %s: %w", upstreamID, err)
	}
	return tok.AccessToken, nil
}

// Clear removes all registered token sources. Called by Router.Reload()
// before re-registering upstreams.
func (m *VertexTokenManager) Clear() {
	m.mu.Lock()
	m.tokens = make(map[string]*vertexToken)
	m.mu.Unlock()
}

// Deregister removes a single upstream's token source.
func (m *VertexTokenManager) Deregister(upstreamID string) {
	m.mu.Lock()
	delete(m.tokens, upstreamID)
	m.mu.Unlock()
}
