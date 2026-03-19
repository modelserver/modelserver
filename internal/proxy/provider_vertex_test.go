package proxy

import (
	"net/http"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
	"golang.org/x/oauth2"
)

func TestVertexTransformer_SetUpstream(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "ya29.test-token"},
	}
	tm.registerWithSource("u1", mock)

	transformer := &VertexTransformer{}
	transformer.SetTokenManager(tm)

	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req = withVertexParams(req, "claude-sonnet-4-20250514", true)

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

func TestVertexTransformer_SetUpstream_TokenError(t *testing.T) {
	tm := NewVertexTokenManager()
	transformer := &VertexTransformer{}
	transformer.SetTokenManager(tm)

	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req = withVertexParams(req, "claude-sonnet-4-20250514", false)

	upstream := &types.Upstream{
		ID:      "unknown",
		BaseURL: "https://example.com/v1/projects/p/locations/r/publishers/anthropic/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err == nil {
		t.Fatal("expected error for unregistered upstream, got nil")
	}
}

func TestVertexTransformer_SetUpstream_NilTokenManager(t *testing.T) {
	transformer := &VertexTransformer{} // no SetTokenManager called

	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req = withVertexParams(req, "claude-sonnet-4-20250514", false)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://example.com/v1/projects/p/locations/r/publishers/anthropic/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err == nil {
		t.Fatal("expected error for nil token manager, got nil")
	}
}

func TestVertexTransformer_TransformBody(t *testing.T) {
	transformer := &VertexTransformer{}

	headers := http.Header{}
	headers.Set("anthropic-beta", "interleaved-thinking-2025-05-14,claude-code-20250219")

	body := []byte(`{"model":"claude-sonnet-4","stream":true,"max_tokens":1024}`)
	result, err := transformer.TransformBody(body, "claude-sonnet-4", true, headers)
	if err != nil {
		t.Fatalf("TransformBody() error = %v", err)
	}
	s := string(result)

	if contains(s, `"model"`) {
		t.Errorf("model should be removed: %s", s)
	}
	if contains(s, `"stream"`) {
		t.Errorf("stream should be removed: %s", s)
	}
	if !contains(s, `"anthropic_version"`) {
		t.Errorf("anthropic_version should be set: %s", s)
	}
	if !contains(s, "interleaved-thinking-2025-05-14") {
		t.Errorf("supported beta should be in body: %s", s)
	}
	if contains(s, "claude-code-20250219") {
		t.Errorf("unsupported beta should be dropped: %s", s)
	}
}
