package proxy

import (
	"strings"
	"testing"
)

func TestTransformVertexAnthropicBody(t *testing.T) {
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
		{
			name: "strips output_format and output_config from body",
			body: `{"model":"m","stream":true,"output_format":{"type":"json"},"output_config":{"x":1},"max_tokens":1024}`,
			wantCheck: func(t *testing.T, result string) {
				if strings.Contains(result, `"output_format"`) {
					t.Errorf("output_format should be stripped, got %s", result)
				}
				if strings.Contains(result, `"output_config"`) {
					t.Errorf("output_config should be stripped, got %s", result)
				}
				if !strings.Contains(result, `"max_tokens"`) {
					t.Errorf("max_tokens should remain, got %s", result)
				}
			},
		},
		{
			name: "preserves context_management in body",
			body: `{"model":"m","stream":true,"context_management":{"edits":[{"type":"clear_tool_results"}]},"max_tokens":1024}`,
			wantCheck: func(t *testing.T, result string) {
				if !strings.Contains(result, `"context_management"`) {
					t.Errorf("context_management should be preserved, got %s", result)
				}
			},
		},
		{
			name: "strips scope from cache_control in messages",
			body: `{"model":"m","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","scope":"auto"}}]}]}`,
			wantCheck: func(t *testing.T, result string) {
				if strings.Contains(result, `"scope"`) {
					t.Errorf("scope should be stripped from cache_control, got %s", result)
				}
				if !strings.Contains(result, `"cache_control"`) {
					t.Errorf("cache_control itself should remain, got %s", result)
				}
				if !strings.Contains(result, `"ephemeral"`) {
					t.Errorf("cache_control.type should remain, got %s", result)
				}
			},
		},
		{
			name: "strips scope from cache_control in system and tools",
			body: `{"model":"m","stream":false,"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral","scope":"auto"}}],"tools":[{"name":"t","cache_control":{"type":"ephemeral","scope":"auto"}}]}`,
			wantCheck: func(t *testing.T, result string) {
				if strings.Contains(result, `"scope"`) {
					t.Errorf("scope should be stripped from all cache_control, got %s", result)
				}
				if !strings.Contains(result, `"cache_control"`) {
					t.Errorf("cache_control itself should remain, got %s", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := transformVertexAnthropicBody([]byte(tt.body), tt.betas)
			if err != nil {
				t.Fatalf("transformVertexAnthropicBody() error = %v", err)
			}
			tt.wantCheck(t, string(result))
		})
	}
}

func TestTransformVertexAnthropicBody_PreservesEagerInputStreaming(t *testing.T) {
	body := `{"model":"claude-sonnet-4-20250514","stream":true,"max_tokens":1024,"tools":[{"name":"make_file","eager_input_streaming":true,"input_schema":{"type":"object","properties":{"filename":{"type":"string"}}}}],"messages":[{"role":"user","content":"Hi"}]}`

	result, err := transformVertexAnthropicBody([]byte(body), nil)
	if err != nil {
		t.Fatalf("transformVertexAnthropicBody() error = %v", err)
	}

	got := string(result)

	// eager_input_streaming must survive the transformation.
	if !strings.Contains(got, `"eager_input_streaming":true`) {
		t.Errorf("eager_input_streaming should be preserved, got %s", got)
	}
	// model should be removed, stream preserved.
	if strings.Contains(got, `"model"`) {
		t.Errorf("model should be removed, got %s", got)
	}
	if !strings.Contains(got, `"stream":true`) {
		t.Errorf("stream should be preserved for Vertex, got %s", got)
	}
	if !strings.Contains(got, `"make_file"`) {
		t.Errorf("tool name should remain, got %s", got)
	}
}

func TestTransformVertexAnthropicBody_PreservesEagerInputStreamingWithCacheControlStripping(t *testing.T) {
	// Tool has both eager_input_streaming and cache_control with scope.
	// Scope should be stripped but eager_input_streaming preserved.
	body := `{"model":"m","stream":true,"tools":[{"name":"t","eager_input_streaming":true,"input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","scope":"auto"}}]}`

	result, err := transformVertexAnthropicBody([]byte(body), nil)
	if err != nil {
		t.Fatalf("transformVertexAnthropicBody() error = %v", err)
	}

	got := string(result)
	if !strings.Contains(got, `"eager_input_streaming":true`) {
		t.Errorf("eager_input_streaming should be preserved, got %s", got)
	}
	if strings.Contains(got, `"scope"`) {
		t.Errorf("scope should be stripped, got %s", got)
	}
	if !strings.Contains(got, `"cache_control"`) {
		t.Errorf("cache_control should remain, got %s", got)
	}
}

func TestFilterVertexAnthropicBetas_Allowlist(t *testing.T) {
	input := []string{
		"interleaved-thinking-2025-05-14",
		"prompt-caching-2024-07-31",
		"claude-code-20250219",
		"context-management-2025-06-27",
		"output-128k-2025-02-19",
		"web-search-2025-03-05",
	}
	body := []byte(`{}`)

	supported, dropped := filterVertexAnthropicBetas(input, body)

	wantSupported := []string{
		"interleaved-thinking-2025-05-14",
		"context-management-2025-06-27",
		"web-search-2025-03-05",
	}
	wantDropped := []string{
		"prompt-caching-2024-07-31",
		"claude-code-20250219",
		"output-128k-2025-02-19",
	}

	if len(supported) != len(wantSupported) {
		t.Fatalf("supported = %v, want %v", supported, wantSupported)
	}
	for i, b := range supported {
		if b != wantSupported[i] {
			t.Errorf("supported[%d] = %q, want %q", i, b, wantSupported[i])
		}
	}
	if len(dropped) != len(wantDropped) {
		t.Fatalf("dropped = %v, want %v", dropped, wantDropped)
	}
	for i, b := range dropped {
		if b != wantDropped[i] {
			t.Errorf("dropped[%d] = %q, want %q", i, b, wantDropped[i])
		}
	}
}

func TestFilterVertexAnthropicBetas_Rename(t *testing.T) {
	input := []string{"advanced-tool-use-2025-11-20"}
	body := []byte(`{}`)

	supported, _ := filterVertexAnthropicBetas(input, body)

	if len(supported) != 1 || supported[0] != "tool-search-tool-2025-10-19" {
		t.Errorf("expected advanced-tool-use renamed to tool-search-tool, got %v", supported)
	}
}

func TestFilterVertexAnthropicBetas_InferFromBody(t *testing.T) {
	// No beta headers, but body has context_management and web search tool.
	body := []byte(`{
		"context_management":{"edits":[{"type":"compact_20260112"},{"type":"clear_tool_results"}]},
		"tools":[{"type":"web_search_20250305","name":"web_search"}]
	}`)

	supported, _ := filterVertexAnthropicBetas(nil, body)

	wantSupported := []string{
		"compact-2026-01-12",
		"context-management-2025-06-27",
		"web-search-2025-03-05",
	}

	if len(supported) != len(wantSupported) {
		t.Fatalf("supported = %v, want %v", supported, wantSupported)
	}
	for i, b := range supported {
		if b != wantSupported[i] {
			t.Errorf("supported[%d] = %q, want %q", i, b, wantSupported[i])
		}
	}
}

func TestFilterVertexAnthropicBetas_InferToolSearch(t *testing.T) {
	body := []byte(`{"tools":[{"type":"tool_search","name":"ts"}]}`)

	supported, _ := filterVertexAnthropicBetas(nil, body)

	if len(supported) != 1 || supported[0] != "tool-search-tool-2025-10-19" {
		t.Errorf("expected tool-search-tool inferred, got %v", supported)
	}
}

func TestFilterVertexAnthropicBetas_NoDuplicates(t *testing.T) {
	// Header has context-management AND body infers it too — should deduplicate.
	input := []string{"context-management-2025-06-27"}
	body := []byte(`{"context_management":{"edits":[{"type":"clear_tool_results"}]}}`)

	supported, _ := filterVertexAnthropicBetas(input, body)

	if len(supported) != 1 {
		t.Errorf("expected 1 deduplicated beta, got %v", supported)
	}
}

func TestVertexAnthropicEndpointURL(t *testing.T) {
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
			got := vertexAnthropicEndpointURL(tt.baseURL, tt.model, tt.streaming)
			if got != tt.wantURL {
				t.Errorf("vertexAnthropicEndpointURL() = %q, want %q", got, tt.wantURL)
			}
		})
	}
}

func TestDirectorSetVertexAnthropicUpstream(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req.Header.Set("x-api-key", "user-key")
	req.Header.Set("anthropic-version", "2023-06-01")

	directorSetVertexAnthropicUpstream(req,
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

func TestDirectorSetVertexAnthropicUpstream_NonStreaming(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)

	directorSetVertexAnthropicUpstream(req,
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
