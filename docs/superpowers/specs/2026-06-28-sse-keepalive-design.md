# SSE keepalive injection + stream-interrupt status fix

**Date:** 2026-06-28
**Status:** Approved, ready for plan

## Goal

Stop streaming responses from being torn down mid-flight when an upstream
goes silent for tens of seconds (long Opus reasoning, large tool turns,
1M-context single-turn). modelserver will inject an SSE comment line
`:\n\n` on the downstream socket whenever no bytes have been written for
a configurable interval (default 15s), keeping every client-side and
middlebox-side "no bytes for N seconds → close" timer perpetually reset.

The same change also fixes a longstanding bug where a stream that was
torn down mid-flight is still recorded in the `requests` table as
`status='success'` with an empty `error_message`, hiding the failure
from dashboards and billing.

## Background

### Symptom

Production captured request `ccef7479-d806-4717-b268-7c9c8aea8e26`:

| Field | Value |
|---|---|
| `created_at` | 2026-06-28 08:46:08 UTC |
| `stream_interrupted` log | 2026-06-28 08:53:05 UTC (≈ 7 min later) |
| `bytes_sent` | 1,989,835 (~1.9 MB already delivered) |
| `error` | `write tcp 172.18.0.5:8080->10.6.0.10:47014: write: broken pipe` |
| `latency_ms` | 416 716 |
| `output_tokens` | 0 |
| `status` | **`success`** ← bug |
| `error_message` | **NULL** ← bug |

A second sample landed at ~5 minutes; durations are not fixed. End-user
visible failure mode is the Claude Code CLI surfacing
`API Error: Response stalled mid-stream. The response above may be
incomplete.`

### Why neither modelserver nor the LB is the proximate cause

- `internal/proxy/idle_timeout_reader.go` watches upstream idle and
  trips at `server.stream_idle_timeout` (default 10 min — `internal/
  config/config.go:220`). The captured interrupt fired well before
  that, on a *downstream write*, not an upstream read. So the
  watchdog wasn't the trigger.
- `internal/proxy/executor.go:1566-1573` `upstreamTimeout()` returns
  `0` (no deadline) for streaming requests by design — long Opus turns
  are explicitly allowed.
- The production Tencent CLB (STGW) in front of modelserver is
  configured with `proxy_read_timeout 3600s`, `proxy_send_timeout
  600s`, `proxy_ignore_client_abort on`. None of those map to the
  observed ~5–7 min cut.

`broken pipe` on the downstream socket means **the peer (CLB) sent
FIN/RST first**. The most consistent explanation given the variable
duration and the lack of any modelserver- or LB-side 5-min knob: the
**client** (Claude Code) hit its own stall-detection threshold during
an Opus reasoning silence and closed; CLB transmitted the close to
modelserver despite `proxy_ignore_client_abort on` (a well-known STGW
quirk for SSE responses).

This matches the public reports:

