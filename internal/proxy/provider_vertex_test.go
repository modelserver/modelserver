package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
	"golang.org/x/oauth2"
)

func TestVertexAnthropicTransformer_SetUpstream(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "ya29.test-token"},
	}
	tm.registerWithSource("u1", mock)

	transformer := &VertexAnthropicTransformer{}
	transformer.SetTokenManager(tm)

	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req = withVertexAnthropicParams(req, "claude-sonnet-4-20250514", true)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models",
	}

	err := transformer.SetUpstream(req, upstream, "ignored-api-key")
	if err != nil {
		t.Fatalf("SetUpstream() error = %v", err)
	}

	if req.Header.Get("Authorization") != "Bearer ya29.test-token" {
		t.Errorf("Authorization = %q, want %q", req.Header.Get("Authorization"), "Bearer ya29.test-token")
	}
	wantPath := "/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-20250514:streamRawPredict"
	if req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", req.URL.Path, wantPath)
	}
}

func TestVertexAnthropicTransformer_SetUpstream_MovesBetasToHeader(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "ya29.token"},
	}
	tm.registerWithSource("u1", mock)

	transformer := &VertexAnthropicTransformer{}
	transformer.SetTokenManager(tm)

	// Body with anthropic_beta array (as produced by TransformBody).
	body := []byte(`{"anthropic_version":"vertex-2023-10-16","anthropic_beta":["interleaved-thinking-2025-05-14","context-management-2025-06-27"],"stream":true,"max_tokens":1024}`)
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req = withVertexAnthropicParams(req, "claude-sonnet-4-20250514", true)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err != nil {
		t.Fatalf("SetUpstream() error = %v", err)
	}

	// Betas should now be in the HTTP header.
	gotHeader := req.Header.Get("anthropic-beta")
	if !strings.Contains(gotHeader, "interleaved-thinking-2025-05-14") {
		t.Errorf("anthropic-beta header should contain interleaved-thinking, got %q", gotHeader)
	}
	if !strings.Contains(gotHeader, "context-management-2025-06-27") {
		t.Errorf("anthropic-beta header should contain context-management, got %q", gotHeader)
	}

	// Body should no longer contain anthropic_beta.
	resultBody, _ := io.ReadAll(req.Body)
	if strings.Contains(string(resultBody), "anthropic_beta") {
		t.Errorf("anthropic_beta should be removed from body, got %s", resultBody)
	}
	// Body should still have other fields.
	if !strings.Contains(string(resultBody), "anthropic_version") {
		t.Errorf("anthropic_version should remain in body, got %s", resultBody)
	}
}

func TestVertexAnthropicTransformer_SetUpstream_TokenError(t *testing.T) {
	tm := NewVertexTokenManager()
	transformer := &VertexAnthropicTransformer{}
	transformer.SetTokenManager(tm)

	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req = withVertexAnthropicParams(req, "claude-sonnet-4-20250514", false)

	upstream := &types.Upstream{
		ID:      "unknown",
		BaseURL: "https://example.com/v1/projects/p/locations/r/publishers/anthropic/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err == nil {
		t.Fatal("expected error for unregistered upstream, got nil")
	}
}

func TestVertexAnthropicTransformer_SetUpstream_NilTokenManager(t *testing.T) {
	transformer := &VertexAnthropicTransformer{} // no SetTokenManager called

	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req = withVertexAnthropicParams(req, "claude-sonnet-4-20250514", false)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://example.com/v1/projects/p/locations/r/publishers/anthropic/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err == nil {
		t.Fatal("expected error for nil token manager, got nil")
	}
}

func TestVertexAnthropicTransformer_TransformBody(t *testing.T) {
	transformer := &VertexAnthropicTransformer{}

	headers := http.Header{}
	headers.Set("anthropic-beta", "interleaved-thinking-2025-05-14,claude-code-20250219,prompt-caching-2024-07-31,context-management-2025-06-27")

	body := []byte(`{"model":"claude-sonnet-4","stream":true,"max_tokens":1024}`)
	result, err := transformer.TransformBody(body, "claude-sonnet-4", true, headers)
	if err != nil {
		t.Fatalf("TransformBody() error = %v", err)
	}
	s := string(result)

	if contains(s, `"model"`) {
		t.Errorf("model should be removed: %s", s)
	}
	if !contains(s, `"stream":true`) {
		t.Errorf("stream should be preserved: %s", s)
	}
	if !contains(s, `"anthropic_version"`) {
		t.Errorf("anthropic_version should be set: %s", s)
	}
	// Allowlisted betas should pass through
	if !contains(s, "interleaved-thinking-2025-05-14") {
		t.Errorf("interleaved-thinking should pass through: %s", s)
	}
	if !contains(s, "context-management-2025-06-27") {
		t.Errorf("context-management should pass through: %s", s)
	}
	// Non-allowlisted betas should be dropped
	if contains(s, "claude-code-20250219") {
		t.Errorf("claude-code beta should be dropped (not in allowlist): %s", s)
	}
	if contains(s, "prompt-caching-2024-07-31") {
		t.Errorf("prompt-caching beta should be dropped (not in allowlist): %s", s)
	}
}
