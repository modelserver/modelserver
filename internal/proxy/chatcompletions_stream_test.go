package proxy

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestChatCompletionsStreamInterceptor(t *testing.T) {
	sseData := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"google/gemini-2.5-flash","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"google/gemini-2.5-flash","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"google/gemini-2.5-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	var gotMetrics StreamMetrics
	done := make(chan struct{})

	interceptor := newChatCompletionsStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		func(metrics StreamMetrics) {
			gotMetrics = metrics
			close(done)
		},
	)

	output, err := io.ReadAll(interceptor)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(output) != sseData {
		t.Errorf("output differs from input")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete not called")
	}

	if gotMetrics.Model != "google/gemini-2.5-flash" {
		t.Errorf("Model = %q", gotMetrics.Model)
	}
	if gotMetrics.MsgID != "chatcmpl-1" {
		t.Errorf("MsgID = %q", gotMetrics.MsgID)
	}
	if gotMetrics.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", gotMetrics.InputTokens)
	}
	if gotMetrics.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, want 5", gotMetrics.OutputTokens)
	}
}

func TestChatCompletionsStreamInterceptor_EmptyStream(t *testing.T) {
	var gotMetrics StreamMetrics
	done := make(chan struct{})

	interceptor := newChatCompletionsStreamInterceptor(
		io.NopCloser(strings.NewReader("")),
		time.Now(),
		func(metrics StreamMetrics) {
			gotMetrics = metrics
			close(done)
		},
	)

	io.ReadAll(interceptor)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete not called")
	}

	if gotMetrics.Model != "" || gotMetrics.InputTokens != 0 {
		t.Errorf("expected zero metrics for empty stream")
	}
}

func TestChatCompletionsStreamInterceptor_NoUsage(t *testing.T) {
	// Stream without usage event (provider doesn't include it).
	sseData := strings.Join([]string{
		`data: {"id":"chatcmpl-2","model":"gpt-4","choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	var gotMetrics StreamMetrics
	done := make(chan struct{})

	interceptor := newChatCompletionsStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		func(metrics StreamMetrics) {
			gotMetrics = metrics
			close(done)
		},
	)

	io.ReadAll(interceptor)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete not called")
	}

	if gotMetrics.Model != "gpt-4" {
		t.Errorf("Model = %q", gotMetrics.Model)
	}
	// No usage reported — tokens should be zero.
	if gotMetrics.InputTokens != 0 || gotMetrics.OutputTokens != 0 {
		t.Errorf("expected zero tokens when no usage event")
	}
}
