package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

func TestLoadCredentials(t *testing.T) {
	mgr := NewOAuthTokenManager(nil, nil, nil)

	creds := ClaudeCodeCredentials{
		AccessToken:  "at-123",
		RefreshToken: "rt-456",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	credsJSON, _ := json.Marshal(creds)

	upstreams := []types.Upstream{
		{ID: "ch1", Provider: types.ProviderClaudeCode},
		{ID: "ch2", Provider: types.ProviderAnthropic},
	}
	keys := map[string]string{
		"ch1": string(credsJSON),
		"ch2": "sk-ant-key",
	}

	mgr.LoadCredentials(upstreams, keys)

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if _, ok := mgr.credentials["ch1"]; !ok {
		t.Fatal("expected credentials for ch1")
	}
	if mgr.credentials["ch1"].AccessToken != "at-123" {
		t.Errorf("access_token = %s, want at-123", mgr.credentials["ch1"].AccessToken)
	}
	if _, ok := mgr.credentials["ch2"]; ok {
		t.Error("should not load credentials for non-claudecode upstream")
	}
}

func TestLoadCredentials_InvalidJSON(t *testing.T) {
	mgr := NewOAuthTokenManager(nil, nil, nil)

	upstreams := []types.Upstream{
		{ID: "ch1", Provider: types.ProviderClaudeCode},
	}
	keys := map[string]string{
		"ch1": "not-json",
	}

	mgr.LoadCredentials(upstreams, keys)

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	if _, ok := mgr.credentials["ch1"]; ok {
		t.Error("should not load invalid credentials")
	}
}

func TestGetAccessToken_Valid(t *testing.T) {
	mgr := NewOAuthTokenManager(nil, nil, nil)

	mgr.mu.Lock()
	mgr.credentials["ch1"] = &ClaudeCodeCredentials{
		AccessToken:  "valid-token",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	mgr.mu.Unlock()

	token, err := mgr.GetAccessToken("ch1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "valid-token" {
		t.Errorf("token = %s, want valid-token", token)
	}
}

func TestGetAccessToken_NotFound(t *testing.T) {
	mgr := NewOAuthTokenManager(nil, nil, nil)

	_, err := mgr.GetAccessToken("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent upstream")
	}
}

func TestGetAccessToken_Expired_TriggersRefresh(t *testing.T) {
	// Set up a mock OAuth server that validates the request and returns new tokens.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["scope"] != ClaudeCodeScopes {
			t.Errorf("refresh request scope = %q, want %q", body["scope"], ClaudeCodeScopes)
		}
		if body["grant_type"] != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", body["grant_type"])
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "new-token",
			"refresh_token": "new-rt",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	mgr := NewOAuthTokenManager(nil, nil, nil)
	mgr.httpClient = server.Client()
	mgr.tokenURL = server.URL

	mgr.mu.Lock()
	mgr.credentials["ch1"] = &ClaudeCodeCredentials{
		AccessToken:  "expired-token",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).Unix(),
	}
	mgr.mu.Unlock()

	token, err := mgr.GetAccessToken("ch1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "new-token" {
		t.Errorf("token = %s, want new-token (refreshed)", token)
	}
}

func TestGetAccessToken_RefreshFails_ReturnsFallback(t *testing.T) {
	// Mock server that returns an error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer server.Close()

	mgr := NewOAuthTokenManager(nil, nil, nil)
	mgr.httpClient = server.Client()
	mgr.tokenURL = server.URL

	mgr.mu.Lock()
	mgr.credentials["ch1"] = &ClaudeCodeCredentials{
		AccessToken:  "stale-token",
		RefreshToken: "bad-rt",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).Unix(),
	}
	mgr.mu.Unlock()

	token, err := mgr.GetAccessToken("ch1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return old token as fallback when refresh fails.
	if token != "stale-token" {
		t.Errorf("token = %s, want stale-token (fallback)", token)
	}
}

