package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSplitBetaHeaders(t *testing.T) {
	tests := []struct {
		name   string
		input  []string
		want   []string
	}{
		{"nil", nil, nil},
		{"single value", []string{"beta1"}, []string{"beta1"}},
		{"comma-separated", []string{"beta1,beta2, beta3"}, []string{"beta1", "beta2", "beta3"}},
		{"multiple headers", []string{"beta1", "beta2"}, []string{"beta1", "beta2"}},
		{"mixed", []string{"beta1,beta2", "beta3"}, []string{"beta1", "beta2", "beta3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitBetaHeaders(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("splitBetaHeaders(%v) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitBetaHeaders(%v)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFilterBedrockBetas(t *testing.T) {
	input := []string{
		"interleaved-thinking-2025-05-14",
		"claude-code-20250219",
		"effort-2025-11-24",
		"redact-thinking-2026-02-12",
		"prompt-caching-scope-2026-01-05",
	}
	supported, dropped := filterBedrockBetas(input)

	wantSupported := []string{"interleaved-thinking-2025-05-14", "effort-2025-11-24"}
	wantDropped := []string{"claude-code-20250219", "redact-thinking-2026-02-12", "prompt-caching-scope-2026-01-05"}

	if len(supported) != len(wantSupported) {
		t.Fatalf("supported = %v, want %v", supported, wantSupported)
	}
	for i := range supported {
		if supported[i] != wantSupported[i] {
			t.Errorf("supported[%d] = %q, want %q", i, supported[i], wantSupported[i])
		}
	}
	if len(dropped) != len(wantDropped) {
		t.Fatalf("dropped = %v, want %v", dropped, wantDropped)
	}
	for i := range dropped {
		if dropped[i] != wantDropped[i] {
			t.Errorf("dropped[%d] = %q, want %q", i, dropped[i], wantDropped[i])
		}
	}
}

func TestTransformBedrockBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		betas     []string
		wantCheck func(t *testing.T, result string)
		wantErr   bool
	}{
		{
			name: "sets anthropic_version when absent",
			body: `{"model":"claude-sonnet-4-20250514","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"Hi"}]}`,
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
			name: "preserves existing anthropic_version",
			body: `{"model":"m","stream":false,"anthropic_version":"custom-version"}`,
			wantCheck: func(t *testing.T, result string) {
				if !contains(result, `"anthropic_version":"custom-version"`) {
					t.Errorf("expected custom version preserved, got %s", result)
				}
			},
		},
		{
			name:  "injects betas into body",
			body:  `{"model":"m","stream":false}`,
			betas: []string{"prompt-caching-2024-07-31", "max-tokens-3-5-sonnet-2024-07-15"},
			wantCheck: func(t *testing.T, result string) {
				if !contains(result, `"anthropic_beta"`) {
					t.Errorf("expected anthropic_beta in body, got %s", result)
				}
				if !contains(result, "prompt-caching-2024-07-31") || !contains(result, "max-tokens-3-5-sonnet-2024-07-15") {
					t.Errorf("expected beta values in body, got %s", result)
				}
			},
		},
		{
			name: "no anthropic_beta added when no betas provided",
			body: `{"model":"m","stream":false}`,
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
	// RawPath must preserve the colon — url.PathEscape keeps ":" intact,
	// whereas url.QueryEscape would turn it into "%3A".
	if strings.Contains(req.URL.RawPath, "%3A") {
		t.Errorf("RawPath should not encode colon, got %s", req.URL.RawPath)
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

func TestDirectorSetUpstream_BaseURLWithPath(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	directorSetUpstream(req, "https://custom-proxy.example.com/prefix", "key123")

	if req.URL.Host != "custom-proxy.example.com" {
		t.Errorf("host = %s, want custom-proxy.example.com", req.URL.Host)
	}
	if req.URL.Path != "/prefix/v1/messages" {
		t.Errorf("path = %s, want /prefix/v1/messages", req.URL.Path)
	}
	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
}

func TestDirectorSetOpenAIUpstream_AzureBaseURL(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/responses", nil)
	directorSetOpenAIUpstream(req, "https://myresource.cognitiveservices.azure.com/openai", "azure-key")

	if req.URL.Host != "myresource.cognitiveservices.azure.com" {
		t.Errorf("host = %s, want myresource.cognitiveservices.azure.com", req.URL.Host)
	}
	if req.URL.Path != "/openai/v1/responses" {
		t.Errorf("path = %s, want /openai/v1/responses", req.URL.Path)
	}
	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
	if req.Header.Get("Authorization") != "Bearer azure-key" {
		t.Errorf("Authorization = %s, want Bearer azure-key", req.Header.Get("Authorization"))
	}
}

func TestDirectorSetOpenAIUpstream_PlainBaseURL(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/responses", nil)
	directorSetOpenAIUpstream(req, "https://api.openai.com", "sk-key")

	if req.URL.Host != "api.openai.com" {
		t.Errorf("host = %s, want api.openai.com", req.URL.Host)
	}
	if req.URL.Path != "/v1/responses" {
		t.Errorf("path = %s, want /v1/responses", req.URL.Path)
	}
}

func TestDirectorSetBedrockUpstream_BaseURLWithPath(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	directorSetBedrockUpstream(req, "https://custom-proxy.example.com/bedrock", "token", "anthropic.claude-3-sonnet-20240229-v1:0", false)

	if req.URL.Host != "custom-proxy.example.com" {
		t.Errorf("host = %s, want custom-proxy.example.com", req.URL.Host)
	}
	if req.URL.Path != "/bedrock/model/anthropic.claude-3-sonnet-20240229-v1:0/invoke" {
		t.Errorf("path = %s, want /bedrock/model/anthropic.claude-3-sonnet-20240229-v1:0/invoke", req.URL.Path)
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
