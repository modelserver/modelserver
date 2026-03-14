package proxy

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestOpenAIStreamInterceptor(t *testing.T) {
	sseData := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_001","model":"gpt-5.2","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":" world"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_001","model":"gpt-5.2","usage":{"input_tokens":120,"output_tokens":50,"total_tokens":170,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		``,
	}, "\n")

	var gotModel, gotRespID string
	var gotInput, gotOutput, gotCacheRead, gotTTFT int64
	done := make(chan struct{})

	interceptor := newOpenAIStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		"",
		func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64) {
			gotModel = model
			gotRespID = respID
			gotInput = inputTokens
			gotOutput = outputTokens
			gotCacheRead = cacheReadTokens
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

	if gotModel != "gpt-5.2" {
		t.Errorf("model = %q, want %q", gotModel, "gpt-5.2")
	}
	if gotRespID != "resp_001" {
		t.Errorf("respID = %q, want %q", gotRespID, "resp_001")
	}
	// Normalized: 120 - 80 = 40
	if gotInput != 40 {
		t.Errorf("inputTokens = %d, want 40", gotInput)
	}
	if gotOutput != 50 {
		t.Errorf("outputTokens = %d, want 50", gotOutput)
	}
	if gotCacheRead != 80 {
		t.Errorf("cacheReadTokens = %d, want 80", gotCacheRead)
	}
	if gotTTFT < 0 {
		t.Errorf("ttft = %d, want >= 0", gotTTFT)
	}
}

func TestOpenAIStreamInterceptor_Incomplete(t *testing.T) {
	sseData := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_002","model":"gpt-5.2","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		``,
		`event: response.incomplete`,
		`data: {"type":"response.incomplete","response":{"id":"resp_002","model":"gpt-5.2","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		``,
	}, "\n")

	var gotInput, gotOutput, gotCacheRead int64
	done := make(chan struct{})

	interceptor := newOpenAIStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		"",
		func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64) {
			gotInput = inputTokens
			gotOutput = outputTokens
			gotCacheRead = cacheReadTokens
			close(done)
		},
	)

	if _, err := io.ReadAll(interceptor); err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete callback not called")
	}

	// Normalized: 100 - 0 = 100
	if gotInput != 100 {
		t.Errorf("inputTokens = %d, want 100", gotInput)
	}
	if gotOutput != 20 {
		t.Errorf("outputTokens = %d, want 20", gotOutput)
	}
	if gotCacheRead != 0 {
		t.Errorf("cacheReadTokens = %d, want 0", gotCacheRead)
	}
}

func TestOpenAIStreamInterceptor_ModelFallback(t *testing.T) {
	sseData := strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_003","model":"","usage":{"input_tokens":50,"output_tokens":10,"total_tokens":60,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		``,
	}, "\n")

	var gotModel string
	done := make(chan struct{})

	interceptor := newOpenAIStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		"gpt-5.2-fallback",
		func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64) {
			gotModel = model
			close(done)
		},
	)

	if _, err := io.ReadAll(interceptor); err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete callback not called")
	}

	if gotModel != "gpt-5.2-fallback" {
		t.Errorf("model = %q, want %q", gotModel, "gpt-5.2-fallback")
	}
}
