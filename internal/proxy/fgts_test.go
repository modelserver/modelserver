package proxy

import (
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

func TestApplyFGTS_WithBetaAndTools(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-6",
		"tools": [
			{"name": "bash", "description": "Run bash", "input_schema": {"type": "object"}},
			{"name": "read", "description": "Read file", "input_schema": {"type": "object"}}
		],
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	headers := http.Header{}
	headers.Set("Anthropic-Beta", "fine-grained-tool-streaming-2025-05-14")

	result, err := applyFGTS(body, headers)
	if err != nil {
		t.Fatalf("applyFGTS: %v", err)
	}

	tools := gjson.GetBytes(result, "tools")
	if !tools.IsArray() {
		t.Fatal("tools is not an array")
	}

	for i, tool := range tools.Array() {
		eis := tool.Get("eager_input_streaming")
		if !eis.Exists() || !eis.Bool() {
			t.Errorf("tool %d: eager_input_streaming = %v, want true", i, eis)
		}
		// Verify original fields are preserved.
		if !tool.Get("name").Exists() {
			t.Errorf("tool %d: name field missing", i)
		}
		if !tool.Get("input_schema").Exists() {
			t.Errorf("tool %d: input_schema field missing", i)
		}
	}

	// Verify non-tool fields are preserved.
	if gjson.GetBytes(result, "model").String() != "claude-sonnet-4-6" {
		t.Error("model field was modified")
	}
}

func TestApplyFGTS_WithoutBeta(t *testing.T) {
	body := []byte(`{"tools": [{"name": "bash"}]}`)
	headers := http.Header{}

	result, err := applyFGTS(body, headers)
	if err != nil {
		t.Fatalf("applyFGTS: %v", err)
	}

	eis := gjson.GetBytes(result, "tools.0.eager_input_streaming")
	if eis.Exists() {
		t.Error("eager_input_streaming should not be set without the beta header")
	}
}

func TestApplyFGTS_WithBetaButNoTools(t *testing.T) {
	body := []byte(`{"model": "claude-sonnet-4-6", "messages": []}`)
	headers := http.Header{}
	headers.Set("Anthropic-Beta", "fine-grained-tool-streaming-2025-05-14")

	result, err := applyFGTS(body, headers)
	if err != nil {
		t.Fatalf("applyFGTS: %v", err)
	}

	if string(result) != string(body) {
		t.Errorf("body should be unchanged when no tools present\ngot:  %s\nwant: %s", result, body)
	}
}

func TestApplyFGTS_BetaAmongMultiple(t *testing.T) {
	body := []byte(`{"tools": [{"name": "bash"}]}`)
	headers := http.Header{}
	headers.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")

	result, err := applyFGTS(body, headers)
	if err != nil {
		t.Fatalf("applyFGTS: %v", err)
	}

	eis := gjson.GetBytes(result, "tools.0.eager_input_streaming")
	if !eis.Exists() || !eis.Bool() {
		t.Errorf("eager_input_streaming = %v, want true (beta among multiple)", eis)
	}
}

func TestApplyFGTS_ToolAlreadyHasField(t *testing.T) {
	body := []byte(`{"tools": [{"name": "bash", "eager_input_streaming": true}]}`)
	headers := http.Header{}
	headers.Set("Anthropic-Beta", "fine-grained-tool-streaming-2025-05-14")

	result, err := applyFGTS(body, headers)
	if err != nil {
		t.Fatalf("applyFGTS: %v", err)
	}

	eis := gjson.GetBytes(result, "tools.0.eager_input_streaming")
	if !eis.Bool() {
		t.Errorf("eager_input_streaming = %v, want true", eis)
	}
}

func TestHasBeta(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		beta    string
		want    bool
	}{
		{"exact match", "fine-grained-tool-streaming-2025-05-14", fgtsBeta, true},
		{"comma separated", "interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14", fgtsBeta, true},
		{"not present", "interleaved-thinking-2025-05-14", fgtsBeta, false},
		{"empty header", "", fgtsBeta, false},
		{"with spaces", "interleaved-thinking-2025-05-14, fine-grained-tool-streaming-2025-05-14", fgtsBeta, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			if tt.header != "" {
				headers.Set("Anthropic-Beta", tt.header)
			}
			if got := hasBeta(headers, tt.beta); got != tt.want {
				t.Errorf("hasBeta() = %v, want %v", got, tt.want)
			}
		})
	}
}
