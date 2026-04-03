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

func TestStreamInterceptor_InputJSONDelta(t *testing.T) {
	// Simulate fine-grained tool streaming with input_json_delta events.
	sseData := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_2","model":"claude-opus-4-20250514","usage":{"input_tokens":200,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"make_file","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"filename\": \"poem.txt\""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":", \"lines_of_text\": [\"Roses are red"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\", \"Violets are blue\"]}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":75}}`,
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

	if gotModel != "claude-opus-4-20250514" {
		t.Errorf("model = %q, want claude-opus-4-20250514", gotModel)
	}
	if gotMsgID != "msg_2" {
		t.Errorf("msgID = %q, want msg_2", gotMsgID)
	}
	if gotUsage.InputTokens != 200 {
		t.Errorf("input_tokens = %d, want 200", gotUsage.InputTokens)
	}
	if gotUsage.OutputTokens != 75 {
		t.Errorf("output_tokens = %d, want 75", gotUsage.OutputTokens)
	}
	// TTFT should be measured from the first content_block_delta (input_json_delta).
	if gotTTFT < 0 {
		t.Errorf("ttft = %d, want >= 0", gotTTFT)
	}
}