- [anthropics/claude-code#69415][cc-69415] — Claude Code shows
  "Connection closed mid-response" with no resume capability.
- [anthropics/claude-code#67766][cc-67766] — tcpdump-confirmed
  server-initiated FIN mid-stream against `api.anthropic.com`
  itself, no fix from Anthropic. Validates that the same class of
  cut also happens *upstream-of-modelserver*; keepalive will mask
  most of those too because the next downstream write will only be
  attempted on the re-armed timer rather than blocking the connection
  forever.

### Why `:\n\n` is the right payload

The Anthropic SDK (used by Claude Code via bun-compiled binary) parses
SSE through `@anthropic-ai/sdk`'s `core/streaming.ts`. The decoder's
first guard is:

```ts
if (line.startsWith(':')) {
  return null;
}
```

— a comment line never enters event accumulation, never produces an
SSE event object, never reaches token counters. Identical guarantee
in the OpenAI SDK, Google Generative AI SDK, and the browser-native
`EventSource`. The bytes still flow through the underlying
`fetch`/HTTP reader, which is what resets any byte-level stall timer
the client may run.

### Why the DB row says `success`

`internal/proxy/executor.go:1054` already calls `logger.Warn(
"stream_interrupted", ...)` when `copyWithFlush` returns an error,
but the metrics callback wired up by `transformer.WrapStream`
(`executor.go:1022`) was registered *before* the copy started and has
no signal that the copy aborted. It runs anyway with whatever usage
fields it managed to parse (typically `output_tokens=0` because the
`message_delta` event never arrived), and `completeStreamingRequest`
(`executor.go:1068`) unconditionally writes
`Status: types.RequestStatusSuccess` (`executor.go:1112`) — the call
path has no branch that downgrades the status when the copy aborted.

## Design

### Streaming write path

```
Before:
  upstream Body
    → idleTimeoutReader  (server.stream_idle_timeout, default 10 min)
    → transformer.WrapStream  (metrics interceptor)
    → [optional] httplog TeeReadCloser
    → copyWithFlush  (executor.go:1521)
    → http.ResponseWriter

After:
  upstream Body
    → idleTimeoutReader
    → transformer.WrapStream
    → [optional] httplog TeeReadCloser
    → copyWithFlush
    → keepaliveWriter  (NEW; single mutex serialises data + heartbeat)
    → http.ResponseWriter
```

### `keepaliveWriter` (`internal/proxy/keepalive_writer.go`)

```go
// keepaliveWriter wraps an http.ResponseWriter + http.Flusher and injects
// SSE comment lines ":\n\n" when no data has been written for `interval`.
// Heartbeat and data writes share a single mutex, so a heartbeat can never
// land in the middle of a partially-written SSE event.
//
// A timer (re-armed on every Write) fires on a background goroutine.
// On fire, it acquires the mutex, writes ":\n\n", Flushes, and re-arms
// itself. Close() stops the timer and the goroutine.
//
// interval <= 0 disables the keepalive: the writer is a thin pass-through.
type keepaliveWriter struct {
    w        http.ResponseWriter
    flusher  http.Flusher
    interval time.Duration

    mu     sync.Mutex
    timer  *time.Timer
    closed bool
}

func newKeepaliveWriter(w http.ResponseWriter, interval time.Duration) *keepaliveWriter
func (k *keepaliveWriter) Write(p []byte) (int, error)
func (k *keepaliveWriter) Flush()
func (k *keepaliveWriter) Close() error
```

### Trigger semantics

- The timer's anchor is **the last successful downstream `Write`.** Any
  upstream byte that reaches `copyWithFlush` → `keepaliveWriter.Write`
  resets the timer; upstream silence == timer not reset == heartbeat
  fires.
- The heartbeat itself **does not** reset the timer (it re-arms it
  *once* after writing, so the next heartbeat is again exactly
  `interval` away — no busy loop).
- Only the streaming path constructs a `keepaliveWriter`. Non-stream
  responses bypass `copyWithFlush` entirely and pay zero overhead.

### Mutex contract

```
Write(data):    lock → w.Write(data) → flusher.Flush() → reset timer → unlock
onHeartbeat:    lock → if !closed: w.Write(":\n\n") → flusher.Flush() → re-arm timer → unlock
Close:          lock → closed=true → timer.Stop() → unlock
```

A single mutex protects the timer field *and* the ResponseWriter
write. Heartbeats and data chunks are therefore mutually exclusive at
the write level — SSE event framing is preserved.

### Configuration

```yaml
server:
  sse_keepalive_interval: 15s   # default; 0 disables
```

- `internal/config/config.go`: add field
  `SSEKeepaliveInterval time.Duration`, `SetDefault(
  "server.sse_keepalive_interval", 15*time.Second)`.
- `cmd/modelserver/main.go:204`: pass through into
  `proxy.NewExecutor(...)` alongside the existing
  `cfg.Server.StreamIdleTimeout`.
- Startup sanity: if `SSEKeepaliveInterval > 0 &&
  SSEKeepaliveInterval >= StreamIdleTimeout`, log a Warn but do not
  refuse — the operator probably wants the idle watchdog to be the
  primary signal and that's still safe.

### DB fix: `status` / `error_message` on interrupt

`StreamMetrics` gets one new field:

```go
type StreamMetrics struct {
    // ... existing fields ...
    InterruptErr error // nil on clean completion; set when copyWithFlush returned an error
}
```

`executor.go:1054` `copyWithFlush` returning `copyErr != nil`:

1. Log `stream_interrupted` as today.
2. Set `InterruptErr = copyErr` on the metrics object that the
   interceptor will hand to `completeStreamingRequest`.

`completeStreamingRequest` (`executor.go:1068`):

```go
if metrics.InterruptErr != nil {
    r.Status = types.RequestStatusError
    r.ErrorMessage = "stream_interrupted: " + metrics.InterruptErr.Error()
} else {
    r.Status = types.RequestStatusSuccess
}
```

- `output_tokens` keeps whatever the upstream stream parser actually
  observed (often `0` when the cut happens before `message_delta`) —
  we explicitly do **not** estimate. Audit can do that retroactively
  if it ever matters.
- We **do not** emit an SSE `event: error` frame before closing. By
  the time `copyErr` fires, the downstream socket is already gone (or
  in an unknown state mid-event); writing more bytes risks tearing an
  in-flight event.
- No new request-status value is introduced. Reuse the existing
  `types.RequestStatusError`.

## Testing

### Unit — `internal/proxy/keepalive_writer_test.go`

| # | Name | Verifies |
|---|---|---|
| 1 | `TestKeepaliveWriter_DisabledIsPassthrough` | `interval=0`: Write lands on underlying RW; Close emits no heartbeat; no goroutine leak |
| 2 | `TestKeepaliveWriter_EmitsCommentOnSilence` | `interval=20ms`: idle 80ms then Close; underlying buffer contains ≥ 3 occurrences of `:\n\n` |
| 3 | `TestKeepaliveWriter_ResetsTimerOnWrite` | `interval=30ms`, write every 10ms for 200ms: buffer contains zero `:\n\n` |
| 4 | `TestKeepaliveWriter_NoInterleaveUnderConcurrency` | Concurrent goroutine writes 100 full SSE events while heartbeat at `interval=1ms` races: SSE parser yields exactly 100 events, no `:\n\n` falls inside any `event:` … `\n\n` span |
| 5 | `TestKeepaliveWriter_CloseStopsHeartbeat` | After Close, idle `3*interval`: buffer no longer grows; goroutine count returns to baseline |
| 6 | `TestKeepaliveWriter_FlusherCalledEachHeartbeat` | Fake flusher's count == heartbeat count + data-write count |

All assertions use condition-polled waits (timeout 1s), not
sleep-and-check, to keep them stable.

### Unit — `internal/proxy/executor_stream_interrupt_test.go`

| # | Name | Verifies |
|---|---|---|
| 7 | `TestExecutor_StreamInterruptedSetsErrorStatus` | Fake upstream half-closes mid-stream; fake `http.ResponseWriter` returns broken pipe on Write; `CompleteRequest` invoked with `Status=error` and `ErrorMessage` prefixed `"stream_interrupted: "` |
| 8 | `TestExecutor_NormalStreamStillRecordsSuccess` | Clean stream (including final `message_delta.usage`) records `Status=success` and empty `ErrorMessage` — regression guard |

### Manual acceptance

Run after deploying to staging / production canary:

1. **Long Opus reasoning no longer cut**
   - Drive Claude Code 2.1.x against `code.ai.cs.ac.cn` with a task
     that triggers ≥ 8 minutes of continuous reasoning (e.g. a
     cross-directory `find` + summarise).
   - Expect: no `API Error: Response stalled mid-stream`; no
     `stream_interrupted` entries in modelserver logs.

2. **Heartbeat visible on the wire**
   ```bash
   curl -N -H "Authorization: Bearer $KEY" \
        -H "Content-Type: application/json" \
        --data-binary @long-reasoning.json \
        https://code.ai.cs.ac.cn/v1/messages \
        2>&1 | ts '[%H:%M:%.S]' | tee curl.log
   grep -E '^\[.*\]\s*:\s*$' curl.log | head
   ```
   Expect: one `:` line every ~15s during upstream silence windows.

3. **DB row regression — forced interrupt**
   - `kill -9` the client mid-stream (or simulate by unplugging
     network for ~20s).
   - Inspect the resulting `requests` row:
     `status='error'`, `error_message` starts with `stream_interrupted:`,
     `bytes_sent` consistent with what the log recorded.

CI runs tests #1–#8 only; the manual checklist ships as a release-time
checkbox.

## Files

### Add

- `internal/proxy/keepalive_writer.go`
- `internal/proxy/keepalive_writer_test.go`
- `internal/proxy/executor_stream_interrupt_test.go`

### Modify

- `internal/config/config.go` — `SSEKeepaliveInterval` field +
  default + startup warn
- `internal/config/config_test.go` — default / override assertions
  (mirror the existing `StreamIdleTimeout` pair)
- `config.example.yml` — `server.sse_keepalive_interval: 15s` with
  comment
- `cmd/modelserver/main.go:204` — thread the new config into
  `NewExecutor`
- `internal/proxy/executor.go`:
  - `Executor` struct: `sseKeepaliveInterval time.Duration`
  - `NewExecutor` signature
  - `executor.go:1052-1054` block: wrap `w`/`flusher` in
    `keepaliveWriter` and `defer kw.Close()`
  - `executor.go:1056` `stream_interrupted` branch: propagate
    `copyErr` into metrics
- `internal/proxy/stream.go` (or wherever `WrapStream` / `StreamMetrics`
  live) — add `InterruptErr error` to `StreamMetrics`; plumb it
  through the interceptor → callback path
- `internal/proxy/executor.go:1068` `completeStreamingRequest` —
  set `Status` / `ErrorMessage` from `metrics.InterruptErr`
- `docs/superpowers/specs/2026-06-28-sse-keepalive-design.md` (this
  file)

### No-touch (explicit)

- `idleTimeoutReader` — still the 10-min watchdog of last resort,
  complementary to the heartbeat
- Any transformer / SSE parser — heartbeat writes downstream of them
- `requests` table schema — reuse existing `error` status value
- All non-streaming code paths

[cc-69415]: https://github.com/anthropics/claude-code/issues/69415
[cc-67766]: https://github.com/anthropics/claude-code/issues/67766