func TestReload_PreservesNewerCredentials(t *testing.T) {
	mgr := NewOAuthTokenManager(nil, nil, nil)

	// Set in-memory credentials with a future expiry.
	futureExpiry := time.Now().Add(2 * time.Hour).Unix()
	mgr.mu.Lock()
	mgr.credentials["ch1"] = &ClaudeCodeCredentials{
		AccessToken:  "fresh-token",
		RefreshToken: "fresh-rt",
		ExpiresAt:    futureExpiry,
	}
	mgr.mu.Unlock()

	// DB has older credentials.
	olderCreds := ClaudeCodeCredentials{
		AccessToken:  "old-token",
		RefreshToken: "old-rt",
		ExpiresAt:    time.Now().Add(30 * time.Minute).Unix(),
	}
	credsJSON, _ := json.Marshal(olderCreds)

	upstreams := []types.Upstream{
		{ID: "ch1", Provider: types.ProviderClaudeCode},
	}
	keys := map[string]string{
		"ch1": string(credsJSON),
	}

	mgr.Reload(upstreams, keys)

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.credentials["ch1"].AccessToken != "fresh-token" {
		t.Errorf("expected fresh-token to be preserved, got %s", mgr.credentials["ch1"].AccessToken)
	}
}

func TestRouter_GetClaudeCodeAccessToken(t *testing.T) {
	mgr := NewOAuthTokenManager(nil, nil, nil)

	mgr.mu.Lock()
	mgr.credentials["up1"] = &ClaudeCodeCredentials{
		AccessToken:  "valid-token",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	mgr.mu.Unlock()

	r := &Router{oauthMgr: mgr}
	token, err := r.GetClaudeCodeAccessToken("up1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "valid-token" {
		t.Errorf("token = %s, want valid-token", token)
	}
}

func TestRouter_GetClaudeCodeAccessToken_NoManager(t *testing.T) {
	r := &Router{}
	_, err := r.GetClaudeCodeAccessToken("up1")
	if err == nil {
		t.Error("expected error when oauthMgr is nil")
	}
}

func TestExecutor_ClaudeCodeTokenResolution(t *testing.T) {
	mgr := NewOAuthTokenManager(nil, nil, nil)
	mgr.mu.Lock()
	mgr.credentials["up-cc"] = &ClaudeCodeCredentials{
		AccessToken:  "resolved-oauth-token",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
	}
	mgr.mu.Unlock()

	r := &Router{oauthMgr: mgr}
	// Simulate what the executor does: resolve token for claudecode upstream.
	token, err := r.GetClaudeCodeAccessToken("up-cc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "resolved-oauth-token" {
		t.Errorf("token = %s, want resolved-oauth-token", token)
	}

	// For non-existent upstream, should return error (executor falls back to raw key).
	_, err = r.GetClaudeCodeAccessToken("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent upstream")
	}
}

func TestReload_UsesDBWhenNewer(t *testing.T) {
	mgr := NewOAuthTokenManager(nil, nil, nil)

	// Set in-memory credentials with an old expiry.
	mgr.mu.Lock()
	mgr.credentials["ch1"] = &ClaudeCodeCredentials{
		AccessToken:  "old-token",
		RefreshToken: "old-rt",
		ExpiresAt:    time.Now().Add(10 * time.Minute).Unix(),
	}
	mgr.mu.Unlock()

	// DB has newer credentials.
	newerCreds := ClaudeCodeCredentials{
		AccessToken:  "db-token",
		RefreshToken: "db-rt",
		ExpiresAt:    time.Now().Add(2 * time.Hour).Unix(),
	}
	credsJSON, _ := json.Marshal(newerCreds)

	upstreams := []types.Upstream{
		{ID: "ch1", Provider: types.ProviderClaudeCode},
	}
	keys := map[string]string{
		"ch1": string(credsJSON),
	}

	mgr.Reload(upstreams, keys)

	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	if mgr.credentials["ch1"].AccessToken != "db-token" {
		t.Errorf("expected db-token (newer), got %s", mgr.credentials["ch1"].AccessToken)
	}
}
