package proxy

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestStreamInterceptor(t *testing.T) {
	sseData := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":100,"cache_creation_input_tokens":5,"cache_read_input_tokens":10}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":50}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	var gotModel, gotMsgID string
	var gotUsage anthropic.Usage
	var gotTTFT int64
	done := make(chan struct{})

	interceptor := newStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		nil,
		func(model, msgID string, usage anthropic.Usage, ttft int64) {
			gotModel = model
			gotMsgID = msgID
			gotUsage = usage
			gotTTFT = ttft
			close(done)
		},
	)

	// Read all data through the interceptor.
	output, err := io.ReadAll(interceptor)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	// Original data must pass through unchanged.
	if string(output) != sseData {
		t.Errorf("output differs from input")
	}

	// Wait for callback.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete callback not called")
	}

	if gotModel != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q", gotModel)
	}
	if gotMsgID != "msg_1" {
		t.Errorf("msgID = %q", gotMsgID)
	}
	if gotUsage.InputTokens != 100 {
		t.Errorf("input_tokens = %d", gotUsage.InputTokens)
	}
	if gotUsage.OutputTokens != 50 {
		t.Errorf("output_tokens = %d", gotUsage.OutputTokens)
	}
	if gotUsage.CacheCreationInputTokens != 5 {
		t.Errorf("cache_creation = %d", gotUsage.CacheCreationInputTokens)
	}
	if gotUsage.CacheReadInputTokens != 10 {
		t.Errorf("cache_read = %d", gotUsage.CacheReadInputTokens)
	}
	if gotTTFT < 0 {
		t.Errorf("ttft = %d, want >= 0", gotTTFT)
	}
}
