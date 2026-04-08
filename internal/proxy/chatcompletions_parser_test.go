package proxy

import "testing"

func TestParseChatCompletionsResponse(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl-abc123",
		"object": "chat.completion",
		"model": "google/gemini-2.5-flash",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hello!"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`)

	metrics, err := ParseChatCompletionsResponse(body)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if metrics.Model != "google/gemini-2.5-flash" {
		t.Errorf("Model = %q", metrics.Model)
	}
	if metrics.MsgID != "chatcmpl-abc123" {
		t.Errorf("MsgID = %q", metrics.MsgID)
	}
	if metrics.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", metrics.InputTokens)
	}
	if metrics.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", metrics.OutputTokens)
	}
}

func TestParseChatCompletionsResponse_InvalidJSON(t *testing.T) {
	_, err := ParseChatCompletionsResponse([]byte(`not json`))
	if err == nil {
		t.Error("expected error")
	}
}

func TestParseChatCompletionsStreamEvent_WithContent(t *testing.T) {
	data := []byte(`{"id":"chatcmpl-x","object":"chat.completion.chunk","model":"google/gemini-2.5-flash","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`)

	model, respID, _, hasUsage, hasContent := ParseChatCompletionsStreamEvent(data)
	if model != "google/gemini-2.5-flash" {
		t.Errorf("model = %q", model)
	}
	if respID != "chatcmpl-x" {
		t.Errorf("respID = %q", respID)
	}
	if hasUsage {
		t.Error("expected hasUsage=false")
	}
	if !hasContent {
		t.Error("expected hasContent=true")
	}
}

func TestParseChatCompletionsStreamEvent_WithUsage(t *testing.T) {
	data := []byte(`{"id":"chatcmpl-x","model":"google/gemini-2.5-flash","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`)

	_, _, usage, hasUsage, hasContent := ParseChatCompletionsStreamEvent(data)
	if !hasUsage {
		t.Fatal("expected hasUsage=true")
	}
	if usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d", usage.PromptTokens)
	}
	if usage.CompletionTokens != 20 {
		t.Errorf("CompletionTokens = %d", usage.CompletionTokens)
	}
	if hasContent {
		t.Error("expected hasContent=false for usage-only event")
	}
}

func TestParseChatCompletionsStreamEvent_InvalidJSON(t *testing.T) {
	model, respID, _, hasUsage, hasContent := ParseChatCompletionsStreamEvent([]byte(`invalid`))
	if model != "" || respID != "" || hasUsage || hasContent {
		t.Error("expected empty results")
	}
}
