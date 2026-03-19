package proxy

import (
	"strings"
	"testing"
)

func TestTransformVertexBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		betas     []string
		wantCheck func(t *testing.T, result string)
	}{
		{
			name: "sets anthropic_version, strips model, preserves stream",
			body: `{"model":"claude-sonnet-4-20250514","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"Hi"}]}`,
			wantCheck: func(t *testing.T, result string) {
				if !strings.Contains(result, `"anthropic_version":"vertex-2023-10-16"`) {
					t.Errorf("expected anthropic_version, got %s", result)
				}
				if strings.Contains(result, `"model"`) {
					t.Errorf("model should be removed, got %s", result)
				}
				if !strings.Contains(result, `"stream":true`) {
					t.Errorf("stream should be preserved, got %s", result)
				}
				if !strings.Contains(result, `"max_tokens"`) {
					t.Errorf("max_tokens should remain, got %s", result)
				}
			},
		},
		{
			name: "preserves existing anthropic_version",
			body: `{"model":"m","stream":false,"anthropic_version":"custom-ver"}`,
			wantCheck: func(t *testing.T, result string) {
				if !strings.Contains(result, `"anthropic_version":"custom-ver"`) {
					t.Errorf("expected custom version preserved, got %s", result)
				}
			},
		},
		{
			name:  "injects betas into body",
			body:  `{"model":"m","stream":false}`,
			betas: []string{"interleaved-thinking-2025-05-14"},
			wantCheck: func(t *testing.T, result string) {
				if !strings.Contains(result, `"anthropic_beta"`) {
					t.Errorf("expected anthropic_beta, got %s", result)
				}
				if !strings.Contains(result, "interleaved-thinking-2025-05-14") {
					t.Errorf("expected beta value, got %s", result)
				}
			},
		},
		{
			name: "no betas added when none provided",
			body: `{"model":"m","stream":false}`,
			wantCheck: func(t *testing.T, result string) {
				if strings.Contains(result, `"anthropic_beta"`) {
					t.Errorf("no anthropic_beta expected, got %s", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := transformVertexBody([]byte(tt.body), tt.betas)
			if err != nil {
				t.Fatalf("transformVertexBody() error = %v", err)
			}
			tt.wantCheck(t, string(result))
		})
	}
}

func TestVertexEndpointURL(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		model     string
		streaming bool
		wantURL   string
	}{
		{
			name:      "non-streaming rawPredict",
			baseURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models",
			model:     "claude-sonnet-4-20250514",
			streaming: false,
			wantURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-20250514:rawPredict",
		},
		{
			name:      "streaming streamRawPredict",
			baseURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models",
			model:     "claude-sonnet-4-20250514",
			streaming: true,
			wantURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-20250514:streamRawPredict",
		},
		{
			name:      "trailing slash in base URL",
			baseURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models/",
			model:     "claude-opus-4-20250514",
			streaming: false,
			wantURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-opus-4-20250514:rawPredict",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vertexEndpointURL(tt.baseURL, tt.model, tt.streaming)
			if got != tt.wantURL {
				t.Errorf("vertexEndpointURL() = %q, want %q", got, tt.wantURL)
			}
		})
	}
}

func TestDirectorSetVertexUpstream(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req.Header.Set("x-api-key", "user-key")
	req.Header.Set("anthropic-version", "2023-06-01")

	directorSetVertexUpstream(req,
		"https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models",
		"ya29.fake-access-token",
		"claude-sonnet-4-20250514",
		true,
	)

	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
	if req.URL.Host != "us-east5-aiplatform.googleapis.com" {
		t.Errorf("host = %s, want us-east5-aiplatform.googleapis.com", req.URL.Host)
	}
	wantPath := "/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-20250514:streamRawPredict"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
	if req.Header.Get("Authorization") != "Bearer ya29.fake-access-token" {
		t.Errorf("Authorization = %s, want Bearer ya29.fake-access-token", req.Header.Get("Authorization"))
	}
	// Client headers should be removed
	if req.Header.Get("x-api-key") != "" {
		t.Errorf("x-api-key should be removed")
	}
	if req.Header.Get("anthropic-version") != "" {
		t.Errorf("anthropic-version should be removed")
	}
}

func TestDirectorSetVertexUpstream_NonStreaming(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)

	directorSetVertexUpstream(req,
		"https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models",
		"ya29.token",
		"claude-sonnet-4-20250514",
		false,
	)

	wantPath := "/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-20250514:rawPredict"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
}
