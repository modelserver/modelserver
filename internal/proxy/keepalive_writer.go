package proxy

import (
	"net/http"
	"sync"
	"time"
)

// sseHeartbeat is the exact 3-byte SSE comment line we inject when the
// upstream is silent. SSE clients (Anthropic SDK, OpenAI SDK, browser
// EventSource) short-circuit on lines starting with ':' and produce no
// event object, so the heartbeat is invisible to consumers while still
// resetting any byte-level stall timer in the client or middleboxes.
const sseHeartbeat = ":\n\n"

// keepaliveWriter wraps an http.ResponseWriter + http.Flusher and injects
// sseHeartbeat when no data has been written for `interval`. Heartbeat and
// data writes share a single mutex, so a heartbeat can never land in the
// middle of a partially-written SSE event.
//
// A timer (re-armed on every successful Write) fires on a background
// goroutine. On fire, it acquires the mutex, writes the heartbeat,
// Flushes, and re-arms itself. Close() stops the timer.
//
// interval <= 0 disables the keepalive: the writer is a thin pass-through
// with no timer goroutine spawned.
type keepaliveWriter struct {
	w        http.ResponseWriter
	flusher  http.Flusher
	interval time.Duration

	mu     sync.Mutex
	timer  *time.Timer
	closed bool
}

// newKeepaliveWriter returns a keepaliveWriter wrapping w. If w also
// implements http.Flusher, every write (data or heartbeat) is followed by
// a Flush. A non-positive interval disables the heartbeat entirely.
func newKeepaliveWriter(w http.ResponseWriter, interval time.Duration) *keepaliveWriter {
	k := &keepaliveWriter{
		w:        w,
		interval: interval,
	}
	if f, ok := w.(http.Flusher); ok {
		k.flusher = f
	}
	if interval > 0 {
		k.timer = time.AfterFunc(interval, k.onHeartbeat)
	}
	return k
}

// Write forwards p to the underlying ResponseWriter under the mutex and
// resets the heartbeat timer. Flushes after the write so SSE clients see
// the bytes immediately.
func (k *keepaliveWriter) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	n, err := k.w.Write(p)
	if k.flusher != nil {
		k.flusher.Flush()
	}
	if k.timer != nil && !k.closed {
		k.timer.Reset(k.interval)
	}
	return n, err
}

// Flush forwards to the underlying Flusher (if any). Provided so
// keepaliveWriter satisfies http.Flusher for callers that type-assert.
func (k *keepaliveWriter) Flush() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.flusher != nil {
		k.flusher.Flush()
	}
}

// Close stops the heartbeat timer. Subsequent heartbeat fires are no-ops.
// Safe to call multiple times.
func (k *keepaliveWriter) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.closed = true
	if k.timer != nil {
		k.timer.Stop()
	}
	return nil
}

// onHeartbeat runs on the timer goroutine when the interval elapses with
// no Write. Writes sseHeartbeat under the mutex and re-arms the timer.
// Skips the write if Close has already run.
func (k *keepaliveWriter) onHeartbeat() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return
	}
	_, _ = k.w.Write([]byte(sseHeartbeat))
	if k.flusher != nil {
		k.flusher.Flush()
	}
	k.timer.Reset(k.interval)
}
