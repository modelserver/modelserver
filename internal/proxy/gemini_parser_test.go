package proxy

import "testing"

func TestParseGeminiResponse(t *testing.T) {
	body := []byte(`{
		"candidates": [
			{
				"content": {
					"parts": [{"text": "Hello!"}],
					"role": "model"
				},
				"finishReason": "STOP"
			}
		],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 5,
			"totalTokenCount": 15,
			"cachedContentTokenCount": 2,
			"thoughtsTokenCount": 3
		},
		"modelVersion": "gemini-2.5-flash-001",
		"responseId": "resp-abc123"
	}`)

	metrics, err := ParseGeminiResponse(body)
	if err != nil {
		t.Fatalf("ParseGeminiResponse() error = %v", err)
	}

	if metrics.Model != "gemini-2.5-flash-001" {
		t.Errorf("Model = %q, want %q", metrics.Model, "gemini-2.5-flash-001")
	}
	if metrics.MsgID != "resp-abc123" {
		t.Errorf("MsgID = %q, want %q", metrics.MsgID, "resp-abc123")
	}
	if metrics.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", metrics.InputTokens)
	}
	// OutputTokens = candidatesTokenCount (5) + thoughtsTokenCount (3) = 8
	if metrics.OutputTokens != 8 {
		t.Errorf("OutputTokens = %d, want 8 (5 candidates + 3 thoughts)", metrics.OutputTokens)
	}
	if metrics.CacheReadTokens != 2 {
		t.Errorf("CacheReadTokens = %d, want 2", metrics.CacheReadTokens)
	}
	if metrics.CacheCreationTokens != 0 {
		t.Errorf("CacheCreationTokens = %d, want 0", metrics.CacheCreationTokens)
	}
}

func TestParseGeminiResponse_NoUsage(t *testing.T) {
	body := []byte(`{
		"candidates": [{"content": {"parts": [{"text": "Hi"}], "role": "model"}}],
		"modelVersion": "gemini-2.5-pro-001"
	}`)

	metrics, err := ParseGeminiResponse(body)
	if err != nil {
		t.Fatalf("ParseGeminiResponse() error = %v", err)
	}
	if metrics.Model != "gemini-2.5-pro-001" {
		t.Errorf("Model = %q", metrics.Model)
	}
	if metrics.InputTokens != 0 || metrics.OutputTokens != 0 {
		t.Errorf("expected zero tokens")
	}
}

func TestParseGeminiResponse_InvalidJSON(t *testing.T) {
	_, err := ParseGeminiResponse([]byte(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseGeminiStreamEvent(t *testing.T) {
	data := []byte(`{
		"candidates": [
			{
				"content": {
					"parts": [{"text": "Hello"}],
					"role": "model"
				}
			}
		],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 1,
			"totalTokenCount": 11,
			"cachedContentTokenCount": 0,
			"thoughtsTokenCount": 0
		},
		"modelVersion": "gemini-2.5-flash-001",
		"responseId": "resp-xyz"
	}`)

	model, respID, usage, hasText := ParseGeminiStreamEvent(data)

	if model != "gemini-2.5-flash-001" {
		t.Errorf("model = %q", model)
	}
	if respID != "resp-xyz" {
		t.Errorf("respID = %q", respID)
	}
	if usage.PromptTokenCount != 10 {
		t.Errorf("PromptTokenCount = %d", usage.PromptTokenCount)
	}
	if usage.CandidatesTokenCount != 1 {
		t.Errorf("CandidatesTokenCount = %d", usage.CandidatesTokenCount)
	}
	if !hasText {
		t.Error("expected hasText=true for event with text content")
	}
}

func TestParseGeminiStreamEvent_NoText(t *testing.T) {
	// Event with function call, no text content
	data := []byte(`{
		"candidates": [
			{
				"content": {
					"parts": [{"functionCall": {"name": "search", "args": {}}}],
					"role": "model"
				}
			}
		],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 1, "totalTokenCount": 6},
		"modelVersion": "gemini-2.5-flash-001"
	}`)

	_, _, _, hasText := ParseGeminiStreamEvent(data)
	if hasText {
		t.Error("expected hasText=false for event without text")
	}
}

func TestParseGeminiStreamEvent_InvalidJSON(t *testing.T) {
	model, respID, _, hasText := ParseGeminiStreamEvent([]byte(`invalid`))
	if model != "" || respID != "" || hasText {
		t.Error("expected empty results for invalid JSON")
	}
}
