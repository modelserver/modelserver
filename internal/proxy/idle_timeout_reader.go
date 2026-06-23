package proxy

import (
	"errors"
	"io"
	"sync"
	"time"
)

// ErrStreamIdleTimeout is returned by idleTimeoutReader.Read when the inner
// source has been silent for longer than the configured idle window. Wrapped
// rather than naked so callers can use errors.Is to distinguish a real
// upstream stall (this) from a clean upstream EOF (io.EOF) or context
// cancellation. Surfaced to the streaming client as a final SSE error chunk
// (or a connection close on platforms that don't render it), so Claude Code
// and similar clients stop showing a generic "partial response received"
// against an idle gateway.
var ErrStreamIdleTimeout = errors.New("upstream stream idle timeout")

// idleTimeoutReader wraps an io.ReadCloser with a per-Read idle watchdog.
// Every successful Read (n > 0) resets the timer; if the timer fires before
// the next Read returns data, the inner body is closed and subsequent Reads
// return ErrStreamIdleTimeout.
//
// Unlike a request-scoped context.WithTimeout, this enforces "no upstream
// activity for D seconds", not "total stream duration ≤ D". Long Opus
// reasoning, long 1M-context tool turns, and 30-minute streamed writes all
// remain valid as long as some bytes arrive within each window — only true
// upstream silence triggers it.
//
// A timeout <= 0 disables the watchdog and the reader is a thin pass-through.
type idleTimeoutReader struct {
	inner   io.ReadCloser
	timeout time.Duration

	mu       sync.Mutex
	timer    *time.Timer
	tripped  bool
	closeErr error
}

func newIdleTimeoutReader(inner io.ReadCloser, timeout time.Duration) *idleTimeoutReader {
	r := &idleTimeoutReader{
		inner:   inner,
		timeout: timeout,
	}
	if timeout > 0 {
		r.timer = time.AfterFunc(timeout, r.onTimeout)
	}
	return r
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)

	r.mu.Lock()
	tripped := r.tripped
	r.mu.Unlock()
	if tripped {
		// Inner Read returned because we closed it; surface the watchdog
		// error regardless of what the inner reported (typically a wrapped
		// "use of closed network connection").
		return n, ErrStreamIdleTimeout
	}

	if n > 0 && r.timer != nil {
		r.timer.Reset(r.timeout)
	}
	return n, err
}

func (r *idleTimeoutReader) Close() error {
	if r.timer != nil {
		r.timer.Stop()
	}
	return r.inner.Close()
}

// onTimeout fires on the timer goroutine when the idle window elapses
// without a successful Read. We mark the reader as tripped and close the
// inner source so any blocked Read unwinds; the next Read returns
// ErrStreamIdleTimeout.
func (r *idleTimeoutReader) onTimeout() {
	r.mu.Lock()
	if r.tripped {
		r.mu.Unlock()
		return
	}
	r.tripped = true
	r.mu.Unlock()
	r.closeErr = r.inner.Close()
}
