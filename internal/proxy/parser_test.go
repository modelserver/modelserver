package proxy

import (
	"testing"
)

func TestParseNonStreamingResponse(t *testing.T) {
	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"model": "claude-sonnet-4-20250514",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 10,
			"cache_read_input_tokens": 20
		}
	}`)

	model, msgID, usage, err := ParseNonStreamingResponse(body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q", model)
	}
	if msgID != "msg_123" {
		t.Errorf("msgID = %q", msgID)
	}
	if usage.InputTokens != 100 {
		t.Errorf("input_tokens = %d", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("output_tokens = %d", usage.OutputTokens)
	}
	if usage.CacheCreationInputTokens != 10 {
		t.Errorf("cache_creation = %d", usage.CacheCreationInputTokens)
	}
	if usage.CacheReadInputTokens != 20 {
		t.Errorf("cache_read = %d", usage.CacheReadInputTokens)
	}
}

func TestParseStreamEvent_MessageStart(t *testing.T) {
	data := []byte(`{"type":"message_start","message":{"id":"msg_456","model":"claude-sonnet-4-20250514","usage":{"input_tokens":200,"cache_creation_input_tokens":5,"cache_read_input_tokens":15}}}`)

	eventType, model, msgID, usage, hasUsage := ParseStreamEvent(data)
	if eventType != "message_start" {
		t.Errorf("eventType = %q", eventType)
	}
	if model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q", model)
	}
	if msgID != "msg_456" {
		t.Errorf("msgID = %q", msgID)
	}
	if !hasUsage {
		t.Error("expected hasUsage = true")
	}
	if usage.InputTokens != 200 {
		t.Errorf("input_tokens = %d", usage.InputTokens)
	}
}

func TestParseStreamEvent_MessageDelta(t *testing.T) {
	data := []byte(`{"type":"message_delta","usage":{"output_tokens":75}}`)

	eventType, _, _, usage, hasUsage := ParseStreamEvent(data)
	if eventType != "message_delta" {
		t.Errorf("eventType = %q", eventType)
	}
	if !hasUsage {
		t.Error("expected hasUsage = true")
	}
	if usage.OutputTokens != 75 {
		t.Errorf("output_tokens = %d", usage.OutputTokens)
	}
}

func TestParseStreamEvent_ContentBlockDelta(t *testing.T) {
	data := []byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}`)

	eventType, _, _, _, hasUsage := ParseStreamEvent(data)
	if eventType != "content_block_delta" {
		t.Errorf("eventType = %q", eventType)
	}
	if hasUsage {
		t.Error("expected hasUsage = false for content_block_delta")
	}
}
