package proxy

import (
	"strings"
	"testing"
)

func TestDirectorSetClaudeCodeUpstream(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req.Header.Set("x-api-key", "user-key")
	req.Header.Set("Authorization", "Bearer user-token")

	directorSetClaudeCodeUpstream(req, "https://api.anthropic.com", "oauth-access-token")

	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
	if req.URL.Host != "api.anthropic.com" {
		t.Errorf("host = %s, want api.anthropic.com", req.URL.Host)
	}
	if req.URL.Path != "/v1/messages" {
		t.Errorf("path = %s, want /v1/messages", req.URL.Path)
	}
	if req.URL.Query().Get("beta") != "true" {
		t.Errorf("expected beta=true query param, got %s", req.URL.RawQuery)
	}
	if req.Header.Get("Authorization") != "Bearer oauth-access-token" {
		t.Errorf("Authorization = %s, want Bearer oauth-access-token", req.Header.Get("Authorization"))
	}
	if req.Header.Get("x-api-key") != "" {
		t.Errorf("x-api-key should be removed, got %s", req.Header.Get("x-api-key"))
	}
	if req.Header.Get("Anthropic-Version") != "2023-06-01" {
		t.Errorf("Anthropic-Version = %s, want 2023-06-01", req.Header.Get("Anthropic-Version"))
	}
	if req.Header.Get("Anthropic-Dangerous-Direct-Browser-Access") != "true" {
		t.Errorf("expected Anthropic-Dangerous-Direct-Browser-Access: true")
	}
	if req.Header.Get("X-App") != "cli" {
		t.Errorf("X-App = %s, want cli", req.Header.Get("X-App"))
	}
	wantBeta := "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27"
	if got := req.Header.Get("Anthropic-Beta"); got != wantBeta {
		t.Errorf("Anthropic-Beta = %s, want %s", got, wantBeta)
	}
	if got := req.Header.Get("User-Agent"); got != "claude-cli/2.1.76 (external, cli)" {
		t.Errorf("User-Agent = %s, want claude-cli/2.1.76 (external, cli)", got)
	}
	if got := req.Header.Get("X-Stainless-Package-Version"); got != "0.74.0" {
		t.Errorf("X-Stainless-Package-Version = %s, want 0.74.0", got)
	}
	if got := req.Header.Get("X-Stainless-Runtime"); got != "bun" {
		t.Errorf("X-Stainless-Runtime = %s, want bun", got)
	}
	if got := req.Header.Get("X-Stainless-Runtime-Version"); got != "1.3.11" {
		t.Errorf("X-Stainless-Runtime-Version = %s, want 1.3.11", got)
	}
	if req.Header.Get("Connection") != "keep-alive" {
		t.Errorf("Connection = %s, want keep-alive", req.Header.Get("Connection"))
	}
}

func TestDirectorSetClaudeCodeUpstream_ClientBetaPassthrough(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	// Simulate a client that sends additional beta flags (e.g. context-1m).
	req.Header.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14,context-1m-2025-08-07")

	directorSetClaudeCodeUpstream(req, "https://api.anthropic.com", "token")

	got := req.Header.Get("Anthropic-Beta")
	// Base betas must always be present.
	for _, b := range claudeCodeBaseBetas {
		if !strings.Contains(got, b) {
			t.Errorf("Anthropic-Beta missing base beta %s, got %s", b, got)
		}
	}
	// Client-only beta must be preserved.
	if !strings.Contains(got, "context-1m-2025-08-07") {
		t.Errorf("Anthropic-Beta should contain client beta context-1m-2025-08-07, got %s", got)
	}
	// Base betas should come first.
	wantPrefix := "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("Anthropic-Beta should start with base betas %s, got %s", wantPrefix, got)
	}
}

func TestDirectorSetClaudeCodeUpstream_NoDuplicateBetas(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	// Client sends betas that overlap with the base set.
	req.Header.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27")

	directorSetClaudeCodeUpstream(req, "https://api.anthropic.com", "token")

	got := req.Header.Get("Anthropic-Beta")
	want := "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27"
	if got != want {
		t.Errorf("Anthropic-Beta = %s, want %s (no duplicates)", got, want)
	}
}

func TestMergeClaudeCodeBetas(t *testing.T) {
	tests := []struct {
		name       string
		clientBeta string
		want       string
	}{
		{
			name:       "empty client",
			clientBeta: "",
			want:       "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27",
		},
		{
			name:       "client with new beta",
			clientBeta: "context-1m-2025-08-07",
			want:       "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,context-1m-2025-08-07",
		},
		{
			name:       "client with overlap and new",
			clientBeta: "claude-code-20250219,context-1m-2025-08-07,tool-search-tool-2025-10-19",
			want:       "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,context-1m-2025-08-07,tool-search-tool-2025-10-19",
		},
		{
			name:       "client exact match",
			clientBeta: "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27",
			want:       "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27",
		},
		{
			name:       "client with whitespace",
			clientBeta: " context-1m-2025-08-07 , tool-examples-2025-10-29 ",
			want:       "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,context-1m-2025-08-07,tool-examples-2025-10-29",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeClaudeCodeBetas(tt.clientBeta)
			if got != tt.want {
				t.Errorf("mergeClaudeCodeBetas(%q) = %q, want %q", tt.clientBeta, got, tt.want)
			}
		})
	}
}

func TestDirectorSetClaudeCodeUpstream_BaseURLWithPath(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	directorSetClaudeCodeUpstream(req, "https://custom-proxy.example.com/prefix", "token")

	if req.URL.Host != "custom-proxy.example.com" {
		t.Errorf("host = %s, want custom-proxy.example.com", req.URL.Host)
	}
	if req.URL.Path != "/prefix/v1/messages" {
		t.Errorf("path = %s, want /prefix/v1/messages", req.URL.Path)
	}
}

func TestDirectorSetClaudeCodeUpstream_EmptyBaseURL(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	directorSetClaudeCodeUpstream(req, "", "token")

	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
}
