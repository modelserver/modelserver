package proxy

import (
	"fmt"
	"sync"
	"testing"

	"golang.org/x/oauth2"
)

// mockTokenSource returns a fixed token for testing.
type mockTokenSource struct {
	token *oauth2.Token
	err   error
	calls int
	mu    sync.Mutex
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.token, m.err
}

func TestVertexTokenManager_RegisterAndGetToken(t *testing.T) {
	tm := NewVertexTokenManager()

	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "fake-token-123"},
	}
	tm.registerWithSource("upstream-1", mock)

	tok, err := tm.GetToken("upstream-1")
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if tok != "fake-token-123" {
		t.Errorf("GetToken() = %q, want %q", tok, "fake-token-123")
	}
}

func TestVertexTokenManager_GetToken_UnknownUpstream(t *testing.T) {
	tm := NewVertexTokenManager()

	_, err := tm.GetToken("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown upstream, got nil")
	}
}

func TestVertexTokenManager_GetToken_SourceError(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{err: fmt.Errorf("auth failed")}
	tm.registerWithSource("upstream-1", mock)

	_, err := tm.GetToken("upstream-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestVertexTokenManager_Clear(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "tok"},
	}
	tm.registerWithSource("upstream-1", mock)

	tm.Clear()

	_, err := tm.GetToken("upstream-1")
	if err == nil {
		t.Fatal("expected error after Clear(), got nil")
	}
}

func TestVertexTokenManager_Deregister(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "tok"},
	}
	tm.registerWithSource("upstream-1", mock)

	tm.Deregister("upstream-1")

	_, err := tm.GetToken("upstream-1")
	if err == nil {
		t.Fatal("expected error after Deregister(), got nil")
	}
}
