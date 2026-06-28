# SSE Keepalive Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Status (2026-06-28, post-implementation):** Branch `feat/sse-keepalive` shipped. Two deviations from the text below — both recorded in the spec's "Implementation updates" section: (1) **image streaming path** was modified after final review flagged the same phantom-success bug there (Task 4 "do NOT add `interruptErrPtr` to the image stream path" is superseded; the closure now passes `*interruptErrPtr` to `completeImageStreamingRequest` too). (2) **Task 1 Step 1.1 test #4** (`TestKeepaliveWriter_NoInterleaveUnderConcurrency`) was rewritten after a re-audit found the original tight-loop shape fired 0 heartbeats in 5/5 trials — the test passed while exercising nothing. The shipped version uses 4 concurrent writer goroutines with brief sleeps and additionally asserts heartbeats actually fire.

**Goal:** Inject `:\n\n` SSE comment heartbeats on the downstream socket every 15s of upstream silence to mask client-side stall detection and middlebox idle ceilings, and fix the bug where mid-stream-interrupted requests are recorded as `status='success'`.

**Architecture:** New `keepaliveWriter` wraps `http.ResponseWriter` between `copyWithFlush` and the real RW. A timer re-armed on every `Write` fires a heartbeat under the same mutex, so heartbeats can never interleave with SSE event bytes. `StreamMetrics` grows one field (`InterruptErr error`) plumbed from `copyWithFlush`'s error through the existing interceptor `Close()` → `finish()` → `onComplete` callback path; `completeStreamingRequest` flips `Status` / `ErrorMessage` when set.

**Tech Stack:** Go 1.x, standard `net/http` + `sync` + `time`, project-internal `slog`. No new dependencies.

## Global Constraints

- All new code under `internal/proxy/`, package `proxy`.
- Heartbeat payload **must** be the 3-byte string `:\n\n` — no other variant.
- Heartbeat **must** share a single `sync.Mutex` with data `Write()` so SSE events are never torn.
- Default keepalive interval: **15s**. Configurable via `server.sse_keepalive_interval`. `<= 0` disables (pass-through).
- Reuse `types.RequestStatusError` for interrupted streams. Do **not** add a new status value.
- Do **not** estimate `output_tokens` on interrupt — keep whatever the upstream parser observed.
- Do **not** emit an SSE `event: error` frame on interrupt — by the time `copyErr` fires the socket is gone.
- Follow `idle_timeout_reader.go` / `idle_timeout_reader_test.go` patterns for file shape and test style.

---

### Task 1: `keepaliveWriter` type and tests

**Files:**
- Create: `internal/proxy/keepalive_writer.go`
- Create: `internal/proxy/keepalive_writer_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `func newKeepaliveWriter(w http.ResponseWriter, interval time.Duration) *keepaliveWriter`
  - `(k *keepaliveWriter) Write(p []byte) (int, error)`
  - `(k *keepaliveWriter) Flush()` — implements `http.Flusher`
  - `(k *keepaliveWriter) Close() error`
  - Constant `sseHeartbeat = ":\n\n"`

- [ ] **Step 1.1: Write the failing tests**

Create `internal/proxy/keepalive_writer_test.go` with the full test file below. The file pattern mirrors `idle_timeout_reader_test.go`.

```go
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
```

- [ ] **Step 1.2: Run the tests to verify they fail**

Run: `go test ./internal/proxy/ -run TestKeepaliveWriter -v`
Expected: compilation error — `undefined: newKeepaliveWriter`.

- [ ] **Step 1.3: Write the implementation**

Create `internal/proxy/keepalive_writer.go`:

```go
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
```

- [ ] **Step 1.4: Run the tests to verify they pass**

Run: `go test ./internal/proxy/ -run TestKeepaliveWriter -v`
Expected: all 6 sub-tests PASS.

- [ ] **Step 1.5: Run race detector**

Run: `go test -race ./internal/proxy/ -run TestKeepaliveWriter -count=3`
Expected: no race detected across 3 iterations.

- [ ] **Step 1.6: Commit**

```bash
git add internal/proxy/keepalive_writer.go internal/proxy/keepalive_writer_test.go
git commit -m "feat(proxy): add keepaliveWriter for SSE heartbeat injection

