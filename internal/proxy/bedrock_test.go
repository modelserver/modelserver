package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTransformBedrockBody(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		betas      []string
		wantCheck  func(t *testing.T, result string)
		wantErr    bool
	}{
		{
			name:  "sets anthropic_version when absent",
			body:  `{"model":"claude-sonnet-4-20250514","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"Hi"}]}`,
			betas: nil,
			wantCheck: func(t *testing.T, result string) {
				if !contains(result, `"anthropic_version":"bedrock-2023-05-31"`) {
					t.Errorf("expected anthropic_version, got %s", result)
				}
				if contains(result, `"model"`) {
					t.Errorf("expected model to be removed, got %s", result)
				}
				if contains(result, `"stream"`) {
					t.Errorf("expected stream to be removed, got %s", result)
				}
				if !contains(result, `"max_tokens"`) {
					t.Errorf("expected max_tokens to remain, got %s", result)
				}
			},
		},
		{
			name:  "preserves existing anthropic_version",
			body:  `{"model":"m","stream":false,"anthropic_version":"custom-version"}`,
			betas: nil,
			wantCheck: func(t *testing.T, result string) {
				if !contains(result, `"anthropic_version":"custom-version"`) {
					t.Errorf("expected custom version preserved, got %s", result)
				}
			},
		},
		{
			name:  "moves beta header values into body",
			body:  `{"model":"m","stream":false}`,
			betas: []string{"prompt-caching-2024-07-31", "max-tokens-3-5-sonnet-2024-07-15"},
			wantCheck: func(t *testing.T, result string) {
				if !contains(result, `"anthropic_beta"`) {
					t.Errorf("expected anthropic_beta in body, got %s", result)
				}
				if !contains(result, "prompt-caching-2024-07-31") {
					t.Errorf("expected beta value in body, got %s", result)
				}
			},
		},
		{
			name:  "no betas means no anthropic_beta field",
			body:  `{"model":"m","stream":false}`,
			betas: nil,
			wantCheck: func(t *testing.T, result string) {
				if contains(result, `"anthropic_beta"`) {
					t.Errorf("expected no anthropic_beta in body, got %s", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := transformBedrockBody([]byte(tt.body), tt.betas)
			if (err != nil) != tt.wantErr {
				t.Fatalf("transformBedrockBody() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantCheck != nil {
				tt.wantCheck(t, string(result))
			}
		})
	}
}

func TestBedrockURLPath(t *testing.T) {
	tests := []struct {
		model     string
		streaming bool
		want      string
	}{
		{
			model:     "anthropic.claude-3-sonnet-20240229-v1:0",
			streaming: false,
			want:      "/model/anthropic.claude-3-sonnet-20240229-v1:0/invoke",
		},
		{
			model:     "anthropic.claude-3-sonnet-20240229-v1:0",
			streaming: true,
			want:      "/model/anthropic.claude-3-sonnet-20240229-v1:0/invoke-with-response-stream",
		},
		{
			model:     "us.anthropic.claude-sonnet-4-20250514-v1:0",
			streaming: true,
			want:      "/model/us.anthropic.claude-sonnet-4-20250514-v1:0/invoke-with-response-stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := bedrockURLPath(tt.model, tt.streaming)
			if got != tt.want {
				t.Errorf("bedrockURLPath(%q, %v) = %q, want %q", tt.model, tt.streaming, got, tt.want)
			}
		})
	}
}

func TestDirectorSetBedrockUpstream(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req.Header.Set("x-api-key", "user-key")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "some-beta")

	directorSetBedrockUpstream(req, "https://bedrock-runtime.us-west-2.amazonaws.com", "test-token", "anthropic.claude-3-sonnet-20240229-v1:0", true)

	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
	if req.URL.Host != "bedrock-runtime.us-west-2.amazonaws.com" {
		t.Errorf("host = %s, want bedrock-runtime.us-west-2.amazonaws.com", req.URL.Host)
	}
	if want := "/model/anthropic.claude-3-sonnet-20240229-v1:0/invoke-with-response-stream"; req.URL.Path != want {
		t.Errorf("path = %s, want %s", req.URL.Path, want)
	}
	if req.Header.Get("Authorization") != "Bearer test-token" {
		t.Errorf("Authorization = %s, want Bearer test-token", req.Header.Get("Authorization"))
	}
	if req.Header.Get("x-api-key") != "" {
		t.Errorf("x-api-key should be removed, got %s", req.Header.Get("x-api-key"))
	}
	if req.Header.Get("anthropic-version") != "" {
		t.Errorf("anthropic-version should be removed, got %s", req.Header.Get("anthropic-version"))
	}
	if req.Header.Get("anthropic-beta") != "" {
		t.Errorf("anthropic-beta should be removed, got %s", req.Header.Get("anthropic-beta"))
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func mustNewRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
