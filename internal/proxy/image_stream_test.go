package proxy

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestImageStreamInterceptor_OversizeEventDoesNotBufferTrailingBytes(t *testing.T) {
	// One oversize event followed by a large trailing payload. Before the fix,
	// lineBuf kept growing after parseOff fired; the trailing bytes here would
	// all accumulate in memory.
	bigData := strings.Repeat("x", maxImageStreamEventBytes+1)
	trailing := strings.Repeat("y", 4<<20) // 4 MiB
	stream := "data: " + bigData + "\n\n" + trailing

	rc := newImageStreamInterceptor(io.NopCloser(strings.NewReader(stream)), time.Now(), nil)
	si := rc.(*imageStreamInterceptor)
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !si.parseOff {
		t.Fatal("expected parseOff after oversize event")
	}
	if si.lineBuf.Len() != 0 {
		t.Fatalf("lineBuf retained %d bytes after parseOff; want 0", si.lineBuf.Len())
	}
}

func TestImageStreamInterceptor_Golden(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"image_generation.partial_image"}`,
		"",
		`data: {"type":"image_generation.completed","usage":{"input_tokens":10,"output_tokens":50,"total_tokens":60}}`,
		"",
	}, "\n")
	var gotUsage ImageTokenUsage
	var gotPresent bool
	var gotTTFT int64
	rc := newImageStreamInterceptor(io.NopCloser(strings.NewReader(stream)), time.Now().Add(-time.Second), func(u ImageTokenUsage, present bool, ttft int64) {
		gotUsage = u
		gotPresent = present
		gotTTFT = ttft
	})
	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(out) != stream {
		t.Fatalf("stream bytes changed")
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !gotPresent {
		t.Fatal("usagePresent=false, want true")
	}
	if gotUsage.ImageOutputTokens != 50 {
		t.Fatalf("ImageOutputTokens = %d, want 50", gotUsage.ImageOutputTokens)
	}
	if gotTTFT <= 0 {
		t.Fatalf("TTFT = %d, want > 0", gotTTFT)
	}
}

func TestImageStreamInterceptor_Truncated(t *testing.T) {
	stream := "data: {\"type\":\"image_edit.partial_image\"}\n\n"
	var called int
	var gotPresent bool
	rc := newImageStreamInterceptor(io.NopCloser(strings.NewReader(stream)), time.Now(), func(_ ImageTokenUsage, present bool, _ int64) {
		called++
		gotPresent = present
	})
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	_ = rc.Close()
	if called != 1 {
		t.Fatalf("callback called %d times, want 1", called)
	}
	if gotPresent {
		t.Fatal("usagePresent=true, want false")
	}
}