Wraps http.ResponseWriter with a timer-driven :\n\n injector. Single
mutex ensures heartbeats never tear an SSE event mid-frame. interval<=0
is a thin pass-through.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Config field + defaults

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `cfg.Server.SSEKeepaliveInterval time.Duration`

- [ ] **Step 2.1: Write the failing test**

Open `internal/config/config_test.go` and find the existing `StreamIdleTimeout` assertions (around lines 28, 68, 122 — see `grep -n StreamIdleTimeout internal/config/config_test.go`). Add a sibling assertion in the **defaults** test (the one that loads with no overrides):

```go
	if cfg.Server.SSEKeepaliveInterval != 15*time.Second {
		t.Errorf("Server.SSEKeepaliveInterval = %v, want 15s", cfg.Server.SSEKeepaliveInterval)
	}
```

And a sibling in the **overrides** test (the one that loads YAML with `stream_idle_timeout: 90s`). Add `sse_keepalive_interval: 7s` to that YAML literal and assert:

```go
	if cfg.Server.SSEKeepaliveInterval != 7*time.Second {
		t.Errorf("Server.SSEKeepaliveInterval = %v, want 7s", cfg.Server.SSEKeepaliveInterval)
	}
```

- [ ] **Step 2.2: Run tests to verify they fail**

Run: `go test ./internal/config/ -v`
Expected: compilation error — `cfg.Server.SSEKeepaliveInterval undefined`.

- [ ] **Step 2.3: Add the config field**

In `internal/config/config.go`, append to `ServerConfig` (right after the existing `StreamIdleTimeout` field around line 43):

```go
	// SSEKeepaliveInterval is the gap of downstream silence after which
	// modelserver injects an SSE comment line (":\n\n") to reset
	// client-side stall detection and middlebox idle timers. The timer
	// resets on every successful downstream Write, so a busy stream emits
	// no heartbeats. Default 15s — well under any known client stall
	// threshold and aligned with Anthropic's own ping cadence. Set to 0
	// to disable.
	SSEKeepaliveInterval time.Duration `yaml:"sse_keepalive_interval" mapstructure:"sse_keepalive_interval"`
```

In the `setDefaults` / `SetDefault` block (find it via `grep -n 'server.stream_idle_timeout' internal/config/config.go` — line 220), add right after the existing `StreamIdleTimeout` default:

```go
	v.SetDefault("server.sse_keepalive_interval", 15*time.Second)
```

- [ ] **Step 2.4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: all config tests PASS, including the two new assertions.

- [ ] **Step 2.5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add server.sse_keepalive_interval (default 15s)

Configures the new keepaliveWriter heartbeat cadence. 0 disables.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Plumb `InterruptErr` through `StreamMetrics`

**Files:**
- Modify: `internal/proxy/provider_transform.go`

**Interfaces:**
- Consumes: existing `StreamMetrics` struct (`internal/proxy/provider_transform.go:11-20`)
- Produces: `StreamMetrics.InterruptErr error` — set by the caller after `WrapStream` returns, before the interceptor's `finish()` runs. Read by `completeStreamingRequest` (Task 5).

Note: provider-specific `WrapStream` implementations (anthropic, openai, gemini, bedrock, codex, claudecode, vertex_*) all construct `StreamMetrics{...}` directly inside their `onComplete` closure. They do **not** set `InterruptErr`. The field is set by the executor's `stream_interrupted` branch on the metrics value the *callback* receives — which means the plumbing goes through a side channel, not through the per-provider builder. Task 4 handles the side channel.

- [ ] **Step 3.1: Add the field**

In `internal/proxy/provider_transform.go`, edit the `StreamMetrics` struct:

```go
// StreamMetrics is the unified metric output from all stream interceptors.
type StreamMetrics struct {
	Model               string
	MsgID               string
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	TTFTMs              int64
	// InterruptErr is set by the executor when copyWithFlush returned
	// an error (downstream write failed, upstream EOF mid-stream, etc).
	// nil on clean completion. Read by completeStreamingRequest to flip
	// the request row from success→error and record the cause.
	InterruptErr error
}
```

