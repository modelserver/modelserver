package proxy

import (
	"net/http"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
	"golang.org/x/oauth2"
)

func TestVertexGoogleEndpointURL(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		model     string
		streaming bool
		wantURL   string
	}{
		{
			name:      "non-streaming generateContent",
			baseURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/my-proj/locations/us-central1/publishers/google/models",
			model:     "gemini-2.5-flash",
			streaming: false,
			wantURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/my-proj/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent",
		},
		{
			name:      "streaming streamGenerateContent",
			baseURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/my-proj/locations/us-central1/publishers/google/models",
			model:     "gemini-2.5-pro",
			streaming: true,
			wantURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/my-proj/locations/us-central1/publishers/google/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
		},
		{
			name:      "trailing slash in base URL",
			baseURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models/",
			model:     "gemini-3-flash",
			streaming: false,
			wantURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models/gemini-3-flash:generateContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vertexGoogleEndpointURL(tt.baseURL, tt.model, tt.streaming)
			if got != tt.wantURL {
				t.Errorf("vertexGoogleEndpointURL() = %q, want %q", got, tt.wantURL)
			}
		})
	}
}

func TestDirectorSetVertexGoogleUpstream(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req.Header.Set("x-api-key", "user-key")
	req.Header.Set("anthropic-version", "2023-06-01")

	directorSetVertexGoogleUpstream(req,
		"https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models",
		"ya29.fake-access-token",
		"gemini-2.5-flash",
		false,
	)

	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
	if req.URL.Host != "us-central1-aiplatform.googleapis.com" {
		t.Errorf("host = %s, want us-central1-aiplatform.googleapis.com", req.URL.Host)
	}
	wantPath := "/v1beta1/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
	if req.Header.Get("Authorization") != "Bearer ya29.fake-access-token" {
		t.Errorf("Authorization = %s, want Bearer ya29.fake-access-token", req.Header.Get("Authorization"))
	}
	if req.Header.Get("x-api-key") != "" {
		t.Errorf("x-api-key should be removed")
	}
	if req.Header.Get("anthropic-version") != "" {
		t.Errorf("anthropic-version should be removed")
	}
}

func TestDirectorSetVertexGoogleUpstream_Streaming(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-pro:streamGenerateContent", nil)

	directorSetVertexGoogleUpstream(req,
		"https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models",
		"ya29.token",
		"gemini-2.5-pro",
		true,
	)

	wantPath := "/v1beta1/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-pro:streamGenerateContent"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
	if req.URL.RawQuery != "alt=sse" {
		t.Errorf("query = %s, want alt=sse", req.URL.RawQuery)
	}
}

func TestVertexGoogleTransformer_TransformBody_NoOp(t *testing.T) {
	transformer := &VertexGoogleTransformer{}
	input := []byte(`{"contents":[{"parts":[{"text":"hello"}],"role":"user"}]}`)
	output, err := transformer.TransformBody(input, "gemini-2.5-flash", false, http.Header{})
	if err != nil {
		t.Fatalf("TransformBody() error = %v", err)
	}
	if string(output) != string(input) {
		t.Errorf("TransformBody() should be no-op, got %s", string(output))
	}
}

func TestVertexGoogleTransformer_SetUpstream(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "ya29.test-token"},
	}
	tm.registerWithSource("u1", mock)

	transformer := &VertexGoogleTransformer{}
	transformer.SetTokenManager(tm)

	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req = withVertexGoogleParams(req, "gemini-2.5-flash", false)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models",
	}

	err := transformer.SetUpstream(req, upstream, "ignored-api-key")
	if err != nil {
		t.Fatalf("SetUpstream() error = %v", err)
	}

	if req.Header.Get("Authorization") != "Bearer ya29.test-token" {
		t.Errorf("Authorization = %q, want %q", req.Header.Get("Authorization"), "Bearer ya29.test-token")
	}
	wantPath := "/v1beta1/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"
	if req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", req.URL.Path, wantPath)
	}
}

func TestVertexGoogleTransformer_SetUpstream_Streaming(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "ya29.stream-token"},
	}
	tm.registerWithSource("u1", mock)

	transformer := &VertexGoogleTransformer{}
	transformer.SetTokenManager(tm)

	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:streamGenerateContent", nil)
	req = withVertexGoogleParams(req, "gemini-2.5-flash", true)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err != nil {
		t.Fatalf("SetUpstream() error = %v", err)
	}

	wantPath := "/v1beta1/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent"
	if req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", req.URL.Path, wantPath)
	}
	if req.URL.RawQuery != "alt=sse" {
		t.Errorf("query = %q, want alt=sse", req.URL.RawQuery)
	}
}

func TestVertexGoogleTransformer_SetUpstream_TokenError(t *testing.T) {
	tm := NewVertexTokenManager()
	transformer := &VertexGoogleTransformer{}
	transformer.SetTokenManager(tm)

	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req = withVertexGoogleParams(req, "gemini-2.5-flash", false)

	upstream := &types.Upstream{
		ID:      "unknown",
		BaseURL: "https://example.com/v1beta1/projects/p/locations/r/publishers/google/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err == nil {
		t.Fatal("expected error for unregistered upstream, got nil")
	}
}

func TestVertexGoogleTransformer_SetUpstream_NilTokenManager(t *testing.T) {
	transformer := &VertexGoogleTransformer{} // no SetTokenManager called

	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req = withVertexGoogleParams(req, "gemini-2.5-flash", false)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://example.com/v1beta1/projects/p/locations/r/publishers/google/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err == nil {
		t.Fatal("expected error for nil token manager, got nil")
	}
}
