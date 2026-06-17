# SSE `data:` prefix parsing — strict-prefix bug fix

**Date:** 2026-06-17
**Status:** Approved, ready for plan

## Goal

Fix a longstanding bug where streaming requests through any upstream that
emits SSE `data:` lines **without** a trailing space (e.g.,
DashScope's `/apps/anthropic` BaiLian app endpoint) have their `usage` /
`ttft` silently dropped at the stream interceptor layer. After this fix,
both prefix forms (`data:` and `data: `) parse identically, matching the
HTML SSE specification.

## Background

The proxy intercepts every streaming response to extract per-request
metrics:

- **Anthropic-protocol streams** are parsed by
  `internal/proxy/stream.go`'s `streamInterceptor.parseLine`, which then
  delegates JSON payload extraction to `internal/proxy/parser.go`'s
  `ParseStreamEvent`. The function looks for the SSE-event types
  `message_start`, `content_block_delta`, and `message_delta` and
  records `input_tokens` / `cache_*_tokens` / `output_tokens` / `ttft_ms`.
- **OpenAI-protocol chat-completion streams** are parsed by
  `internal/proxy/chatcompletions_stream.go`'s `parseLine`, which
  extracts `usage.{prompt,completion,cached_prompt}_tokens` from
  `data:` events.

Both functions detect a "data" line by checking:

```go
if !bytes.HasPrefix(line, []byte("data: ")) {  // 6 bytes — note the space
    return
}
```

That literal hard-codes the **6-byte** prefix including a trailing
space. Any line emitted as `data:{json}` (no space) silently fails the
prefix check and is dropped.

## Why this matters

A live audit of `https://codeapi.cs.ac.cn` (2026-06-17) shows the
DashScope `/apps/anthropic` upstream
(`https://dashscope.aliyuncs.com/apps/anthropic`) emits SSE in the form:

```
event:message_start
data:{"message":{"model":"glm-5.2","id":"msg_…","usage":{"input_tokens":3,"output_tokens":0}},"type":"message_start"}

event:content_block_delta
data:{"delta":{"type":"thinking_delta","thinking":"…"},"type":"content_block_delta","index":0}
```

No space after `event:` or `data:`. As a result, **every** streaming
request through this upstream — across 13 supported models (glm-5.2,
glm-5.1, kimi-k2.5, qwen3-max, qwen3.6-plus, minimax-m2.x,
deepseek-v4-flash, deepseek-v4-pro, etc.) — records 0 input / 0 output
/ 0 ttft. Non-streaming requests on the same upstream record correctly
because they bypass the SSE parser entirely.

The same bug exists in `chatcompletions_stream.go` for the OpenAI
protocol path. Any future upstream pointed at an OpenAI-compatible
endpoint that emits `data:{…}` (no space) will exhibit the same
silent-zero-usage failure.

## Why our parser is wrong

The HTML SSE spec
([whatwg, §9.2.6](https://html.spec.whatwg.org/multipage/server-sent-events.html#parsing-an-event-stream))
defines a "data" field as: a line starting with `data:`, then optionally
skipping a single U+0020 SPACE if present, with the remainder being the
data. Both `data:foo` and `data: foo` MUST yield the same data `"foo"`.
Our prefix check rejects the no-space form.

## Scope

In scope:
- Fix `internal/proxy/stream.go`'s `parseLine` to accept both forms.
- Fix `internal/proxy/chatcompletions_stream.go`'s `parseLine` to
  accept both forms.
- Add unit tests covering the no-space form for both files.

Out of scope:
- Refactoring both call sites into a shared SSE helper. Each callsite
  uses the prefix once; YAGNI.
- Other SSE field handling (e.g., `event:` line — we don't parse the
  event name from the SSE wire format; we read it from the JSON payload
  on each `data:` line, so the `event:` line going unmatched is harmless).
- Changing the DashScope upstream config or migration data. The bug is
  in the parser, not the migration/config.
- Backfilling historical zero-usage rows (the data is gone; no upstream
  capture).

## Fix approach

Replace the single hard-coded prefix check with a two-step pattern:

1. Require the line to start with `data:`.
2. Strip that prefix, then strip a single leading space if present.

In Go:

```go
const dataPrefix = "data:"
if !bytes.HasPrefix(line, []byte(dataPrefix)) {
    return
}
data := line[len(dataPrefix):]
if len(data) > 0 && data[0] == ' ' {
    data = data[1:]
}
```

This is a 3-line change per callsite. Behaviour is unchanged for
spec-conforming upstreams (Anthropic, Vertex, OpenAI, etc.) because
they already include the optional space; the only observable change is
that previously-silently-dropped no-space lines now parse correctly.

### Why strip exactly one space (not all leading whitespace)

The HTML SSE spec (whatwg §9.2.6, "Interpreting an event stream") says
of a field value: **"If value starts with a U+0020 SPACE character,
remove it from value."** Exactly one — by design, so that an event of
`data:  hello` (two spaces) yields data `" hello"` (one space + hello),
preserving any intentional leading whitespace in the payload.

Tempting "improvement" to AVOID in future PRs: do NOT change the body
to `data = bytes.TrimLeft(data, " ")`. That would greedily strip
multiple spaces and break upstreams that intentionally embed leading
whitespace in a `data:` payload (rare in JSON streams since JSON parsers
tolerate it, but possible in plain-text streams).

In practice for this repo today, downstream consumers of `data` are:
1. `ParseStreamEvent` → `json.Unmarshal` — tolerates any leading
   whitespace, so multi-space `data:` is benign.
2. `bytes.Equal(data, []byte("[DONE]"))` — strict; a multi-space
   `data:  [DONE]` would fail the sentinel check, but the row then
   falls through harmlessly (unmarshal fails, parseLine returns). No
   data is lost.

So we are correct per spec AND robust in practice. The single-space
strip is the right rule.

## File changes

### 1. `internal/proxy/stream.go`

Replace the existing `data: ` prefix check and `TrimPrefix(line, "data: ")`
(lines 72–77) with the two-step pattern above. Keep the
`[DONE]` sentinel check unchanged.

### 2. `internal/proxy/chatcompletions_stream.go`

Same change at the analogous lines (78–84 per current diff).

### 3. `internal/proxy/stream_test.go`

Add a regression test that feeds the interceptor a stream where every
`data:` line lacks the trailing space and asserts the same usage and
TTFT are captured as the existing tests.

### 4. `internal/proxy/chatcompletions_stream_test.go` (or equivalent)

Same regression test for the OpenAI parser path.

## Risks

- **Backward-compat for downstream consumers**: zero. The intercepted
  stream body is passed through to the client byte-for-byte, unchanged.
  Only our internal parsing path is affected.
- **False positives on `data:` lines that aren't SSE data**: extremely
  unlikely. `data:` is not a common substring at the start of a
  newline-bounded byte sequence outside SSE. The existing 6-byte check
  was already prone to this if anything (e.g., a 5-byte `data:` line in
  some malformed upstream payload would not have been picked up). The
  new code is no less safe.
- **Cross-cutting**: this fixes 13+ models on one production upstream
  starting on next deploy, retroactively eliminating the zero-usage
  problem going forward. No data backfill is possible.

## Verification

After implementation:

1. `go test ./internal/proxy/... -count=1` — all existing tests still
   pass; the two new no-space regression tests pass.
2. `go build ./...` — exit 0.
3. After deploy, re-run a streaming glm-5.2 request through
   codeapi.cs.ac.cn; the resulting row in the admin requests view
   should show non-zero `input_tokens`, `output_tokens`, and `ttft_ms`.

## Sources

- HTML SSE spec, "data" field parsing rules:
  [whatwg §9.2.6](https://html.spec.whatwg.org/multipage/server-sent-events.html#parsing-an-event-stream)
- Live capture against `codeapi.cs.ac.cn`'s dashscope upstream
  (2026-06-17): `event:message_start\ndata:{…}` confirms no-space form.
- Per-upstream usage audit (2026-06-17): 100% of streaming requests via
  upstream `4d10a70a-a093-4da8-aa43-b67ee25480f6` recorded 0 input / 0
  output / 0 ttft across 50+ requests spanning 7 models; non-streaming
  on same upstream records correctly.