- [ ] **Step 3.2: Verify the package still compiles**

Run: `go build ./internal/proxy/...`
Expected: clean build. All existing `StreamMetrics{...}` literals across `provider_*.go` files compile unchanged because the new field is optional in Go struct literals.

- [ ] **Step 3.3: Commit**

```bash
git add internal/proxy/provider_transform.go
git commit -m "feat(proxy): add StreamMetrics.InterruptErr field

Optional field set by the executor's stream_interrupted branch and
consumed by completeStreamingRequest. Existing provider transformers
construct StreamMetrics with named fields, so all literals compile
unchanged.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Executor — wire keepaliveWriter and capture `copyErr`

**Files:**
- Modify: `cmd/modelserver/main.go`
- Modify: `internal/proxy/executor.go`

**Interfaces:**
- Consumes:
  - `newKeepaliveWriter(w, interval)` from Task 1
  - `cfg.Server.SSEKeepaliveInterval` from Task 2
  - `StreamMetrics.InterruptErr` from Task 3
- Produces:
  - `Executor.sseKeepaliveInterval time.Duration` field
  - `proxy.NewExecutor` gains a `sseKeepaliveInterval time.Duration` parameter (positional, immediately after `streamIdleTimeout` to mirror config grouping)
  - Streaming path wraps `w`/`flusher` in a `keepaliveWriter` between `copyWithFlush` and the real RW
  - On `copyErr != nil`, the captured error is published to the `metrics` value the interceptor's `onComplete` callback receives, via a per-request side channel (a pointer to a `*error` closed over in the `WrapStream` callback)

- [ ] **Step 4.1: Extend `NewExecutor` signature**

In `internal/proxy/executor.go`, find the `NewExecutor` function (line 166). Add a `sseKeepaliveInterval time.Duration` parameter immediately after the existing `streamIdleTimeout time.Duration` parameter:

```go
func NewExecutor(
	router *Router,
	st *store.Store,
	coll *collector.Collector,
	limiter ratelimit.RateLimiter,
	catalog modelcatalog.Catalog,
	logger *slog.Logger,
	maxBodySize int64,
	imagesMaxBodySize int64,
	extraUsageCfg config.ExtraUsageConfig,
	streamIdleTimeout time.Duration,
	sseKeepaliveInterval time.Duration,
	bl *httplog.Logger,
	blCfg config.HttpLogConfig,
) *Executor {
```

Add the field on the `Executor` struct (right after `streamIdleTimeout`, around line 160):

```go
	// sseKeepaliveInterval is the gap of downstream silence after which
	// a ":\n\n" SSE comment is injected to reset client stall timers.
	// 0 disables. See keepaliveWriter for semantics.
	sseKeepaliveInterval time.Duration
```

Set it in the struct literal at the bottom of `NewExecutor` (right after `streamIdleTimeout: streamIdleTimeout,`):

```go
		sseKeepaliveInterval: sseKeepaliveInterval,
```

- [ ] **Step 4.2: Pass the config through from main**

In `cmd/modelserver/main.go` line 204, update the `proxy.NewExecutor(...)` call to include the new arg in the same position:

```go
executor := proxy.NewExecutor(router, st, coll, rateLimiter, catalog, logger, cfg.Server.MaxRequestBody, cfg.Images.MaxBodySize, cfg.ExtraUsage, cfg.Server.StreamIdleTimeout, cfg.Server.SSEKeepaliveInterval, httpLogger, cfg.HttpLog)
```

If a logger.Warn check is desired for misconfigured interval (`>= StreamIdleTimeout`), add right after the `executor :=` line (still in `main.go`):

```go
if cfg.Server.SSEKeepaliveInterval > 0 && cfg.Server.StreamIdleTimeout > 0 && cfg.Server.SSEKeepaliveInterval >= cfg.Server.StreamIdleTimeout {
	logger.Warn("server.sse_keepalive_interval >= server.stream_idle_timeout — keepalive may not prevent idle-timeout trips",
		"keepalive", cfg.Server.SSEKeepaliveInterval,
		"idle_timeout", cfg.Server.StreamIdleTimeout)
}
```

- [ ] **Step 4.3: Wrap the ResponseWriter in the streaming path**

In `internal/proxy/executor.go`, find the streaming flush block (currently lines 1051-1065):

```go
	// Flush streaming data to the client.
	flusher, _ := w.(http.Flusher)

	n, copyErr := copyWithFlush(streamReader, w, flusher)
	if copyErr != nil {
		logger.Warn("stream_interrupted",
			"request_id", reqCtx.RequestID,
			"upstream_id", candidate.Upstream.ID,
			"bytes_sent", n,
			"error", copyErr.Error(),
		)
		e.router.Metrics().RecordError(candidate.Upstream.ID)
	}

	streamReader.Close()
```

Replace it with:

```go
	// Wrap w in a keepaliveWriter so a ":\n\n" SSE comment is injected
	// during upstream silence windows, preventing client stall detection
	// (Claude Code et al.) and middlebox idle ceilings from cutting long
	// reasoning turns mid-stream. The wrapper itself implements
	// http.Flusher and serialises heartbeats vs data writes through a
	// single mutex, so SSE events are never torn.
	kw := newKeepaliveWriter(w, e.sseKeepaliveInterval)
	defer kw.Close()

	n, copyErr := copyWithFlush(streamReader, kw, kw)
	if copyErr != nil {
		logger.Warn("stream_interrupted",
			"request_id", reqCtx.RequestID,
			"upstream_id", candidate.Upstream.ID,
			"bytes_sent", n,
			"error", copyErr.Error(),
		)
		e.router.Metrics().RecordError(candidate.Upstream.ID)
		// Publish the error so completeStreamingRequest can flip the
		// request row from success→error and record the cause. The
		// callback registered with transformer.WrapStream above closes
		// over `interruptErrPtr`; the interceptor's finish() runs from
		// streamReader.Close() below and will read it.
		*interruptErrPtr = copyErr
	}

	streamReader.Close()
```

Now declare `interruptErrPtr` and thread it into the `transformer.WrapStream` callback. Find the existing `WrapStream` call (line 1022):

```go
		wrapped = transformer.WrapStream(upstreamBody, startTime, func(metrics StreamMetrics) {
			e.completeStreamingRequest(candidate, reqCtx, metrics, startTime, cancelFn, logger)
		})
```

Replace with:

```go
		// interruptErrPtr is set by the stream-flush block below when
		// copyWithFlush fails. The interceptor's finish() runs on
		// streamReader.Close() *after* that block, so the closure here
		// reads the pointer at callback time, not at registration time.
		var interruptErr error
		interruptErrPtr := &interruptErr
		wrapped = transformer.WrapStream(upstreamBody, startTime, func(metrics StreamMetrics) {
			metrics.InterruptErr = *interruptErrPtr
			e.completeStreamingRequest(candidate, reqCtx, metrics, startTime, cancelFn, logger)
		})
```

Apply the same change to the **image** path right above it if present (find via `grep -n 'newImageStreamInterceptor' internal/proxy/executor.go`). The image branch calls `completeImageStreamingRequest` — it's a different settle path with its own status field; leave it alone for now (out of scope per the spec — non-streaming and image paths are not in scope). Confirm by re-reading the surrounding `if isImageRequestKind(reqCtx.RequestKind)` branch: do **not** add `interruptErrPtr` there.

- [ ] **Step 4.4: Verify the package builds**

Run: `go build ./...`
Expected: clean build across all packages.

- [ ] **Step 4.5: Run the full proxy test suite to catch regressions**

Run: `go test ./internal/proxy/... -count=1`
Expected: all existing tests still PASS.

- [ ] **Step 4.6: Commit**

```bash
git add cmd/modelserver/main.go internal/proxy/executor.go
git commit -m "feat(proxy): wrap streaming RW in keepaliveWriter, capture copyErr

Wires sseKeepaliveInterval through Executor; wraps the per-request
http.ResponseWriter so :\n\n is injected during upstream silence.
copyWithFlush errors are now published to StreamMetrics.InterruptErr
via a per-request pointer the WrapStream callback closes over, ready
for the next task to flip status=error.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: `completeStreamingRequest` — honour `InterruptErr`

**Files:**
- Modify: `internal/proxy/executor.go`
- Create: `internal/proxy/executor_stream_interrupt_test.go`

**Interfaces:**
- Consumes: `StreamMetrics.InterruptErr` (Task 3), set by executor wiring (Task 4)
- Produces: no new exported names; observable behaviour change is `requests.status='error'` + `error_message='stream_interrupted: ...'` when `InterruptErr != nil`.

- [ ] **Step 5.1: Write the failing test**

Create `internal/proxy/executor_stream_interrupt_test.go`:

```go
package proxy

import (
	"errors"
	"strings"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// TestCompleteStreamingRequest_InterruptSetsError verifies that when the
// metrics callback fires with a non-nil InterruptErr, the synthesised
// types.Request has Status=error and ErrorMessage prefixed with
// "stream_interrupted:". Drives the building branch of
// completeStreamingRequest in isolation by constructing the same fields
// the function does and asserting on them.
//
// We exercise the production code path indirectly through the helper
// buildInterruptRequestFields (extracted in step 5.2), avoiding a fake
// store roundtrip.
func TestCompleteStreamingRequest_InterruptSetsError(t *testing.T) {
	metrics := StreamMetrics{
		Model:        "claude-opus-4-8",
		InterruptErr: errors.New("write tcp 1.2.3.4: broken pipe"),
	}
	status, msg := requestStatusFromMetrics(metrics)
	if status != types.RequestStatusError {
		t.Errorf("status = %q, want %q", status, types.RequestStatusError)
	}
	if !strings.HasPrefix(msg, "stream_interrupted: ") {
		t.Errorf("error_message = %q, want prefix %q", msg, "stream_interrupted: ")
	}
	if !strings.Contains(msg, "broken pipe") {
		t.Errorf("error_message = %q, want it to include the underlying error", msg)
	}
}

// TestCompleteStreamingRequest_CleanStreamRecordsSuccess is the regression
// guard for normal completions — must remain Status=success with no
// error_message.
func TestCompleteStreamingRequest_CleanStreamRecordsSuccess(t *testing.T) {
	metrics := StreamMetrics{
		Model:        "claude-opus-4-8",
		OutputTokens: 100,
		TTFTMs:       42,
	}
	status, msg := requestStatusFromMetrics(metrics)
	if status != types.RequestStatusSuccess {
		t.Errorf("status = %q, want %q", status, types.RequestStatusSuccess)
	}
	if msg != "" {
		t.Errorf("error_message = %q, want empty", msg)
	}
}

```

- [ ] **Step 5.2: Run the test to verify it fails**

Run: `go test ./internal/proxy/ -run TestCompleteStreamingRequest -v`
Expected: compilation error — `undefined: requestStatusFromMetrics`.

- [ ] **Step 5.3: Add the helper and use it in `completeStreamingRequest`**

Open `internal/proxy/executor.go`. Add the helper near the bottom of the file (next to `upstreamTimeout`, around line 1565):

```go
// requestStatusFromMetrics maps a StreamMetrics result to the (status,
// error_message) pair to persist on the requests row. Interrupted streams
// (copyWithFlush returned an error mid-flight) become Status=error with
// the underlying error recorded so dashboards and billing see the truth
// instead of a phantom success.
func requestStatusFromMetrics(m StreamMetrics) (string, string) {
	if m.InterruptErr != nil {
		return types.RequestStatusError, "stream_interrupted: " + m.InterruptErr.Error()
	}
	return types.RequestStatusSuccess, ""
}
```

Now find `completeStreamingRequest` (line 1068) and the `req := types.Request{...}` literal that hardcodes `Status: types.RequestStatusSuccess` (line 1112). Replace those two lines:

```go
	Status:                types.RequestStatusSuccess,
```

with:

```go
	Status:                "", // set below from metrics
```

…and immediately after the struct literal (before the `if reqCtx.RequestID != ""` block around line 1125), insert:

```go
	status, errMsg := requestStatusFromMetrics(metrics)
	req.Status = status
	req.ErrorMessage = errMsg
```

Also update the trailing `logger.Info("request completed", ..., "status", types.RequestStatusSuccess, ...)` call (around line 1137) so the log reflects the real status:

```go
	logger.Info("request completed",
		"msg_id", metrics.MsgID,
		"status", status,
		"streaming", true,
		...
```

(The remaining `logger.Info` keys stay unchanged.)

- [ ] **Step 5.4: Run the tests to verify they pass**

Run: `go test ./internal/proxy/ -run TestCompleteStreamingRequest -v`
Expected: both sub-tests PASS.

- [ ] **Step 5.5: Run the full proxy suite for regression**

Run: `go test ./internal/proxy/... -count=1`
Expected: all tests PASS.

- [ ] **Step 5.6: Commit**

```bash
git add internal/proxy/executor.go internal/proxy/executor_stream_interrupt_test.go
git commit -m "fix(proxy): record stream_interrupted as status=error

completeStreamingRequest now reads StreamMetrics.InterruptErr (set by
the stream-flush block when copyWithFlush fails) and flips the requests
row from success→error with a 'stream_interrupted: <err>' message.
Closes the gap that let mid-flight broken-pipe failures be billed and
reported as clean completions.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Run full test + build + manual acceptance checklist

**Files:** none modified.

- [ ] **Step 6.1: Full test sweep**

Run: `go test ./... -count=1`
Expected: all tests PASS. If any other package (admin, billing) broke from the `NewExecutor` signature change, fix the call site and re-run — but `grep -rn 'proxy.NewExecutor' --include="*.go"` should show only `cmd/modelserver/main.go` already updated in Task 4.

- [ ] **Step 6.2: Race detector across proxy and config**

Run: `go test -race ./internal/proxy/... ./internal/config/... -count=2`
Expected: no race detected.

- [ ] **Step 6.3: Build the server binary**

Run: `go build -o /tmp/modelserver-keepalive ./cmd/modelserver`
Expected: clean build, binary produced.

- [ ] **Step 6.4: Manual acceptance checklist (post-deploy)**

These three scenarios must be run by a human on staging or a production canary after the binary lands. They are not gated in CI. Tick each as done in the deploy ticket.

1. **Long Opus reasoning no longer cut**
   - Drive Claude Code 2.1.x against `code.ai.cs.ac.cn` with a task that triggers ≥ 8 minutes of continuous reasoning.
   - Expect: no `API Error: Response stalled mid-stream`; no `stream_interrupted` entries in modelserver logs for that request_id.

2. **Heartbeat visible on the wire**
   ```bash
   curl -N -H "Authorization: Bearer $KEY" \
        -H "Content-Type: application/json" \
        --data-binary @long-reasoning.json \
        https://code.ai.cs.ac.cn/v1/messages \
        2>&1 | ts '[%H:%M:%.S]' | tee curl.log
   grep -E '^\[[0-9:.]+\][[:space:]]*:[[:space:]]*$' curl.log | head
   ```
   Expect: a `:` line every ~15 s during any upstream silence window.

3. **DB row regression — forced interrupt**
   - Mid-stream, `kill -9` the client (or unplug network ~20 s) so modelserver logs `stream_interrupted`.
   - Run, replacing `<REQ_ID>` with the request_id from the log:
     ```sql
     SELECT id, status, error_message, output_tokens, latency_ms
     FROM requests WHERE id = '<REQ_ID>';
     ```
   - Expect: `status='error'`, `error_message` starts with `stream_interrupted:`, `latency_ms` matches the observed cut time.

- [ ] **Step 6.5: Mark plan complete in deploy ticket**

No further commits in this plan. Subsequent follow-up (e.g. `upstream.ReadTimeout` semantics fix called out in the chat that produced this plan) is a separate spec and plan.
