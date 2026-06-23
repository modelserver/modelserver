package proxy

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestIdleTimeoutReader_PassThroughWithData verifies the reader transparently
// forwards bytes when the source produces data within the idle window. The
// timer should reset on each non-zero Read.
func TestIdleTimeoutReader_PassThroughWithData(t *testing.T) {
	inner := io.NopCloser(strings.NewReader("chunk-1 chunk-2 chunk-3"))
	r := newIdleTimeoutReader(inner, 50*time.Millisecond)

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "chunk-1 chunk-2 chunk-3" {
		t.Errorf("got %q", string(got))
	}
}

// TestIdleTimeoutReader_FiresOnSilence is the core regression: when the
// upstream goes silent for longer than the idle window, the reader closes
// the inner body and surfaces ErrStreamIdleTimeout instead of waiting
// forever (and instead of returning silently — clients need to see an
// error, not a clean EOF on a half-sent SSE stream).
func TestIdleTimeoutReader_FiresOnSilence(t *testing.T) {
	inner := &blockingReadCloser{block: make(chan struct{})}
	r := newIdleTimeoutReader(inner, 20*time.Millisecond)

	buf := make([]byte, 16)
	_, err := r.Read(buf)
	if err == nil {
		t.Fatal("Read returned nil error after idle window")
	}
	if !errors.Is(err, ErrStreamIdleTimeout) {
		t.Errorf("Read err = %v, want ErrStreamIdleTimeout", err)
	}
	if !inner.closed {
		t.Errorf("inner body was not closed on timeout")
	}
}

// TestIdleTimeoutReader_ResetsAfterDataBeforeSilence proves the timer
// actually restarts on each non-zero Read — data flows, then a long silence
// triggers timeout. Without reset, this test would also fire after the
// first interval whether or not data arrived in between.
func TestIdleTimeoutReader_ResetsAfterDataBeforeSilence(t *testing.T) {
	src := &scriptedReader{
		// Two quick chunks well within the 30ms window, then a long block
		// that exceeds it. Total elapsed before timeout > 30ms, proving the
		// timer is per-gap, not absolute.
		steps: []scriptedStep{
			{after: 5 * time.Millisecond, data: []byte("first")},
			{after: 5 * time.Millisecond, data: []byte("second")},
			{after: 100 * time.Millisecond, data: nil}, // exceeds idle
		},
	}
	r := newIdleTimeoutReader(src, 30*time.Millisecond)

	got, err := io.ReadAll(r)
	if !errors.Is(err, ErrStreamIdleTimeout) {
		t.Fatalf("ReadAll err = %v, want ErrStreamIdleTimeout", err)
	}
	if string(got) != "firstsecond" {
		t.Errorf("got %q, want %q", string(got), "firstsecond")
	}
}

// TestIdleTimeoutReader_ZeroTimeoutIsNoop documents the contract: a
// non-positive timeout means "no watchdog" — the reader becomes a thin
// pass-through. This lets callers disable the feature via config without a
// separate code path.
func TestIdleTimeoutReader_ZeroTimeoutIsNoop(t *testing.T) {
	inner := io.NopCloser(strings.NewReader("payload"))
	r := newIdleTimeoutReader(inner, 0)
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("got %q", string(got))
	}
}

// blockingReadCloser blocks indefinitely on Read until Close is called.
type blockingReadCloser struct {
	block  chan struct{}
	closed bool
}

func (b *blockingReadCloser) Read(p []byte) (int, error) {
	<-b.block
	return 0, io.EOF
}

func (b *blockingReadCloser) Close() error {
	if !b.closed {
		b.closed = true
		close(b.block)
	}
	return nil
}

// scriptedReader walks a fixed sequence of (delay, payload) steps. When
// payload is nil it just sleeps then returns EOF — used to model a stalled
// upstream.
type scriptedReader struct {
	steps  []scriptedStep
	pos    int
	closed bool
}

type scriptedStep struct {
	after time.Duration
	data  []byte
}

func (s *scriptedReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.steps) {
		return 0, io.EOF
	}
	step := s.steps[s.pos]
	s.pos++
	time.Sleep(step.after)
	if step.data == nil {
		return 0, io.EOF
	}
	n := copy(p, step.data)
	return n, nil
}

func (s *scriptedReader) Close() error {
	s.closed = true
	return nil
}
