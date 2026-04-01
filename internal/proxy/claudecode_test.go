package proxy

import (
	"net/http"
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
	// ?beta=true is appended (the Anthropic SDK's beta.messages endpoint sends this).
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
	// No client beta header → only the required OAuth beta should be present.
	wantBeta := "oauth-2025-04-20"
	if got := req.Header.Get("Anthropic-Beta"); got != wantBeta {
		t.Errorf("Anthropic-Beta = %s, want %s", got, wantBeta)
	}
	// No client User-Agent → default should be set.
	if got := req.Header.Get("User-Agent"); got != "claude-cli/0.0.0 (external, cli)" {
		t.Errorf("User-Agent = %s, want claude-cli/0.0.0 (external, cli)", got)
	}
	// No client X-Stainless-* → defaults should be set.
	if got := req.Header.Get("X-Stainless-Package-Version"); got != "0.0.0" {
		t.Errorf("X-Stainless-Package-Version = %s, want 0.0.0", got)
	}
	if got := req.Header.Get("X-Stainless-Runtime"); got != "bun" {
		t.Errorf("X-Stainless-Runtime = %s, want bun", got)
	}
	if got := req.Header.Get("X-Stainless-Runtime-Version"); got != "0.0.0" {
		t.Errorf("X-Stainless-Runtime-Version = %s, want 0.0.0", got)
	}
	if req.Header.Get("Connection") != "keep-alive" {
		t.Errorf("Connection = %s, want keep-alive", req.Header.Get("Connection"))
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

func TestDirectorSetClaudeCodeUpstream_MergesClientBetaHeaders(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14,output-128k-2025-02-19")

	directorSetClaudeCodeUpstream(req, "https://api.anthropic.com", "token")

	want := "oauth-2025-04-20,interleaved-thinking-2025-05-14,output-128k-2025-02-19"
	if got := req.Header.Get("Anthropic-Beta"); got != want {
		t.Errorf("Anthropic-Beta = %s, want %s", got, want)
	}
}

func TestDirectorSetClaudeCodeUpstream_DeduplicatesBetas(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req.Header.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14")

	directorSetClaudeCodeUpstream(req, "https://api.anthropic.com", "token")

	want := "oauth-2025-04-20,claude-code-20250219,interleaved-thinking-2025-05-14"
	if got := req.Header.Get("Anthropic-Beta"); got != want {
		t.Errorf("Anthropic-Beta = %s, want %s", got, want)
	}
}

func TestDirectorSetClaudeCodeUpstream_EmptyBaseURL(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	directorSetClaudeCodeUpstream(req, "", "token")

	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
}

func TestDirectorSetClaudeCodeUpstream_PreservesClientHeaders(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	// Simulate client-provided User-Agent and X-Stainless-* headers.
	req.Header.Set("User-Agent", "claude-cli/2.5.0 (external, cli)")
	req.Header.Set("X-Stainless-Package-Version", "1.2.3")
	req.Header.Set("X-Stainless-Runtime", "node")
	req.Header.Set("X-Stainless-Runtime-Version", "22.0.0")
	req.Header.Set("X-Stainless-OS", "Darwin")
	req.Header.Set("X-Stainless-Arch", "arm64")

	directorSetClaudeCodeUpstream(req, "https://api.anthropic.com", "token")

	// Client-provided values should be preserved, not overwritten.
	if got := req.Header.Get("User-Agent"); got != "claude-cli/2.5.0 (external, cli)" {
		t.Errorf("User-Agent = %s, want client value claude-cli/2.5.0 (external, cli)", got)
	}
	if got := req.Header.Get("X-Stainless-Package-Version"); got != "1.2.3" {
		t.Errorf("X-Stainless-Package-Version = %s, want client value 1.2.3", got)
	}
	if got := req.Header.Get("X-Stainless-Runtime"); got != "node" {
		t.Errorf("X-Stainless-Runtime = %s, want client value node", got)
	}
	if got := req.Header.Get("X-Stainless-Runtime-Version"); got != "22.0.0" {
		t.Errorf("X-Stainless-Runtime-Version = %s, want client value 22.0.0", got)
	}
	if got := req.Header.Get("X-Stainless-OS"); got != "Darwin" {
		t.Errorf("X-Stainless-OS = %s, want client value Darwin", got)
	}
	if got := req.Header.Get("X-Stainless-Arch"); got != "arm64" {
		t.Errorf("X-Stainless-Arch = %s, want client value arm64", got)
	}
}

func TestDirectorSetClaudeCodeUpstream_PreservesClientAnthropicVersion(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req.Header.Set("Anthropic-Version", "2024-01-01")

	directorSetClaudeCodeUpstream(req, "https://api.anthropic.com", "token")

	if got := req.Header.Get("Anthropic-Version"); got != "2024-01-01" {
		t.Errorf("Anthropic-Version = %s, want client value 2024-01-01", got)
	}
}

func TestDirectorSetClaudeCodeUpstream_BetaQueryParamPreservesExisting(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages?existing=param", nil)

	directorSetClaudeCodeUpstream(req, "https://api.anthropic.com", "token")

	if req.URL.Query().Get("beta") != "true" {
		t.Errorf("expected beta=true query param, got %s", req.URL.RawQuery)
	}
	// Existing query params should be preserved.
	if req.URL.Query().Get("existing") != "param" {
		t.Errorf("expected existing query param to be preserved, got %s", req.URL.RawQuery)
	}
}

func TestSanitizeOutboundHeaders_ClaudeCodeHeaders(t *testing.T) {
	h := http.Header{}
	// Standard headers.
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer token")
	h.Set("Anthropic-Beta", "claude-code-20250219")
	h.Set("User-Agent", "claude-cli/2.5.0")
	h.Set("X-Stainless-Lang", "js")
	// Claude Code specific headers that should be preserved.
	h.Set("X-Claude-Code-Session-Id", "sess-123")
	h.Set("X-Client-Request-Id", "req-456")
	h.Set("X-Client-App", "my-app")
	h.Set("X-Anthropic-Additional-Protection", "true")
	h.Set("X-Claude-Remote-Container-Id", "container-789")
	h.Set("X-Claude-Remote-Session-Id", "remote-abc")
	// Header that should be stripped.
	h.Set("X-Custom-Disallowed", "should-be-removed")

	sanitized := sanitizeOutboundHeaders(h)

	// All Claude Code headers should pass through.
	for _, hdr := range []string{
		"X-Claude-Code-Session-Id",
		"X-Client-Request-Id",
		"X-Client-App",
		"X-Anthropic-Additional-Protection",
		"X-Claude-Remote-Container-Id",
		"X-Claude-Remote-Session-Id",
	} {
		if sanitized.Get(hdr) == "" {
			t.Errorf("expected %s to be preserved, but it was stripped", hdr)
		}
	}

	// Standard headers should still pass.
	if sanitized.Get("Content-Type") == "" {
		t.Error("Content-Type should be preserved")
	}
	if sanitized.Get("X-Stainless-Lang") == "" {
		t.Error("X-Stainless-Lang should be preserved")
	}

	// Disallowed header should be stripped.
	if sanitized.Get("X-Custom-Disallowed") != "" {
		t.Error("X-Custom-Disallowed should be stripped")
	}
}
