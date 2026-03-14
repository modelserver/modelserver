package proxy

import (
	"testing"

	"github.com/openai/openai-go/v3/responses"
)

func TestParseOpenAINonStreamingResponse(t *testing.T) {
	body := []byte(`{
		"id": "resp_abc123",
		"model": "gpt-4o-2024-08-06",
		"usage": {
			"input_tokens": 120,
			"output_tokens": 50,
			"total_tokens": 170,
			"input_tokens_details": {"cached_tokens": 80},
			"output_tokens_details": {"reasoning_tokens": 0}
		}
	}`)

	model, respID, usage, err := ParseOpenAINonStreamingResponse(body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if model != "gpt-4o-2024-08-06" {
		t.Errorf("model = %q, want %q", model, "gpt-4o-2024-08-06")
	}
	if respID != "resp_abc123" {
		t.Errorf("respID = %q, want %q", respID, "resp_abc123")
	}
	if usage.InputTokens != 120 {
		t.Errorf("input_tokens = %d, want 120", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("output_tokens = %d, want 50", usage.OutputTokens)
	}
	if usage.TotalTokens != 170 {
		t.Errorf("total_tokens = %d, want 170", usage.TotalTokens)
	}
	if usage.InputTokensDetails.CachedTokens != 80 {
		t.Errorf("cached_tokens = %d, want 80", usage.InputTokensDetails.CachedTokens)
	}
	if usage.OutputTokensDetails.ReasoningTokens != 0 {
		t.Errorf("reasoning_tokens = %d, want 0", usage.OutputTokensDetails.ReasoningTokens)
	}
}

func TestParseOpenAINonStreamingResponse_NoCachedTokens(t *testing.T) {
	body := []byte(`{
		"id": "resp_nocache",
		"model": "gpt-4o-mini",
		"usage": {
			"input_tokens": 50,
			"output_tokens": 30,
			"total_tokens": 80,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens_details": {"reasoning_tokens": 0}
		}
	}`)

	model, respID, usage, err := ParseOpenAINonStreamingResponse(body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if model != "gpt-4o-mini" {
		t.Errorf("model = %q, want %q", model, "gpt-4o-mini")
	}
	if respID != "resp_nocache" {
		t.Errorf("respID = %q, want %q", respID, "resp_nocache")
	}
	if usage.InputTokens != 50 {
		t.Errorf("input_tokens = %d, want 50", usage.InputTokens)
	}
	if usage.OutputTokens != 30 {
		t.Errorf("output_tokens = %d, want 30", usage.OutputTokens)
	}
	if usage.TotalTokens != 80 {
		t.Errorf("total_tokens = %d, want 80", usage.TotalTokens)
	}
	if usage.InputTokensDetails.CachedTokens != 0 {
		t.Errorf("cached_tokens = %d, want 0", usage.InputTokensDetails.CachedTokens)
	}
}

func TestParseOpenAINonStreamingResponse_InvalidJSON(t *testing.T) {
	body := []byte(`{not valid json`)

	_, _, _, err := ParseOpenAINonStreamingResponse(body)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseOpenAIStreamEvent_ResponseCreated(t *testing.T) {
	data := []byte(`{"type":"response.created","response":{"id":"resp_stream1","model":"gpt-4o-2024-08-06","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`)

	eventType, model, respID, _, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "response.created" {
		t.Errorf("eventType = %q, want %q", eventType, "response.created")
	}
	if model != "gpt-4o-2024-08-06" {
		t.Errorf("model = %q, want %q", model, "gpt-4o-2024-08-06")
	}
	if respID != "resp_stream1" {
		t.Errorf("respID = %q, want %q", respID, "resp_stream1")
	}
	if hasUsage {
		t.Error("expected hasUsage = false for response.created")
	}
}

func TestParseOpenAIStreamEvent_OutputTextDelta(t *testing.T) {
	data := []byte(`{"type":"response.output_text.delta","delta":"Hello"}`)

	eventType, _, _, _, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "response.output_text.delta" {
		t.Errorf("eventType = %q, want %q", eventType, "response.output_text.delta")
	}
	if hasUsage {
		t.Error("expected hasUsage = false for output_text.delta")
	}
}

func TestParseOpenAIStreamEvent_ResponseCompleted(t *testing.T) {
	data := []byte(`{"type":"response.completed","response":{"id":"resp_done","model":"gpt-4o-2024-08-06","usage":{"input_tokens":120,"output_tokens":50,"total_tokens":170,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":0}}}}`)

	eventType, model, respID, usage, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "response.completed" {
		t.Errorf("eventType = %q, want %q", eventType, "response.completed")
	}
	if model != "gpt-4o-2024-08-06" {
		t.Errorf("model = %q, want %q", model, "gpt-4o-2024-08-06")
	}
	if respID != "resp_done" {
		t.Errorf("respID = %q, want %q", respID, "resp_done")
	}
	if !hasUsage {
		t.Fatal("expected hasUsage = true for response.completed")
	}
	if usage.InputTokens != 120 {
		t.Errorf("input_tokens = %d, want 120", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("output_tokens = %d, want 50", usage.OutputTokens)
	}
	if usage.TotalTokens != 170 {
		t.Errorf("total_tokens = %d, want 170", usage.TotalTokens)
	}
	if usage.InputTokensDetails.CachedTokens != 80 {
		t.Errorf("cached_tokens = %d, want 80", usage.InputTokensDetails.CachedTokens)
	}
}

func TestParseOpenAIStreamEvent_ResponseIncomplete(t *testing.T) {
	data := []byte(`{"type":"response.incomplete","response":{"id":"resp_inc","model":"gpt-4o-2024-08-06","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`)

	eventType, model, respID, usage, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "response.incomplete" {
		t.Errorf("eventType = %q, want %q", eventType, "response.incomplete")
	}
	if model != "gpt-4o-2024-08-06" {
		t.Errorf("model = %q, want %q", model, "gpt-4o-2024-08-06")
	}
	if respID != "resp_inc" {
		t.Errorf("respID = %q, want %q", respID, "resp_inc")
	}
	if !hasUsage {
		t.Fatal("expected hasUsage = true for response.incomplete")
	}
	if usage.InputTokens != 100 {
		t.Errorf("input_tokens = %d, want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 20 {
		t.Errorf("output_tokens = %d, want 20", usage.OutputTokens)
	}
}

func TestParseOpenAIStreamEvent_ResponseFailed(t *testing.T) {
	data := []byte(`{"type":"response.failed","response":{"id":"resp_fail","model":"gpt-4o-2024-08-06","usage":{"input_tokens":60,"output_tokens":0,"total_tokens":60,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`)

	eventType, model, respID, usage, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "response.failed" {
		t.Errorf("eventType = %q, want %q", eventType, "response.failed")
	}
	if model != "gpt-4o-2024-08-06" {
		t.Errorf("model = %q, want %q", model, "gpt-4o-2024-08-06")
	}
	if respID != "resp_fail" {
		t.Errorf("respID = %q, want %q", respID, "resp_fail")
	}
	if !hasUsage {
		t.Fatal("expected hasUsage = true for response.failed")
	}
	if usage.InputTokens != 60 {
		t.Errorf("input_tokens = %d, want 60", usage.InputTokens)
	}
	if usage.OutputTokens != 0 {
		t.Errorf("output_tokens = %d, want 0", usage.OutputTokens)
	}
}

func TestParseOpenAIStreamEvent_InvalidJSON(t *testing.T) {
	data := []byte(`{not valid`)

	eventType, _, _, u, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "" {
		t.Errorf("eventType = %q, want empty", eventType)
	}
	if hasUsage {
		t.Error("expected hasUsage = false for invalid JSON")
	}
	_ = u // ensure zero-value usage returned
}

// verify type is usable from the responses package
var _ responses.ResponseUsage
