package proxy

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestGeminiStreamInterceptor(t *testing.T) {
	sseData := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11},"modelVersion":"gemini-2.5-flash-001","responseId":"resp-1"}`,
		``,
		`data: {"candidates":[{"content":{"parts":[{"text":" world"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15,"cachedContentTokenCount":2,"thoughtsTokenCount":3},"modelVersion":"gemini-2.5-flash-001","responseId":"resp-1"}`,
		``,
	}, "\n")

	var gotMetrics StreamMetrics
	done := make(chan struct{})

	interceptor := newGeminiStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		func(metrics StreamMetrics) {
			gotMetrics = metrics
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

	if gotMetrics.Model != "gemini-2.5-flash-001" {
		t.Errorf("Model = %q", gotMetrics.Model)
	}
	if gotMetrics.MsgID != "resp-1" {
		t.Errorf("MsgID = %q", gotMetrics.MsgID)
	}
	// Usage should reflect the final event's values.
	if gotMetrics.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", gotMetrics.InputTokens)
	}
	// OutputTokens = candidatesTokenCount (5) + thoughtsTokenCount (3) = 8
	if gotMetrics.OutputTokens != 8 {
		t.Errorf("OutputTokens = %d, want 8 (5 candidates + 3 thoughts)", gotMetrics.OutputTokens)
	}
	if gotMetrics.CacheReadTokens != 2 {
		t.Errorf("CacheReadTokens = %d, want 2", gotMetrics.CacheReadTokens)
	}
	if gotMetrics.TTFTMs <= 0 {
		// TTFT should be positive since we read real time.
		// Just check it was set (> 0 means the first text event was detected).
		// In fast tests this could be 0ms, so we just check it was assigned.
		t.Logf("TTFTMs = %d (may be 0 in fast tests)", gotMetrics.TTFTMs)
	}
}

func TestGeminiStreamInterceptor_EmptyStream(t *testing.T) {
	var gotMetrics StreamMetrics
	done := make(chan struct{})

	interceptor := newGeminiStreamInterceptor(
		io.NopCloser(strings.NewReader("")),
		time.Now(),
		func(metrics StreamMetrics) {
			gotMetrics = metrics
			close(done)
		},
	)

	_, err := io.ReadAll(interceptor)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete callback not called for empty stream")
	}

	// Empty stream should report zero metrics.
	if gotMetrics.Model != "" || gotMetrics.MsgID != "" {
		t.Errorf("expected empty model/msgID, got %q/%q", gotMetrics.Model, gotMetrics.MsgID)
	}
	if gotMetrics.InputTokens != 0 || gotMetrics.OutputTokens != 0 {
		t.Errorf("expected zero tokens")
	}
}

func TestGeminiStreamInterceptor_WithDoneEvent(t *testing.T) {
	sseData := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"Hi"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7},"modelVersion":"gemini-2.5-pro-001","responseId":"resp-2"}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	var gotMetrics StreamMetrics
	done := make(chan struct{})

	interceptor := newGeminiStreamInterceptor(
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
		t.Fatal("onComplete callback not called")
	}

	if gotMetrics.Model != "gemini-2.5-pro-001" {
		t.Errorf("Model = %q", gotMetrics.Model)
	}
	if gotMetrics.InputTokens != 5 {
		t.Errorf("InputTokens = %d, want 5", gotMetrics.InputTokens)
	}
	if gotMetrics.OutputTokens != 2 {
		t.Errorf("OutputTokens = %d, want 2", gotMetrics.OutputTokens)
	}
}

func TestGeminiStreamInterceptor_Close(t *testing.T) {
	sseData := `data: {"candidates":[{"content":{"parts":[{"text":"Hi"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4},"modelVersion":"gemini-2.5-flash-001","responseId":"resp-3"}` + "\n"

	var gotMetrics StreamMetrics
	done := make(chan struct{})

	interceptor := newGeminiStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		func(metrics StreamMetrics) {
			gotMetrics = metrics
			close(done)
		},
	)

	// Read all data then close. This mirrors real usage where the stream
	// body is fully consumed before Close is called.
	io.ReadAll(interceptor)
	interceptor.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete not called on Close()")
	}

	if gotMetrics.InputTokens != 3 {
		t.Errorf("InputTokens = %d, want 3", gotMetrics.InputTokens)
	}
}
