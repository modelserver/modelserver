package proxy

import (
	"net/http"
	"testing"
)

func TestGeminiEndpointURL(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		model     string
		streaming bool
		wantURL   string
	}{
		{
			name:      "non-streaming generateContent",
			baseURL:   "https://generativelanguage.googleapis.com",
			model:     "gemini-2.5-flash",
			streaming: false,
			wantURL:   "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent",
		},
		{
			name:      "streaming streamGenerateContent",
			baseURL:   "https://generativelanguage.googleapis.com",
			model:     "gemini-2.5-pro",
			streaming: true,
			wantURL:   "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
		},
		{
			name:      "trailing slash in base URL",
			baseURL:   "https://generativelanguage.googleapis.com/",
			model:     "gemini-3-flash-preview",
			streaming: false,
			wantURL:   "https://generativelanguage.googleapis.com/v1beta/models/gemini-3-flash-preview:generateContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := geminiEndpointURL(tt.baseURL, tt.model, tt.streaming)
			if got != tt.wantURL {
				t.Errorf("geminiEndpointURL() = %q, want %q", got, tt.wantURL)
			}
		})
	}
}

func TestDirectorSetGeminiUpstream(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req.Header.Set("x-api-key", "user-key")
	req.Header.Set("anthropic-version", "2023-06-01")

	directorSetGeminiUpstream(req,
		"https://generativelanguage.googleapis.com",
		"AIzaSy-fake-key",
		"gemini-2.5-flash",
		false,
	)

	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
	if req.URL.Host != "generativelanguage.googleapis.com" {
		t.Errorf("host = %s, want generativelanguage.googleapis.com", req.URL.Host)
	}
	wantPath := "/v1beta/models/gemini-2.5-flash:generateContent"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
	if req.Header.Get("x-goog-api-key") != "AIzaSy-fake-key" {
		t.Errorf("x-goog-api-key = %s, want AIzaSy-fake-key", req.Header.Get("x-goog-api-key"))
	}
	// Anthropic headers should be removed
	if req.Header.Get("x-api-key") != "" {
		t.Errorf("x-api-key should be removed")
	}
	if req.Header.Get("anthropic-version") != "" {
		t.Errorf("anthropic-version should be removed")
	}
}

func TestDirectorSetGeminiUpstream_Streaming(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-pro:streamGenerateContent", nil)

	directorSetGeminiUpstream(req,
		"https://generativelanguage.googleapis.com",
		"AIzaSy-key",
		"gemini-2.5-pro",
		true,
	)

	wantPath := "/v1beta/models/gemini-2.5-pro:streamGenerateContent"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
	if req.URL.RawQuery != "alt=sse" {
		t.Errorf("query = %s, want alt=sse", req.URL.RawQuery)
	}
}

func TestGeminiTransformerTransformBody_NoOp(t *testing.T) {
	transformer := &GeminiTransformer{}
	input := []byte(`{"contents":[{"parts":[{"text":"hello"}],"role":"user"}]}`)
	output, err := transformer.TransformBody(input, "gemini-2.5-flash", false, http.Header{})
	if err != nil {
		t.Fatalf("TransformBody() error = %v", err)
	}
	if string(output) != string(input) {
		t.Errorf("TransformBody() should be no-op, got %s", string(output))
	}
}
