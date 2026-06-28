package proxy

import (
	"bytes"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRW is a thread-safe http.ResponseWriter + http.Flusher for tests.
// Writes append to buf under mu; Flush count is exposed atomically.
type fakeRW struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	hdr    http.Header
	status int
	flush  int64
}

func newFakeRW() *fakeRW { return &fakeRW{hdr: http.Header{}} }

func (f *fakeRW) Header() http.Header  { return f.hdr }
func (f *fakeRW) WriteHeader(s int)    { f.status = s }
func (f *fakeRW) Flush()               { atomic.AddInt64(&f.flush, 1) }
func (f *fakeRW) FlushCount() int64    { return atomic.LoadInt64(&f.flush) }
func (f *fakeRW) Snapshot() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]byte, f.buf.Len())
	copy(out, f.buf.Bytes())
	return out
}

func (f *fakeRW) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(p)
}

// eventually polls fn every 5ms until it returns true or the deadline
// elapses. Returns the elapsed time and whether the condition held.
func eventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

// TestKeepaliveWriter_DisabledIsPassthrough verifies that interval<=0
// short-circuits the heartbeat: data still reaches the underlying RW and
// no goroutine / timer is spawned (no panics on Close in any case).
func TestKeepaliveWriter_DisabledIsPassthrough(t *testing.T) {
	rw := newFakeRW()
	k := newKeepaliveWriter(rw, 0)

	if _, err := k.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := string(rw.Snapshot()); got != "hello" {
		t.Errorf("buf = %q, want %q", got, "hello")
	}
	if bytes.Contains(rw.Snapshot(), []byte(":\n\n")) {
		t.Errorf("heartbeat written despite interval=0")
	}
}

// TestKeepaliveWriter_EmitsCommentOnSilence is the core regression: with
// no Writes in flight, heartbeats fire on schedule with the exact :\n\n
// payload.
func TestKeepaliveWriter_EmitsCommentOnSilence(t *testing.T) {
	rw := newFakeRW()
	k := newKeepaliveWriter(rw, 20*time.Millisecond)
	defer k.Close()

	eventually(t, 1*time.Second, func() bool {
		return bytes.Count(rw.Snapshot(), []byte(":\n\n")) >= 3
	})
}

// TestKeepaliveWriter_ResetsTimerOnWrite proves the timer anchor is the
// last successful Write: a steady stream of data suppresses heartbeats
// entirely.
func TestKeepaliveWriter_ResetsTimerOnWrite(t *testing.T) {
	rw := newFakeRW()
	k := newKeepaliveWriter(rw, 30*time.Millisecond)
	defer k.Close()

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := k.Write([]byte("x")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if c := bytes.Count(rw.Snapshot(), []byte(":\n\n")); c != 0 {
		t.Errorf("heartbeat fired %d times despite continuous writes", c)
	}
}

// TestKeepaliveWriter_NoInterleaveUnderConcurrency stresses the mutex
// contract: 100 full SSE events written by one goroutine while a 1ms
// heartbeat races, and the output must still parse as exactly 100 events
// with no :\n\n inside any event: ... \n\n span.
func TestKeepaliveWriter_NoInterleaveUnderConcurrency(t *testing.T) {
	rw := newFakeRW()
	k := newKeepaliveWriter(rw, 1*time.Millisecond)

	const events = 100
	const payload = "event: x\ndata: {\"i\":0}\n\n"
	for i := 0; i < events; i++ {
		if _, err := k.Write([]byte(payload)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	out := rw.Snapshot()
	// Every event must remain intact. Heartbeats are ":\n\n" and must
	// only appear between events (or not at all if writes were faster
	// than heartbeats), never inside an "event: ... \n\n" block.
	// Strategy: split on the SSE event terminator "\n\n", filter out
	// pure ":" frames (heartbeats), and verify exactly `events` data
	// frames remain, each prefixed by "event: x\ndata:".
	frames := bytes.Split(out, []byte("\n\n"))
	dataFrames := 0
	for _, f := range frames {
		if len(f) == 0 {
			continue
		}
		if bytes.Equal(f, []byte(":")) {
			continue
		}
		if !bytes.HasPrefix(f, []byte("event: x\ndata:")) {
			t.Fatalf("malformed frame: %q", f)
		}
		dataFrames++
	}
	if dataFrames != events {
		t.Errorf("parsed %d data frames, want %d", dataFrames, events)
	}
}

// TestKeepaliveWriter_CloseStopsHeartbeat verifies the timer goroutine
// shuts down on Close — the underlying buffer stops growing.
func TestKeepaliveWriter_CloseStopsHeartbeat(t *testing.T) {
	rw := newFakeRW()
	k := newKeepaliveWriter(rw, 10*time.Millisecond)

	// Let at least one heartbeat fire.
	eventually(t, 500*time.Millisecond, func() bool {
		return bytes.Contains(rw.Snapshot(), []byte(":\n\n"))
	})

	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	sizeAtClose := len(rw.Snapshot())
	time.Sleep(50 * time.Millisecond)
	if got := len(rw.Snapshot()); got != sizeAtClose {
		t.Errorf("buffer grew from %d to %d after Close", sizeAtClose, got)
	}
}

// TestKeepaliveWriter_FlusherCalledEachHeartbeat verifies every byte
// emission (data or heartbeat) is followed by Flush so client-side SSE
// parsers see the bytes promptly.
func TestKeepaliveWriter_FlusherCalledEachHeartbeat(t *testing.T) {
	rw := newFakeRW()
	k := newKeepaliveWriter(rw, 15*time.Millisecond)
	defer k.Close()

	if _, err := k.Write([]byte("hi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	eventually(t, 500*time.Millisecond, func() bool {
		// At least one data write + one heartbeat flushed.
		return rw.FlushCount() >= 2
	})
}
