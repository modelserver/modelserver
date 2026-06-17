# SSE `data:` prefix parsing — strict-prefix bug fix — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Anthropic and OpenAI streaming-interceptor parsers tolerant of `data:` SSE lines without the trailing space, conforming to the HTML SSE spec (whatwg §9.2.6). Unblocks usage capture for DashScope's `/apps/anthropic` upstream and any future upstream that emits SSE without the optional space.

**Architecture:** Two-line behavioural change at two parser callsites (`internal/proxy/stream.go` and `internal/proxy/chatcompletions_stream.go`). Replace `bytes.HasPrefix(line, "data: ")` + `bytes.TrimPrefix(line, "data: ")` with a require-`data:`-then-optionally-skip-one-space pattern. TDD: failing regression test per file showing no-space form fails today, passing after fix.

**Tech Stack:** Go, `bytes` stdlib.

**Spec:** `docs/superpowers/specs/2026-06-17-sse-data-prefix-parsing-design.md`

## Global Constraints

- **SSE spec rule (whatwg §9.2.6)**: a `data:` line's value is the bytes after `data:`, with **exactly one** leading U+0020 SPACE removed if present. Do NOT use `bytes.TrimLeft(data, " ")` or `bytes.TrimLeft(data, " \t")` — that would greedily strip multiple spaces and break the spec for upstreams that intentionally embed leading whitespace in `data:` payloads.
- **The fix MUST NOT change behaviour for spec-conforming upstreams** (Anthropic, Vertex, OpenAI, etc.) that already emit `data: ` with the space. Existing tests must keep passing unmodified.
- **No refactoring**: do NOT abstract the prefix-strip logic into a shared helper. Two callsites, each used once; YAGNI.
- **No other SSE field handling changes**. We don't parse the `event:` line from the wire — we read the event type from the JSON payload's `type` field. The `event:` line going unmatched is harmless and unchanged by this fix.
- **Pristine test output**: no stray prints, no warnings.

---

## File map

- **Modify** `internal/proxy/stream.go` — replace the 6-byte prefix check / trim at the top of `parseLine`.
- **Modify** `internal/proxy/chatcompletions_stream.go` — same change at the analogous location.
- **Modify** `internal/proxy/stream_test.go` — add `TestStreamInterceptor_DataNoSpace` regression test.
- **Modify** `internal/proxy/chatcompletions_stream_test.go` — add `TestChatCompletionsStreamInterceptor_DataNoSpace` regression test.

---

## Task 1: TDD for Anthropic stream interceptor (`stream.go`)

**Files:**
- Modify: `internal/proxy/stream_test.go`
- Modify: `internal/proxy/stream.go`

**Interfaces:**
- Consumes: existing `newStreamInterceptor(inner, startTime, onComplete)` constructor and `streamInterceptor.Read` semantics.
- Produces: `parseLine` accepts both `data: …` and `data:…` forms and yields the same parsed event for both.

- [ ] **Step 1: Add the failing regression test**

Open `/root/coding/modelserver/internal/proxy/stream_test.go`. After the existing `TestStreamInterceptor` function, add this new function. It's a near-copy of the existing test but with **every** `data:` line written without the trailing space — exactly what DashScope's `/apps/anthropic` emits:

```go
// TestStreamInterceptor_DataNoSpace covers SSE streams where `data:` lines
// omit the optional trailing space (per HTML SSE spec §9.2.6: the space
// after the colon is optional). Real-world example: DashScope's
// `/apps/anthropic` BaiLian app endpoint emits `data:{...}` not `data: {...}`.
// Without this tolerance, usage and TTFT extraction silently drop to zero
// for every streaming request through such an upstream.
func TestStreamInterceptor_DataNoSpace(t *testing.T) {
	sseData := strings.Join([]string{
		`event:message_start`,
		`data:{"type":"message_start","message":{"id":"msg_1","model":"glm-5.2","usage":{"input_tokens":100,"cache_creation_input_tokens":5,"cache_read_input_tokens":10}}}`,
		``,
		`event:content_block_delta`,
		`data:{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		``,
		`event:message_delta`,
		`data:{"type":"message_delta","usage":{"output_tokens":50}}`,
		``,
		`event:message_stop`,
		`data:{"type":"message_stop"}`,
		``,
	}, "\n")

	var gotModel, gotMsgID string
	var gotUsage anthropic.Usage
	var gotTTFT int64
	done := make(chan struct{})

	interceptor := newStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		func(model, msgID string, usage anthropic.Usage, ttft int64) {
			gotModel = model
			gotMsgID = msgID
			gotUsage = usage
			gotTTFT = ttft
			close(done)
		},
	)

	output, err := io.ReadAll(interceptor)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}
	if string(output) != sseData {
		t.Errorf("output differs from input (pass-through must be byte-exact)")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callback not invoked within 1s")
	}

	if gotModel != "glm-5.2" {
		t.Errorf("model = %q, want %q", gotModel, "glm-5.2")
	}
	if gotMsgID != "msg_1" {
		t.Errorf("msgID = %q, want %q", gotMsgID, "msg_1")
	}
	if gotUsage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", gotUsage.InputTokens)
	}
	if gotUsage.CacheCreationInputTokens != 5 {
		t.Errorf("CacheCreationInputTokens = %d, want 5", gotUsage.CacheCreationInputTokens)
	}
	if gotUsage.CacheReadInputTokens != 10 {
		t.Errorf("CacheReadInputTokens = %d, want 10", gotUsage.CacheReadInputTokens)
	}
	if gotUsage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", gotUsage.OutputTokens)
	}
	if gotTTFT <= 0 {
		t.Errorf("TTFT = %d, want > 0 (content_block_delta should set it)", gotTTFT)
	}
}
```

- [ ] **Step 2: Run the new test and confirm RED**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/... -run TestStreamInterceptor_DataNoSpace -count=1 -v
```
Expected: FAIL. The failure messages should show `model = ""`, `msgID = ""`, `InputTokens = 0`, `OutputTokens = 0`, `TTFT = 0` — all the symptoms of the live bug.

If it PASSES instead, the bug isn't where we think; STOP and report. If it FAILS for a different reason (e.g., compile error from the new test code), STOP and fix the test first.

- [ ] **Step 3: Apply the fix in `stream.go`**

Open `/root/coding/modelserver/internal/proxy/stream.go`. Find `parseLine` (currently lines 72–105). Replace the prefix-check and `TrimPrefix` lines (72–77) with the SSE-spec-conformant version. The exact target lines today:

```go
func (si *streamInterceptor) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return
	}
	data := bytes.TrimPrefix(line, []byte("data: "))
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}
```

Change them to:

```go
func (si *streamInterceptor) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	// SSE spec (whatwg §9.2.6): a `data:` field's value is the bytes after
	// the colon, with EXACTLY ONE leading U+0020 SPACE removed if present.
	// Do not greedy-trim — multi-space `data:` is rare but spec-defined
	// to preserve the extra spaces as data content. JSON consumers tolerate
	// the resulting leading whitespace; the `[DONE]` sentinel doesn't appear
	// with multi-space prefixes in practice.
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := line[len("data:"):]
	if len(data) > 0 && data[0] == ' ' {
		data = data[1:]
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}
```

Leave the rest of `parseLine` (lines 82–105: `ParseStreamEvent` call, model/msgID/ttft/usage capture) **unchanged**.

- [ ] **Step 4: Run the new test and confirm GREEN; run the whole test file to ensure no regressions**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/... -run "TestStreamInterceptor" -count=1 -v
```
Expected: BOTH `TestStreamInterceptor` (the existing, spec-conforming case) and `TestStreamInterceptor_DataNoSpace` (the new no-space case) PASS. If the original test fails, the fix changed behaviour for the conforming form — revert and re-check.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/stream.go internal/proxy/stream_test.go
git commit -m "fix(proxy): tolerate SSE data: lines without trailing space (anthropic)

The HTML SSE spec (whatwg §9.2.6) defines the space after data: as
optional. Some upstreams (notably DashScope's /apps/anthropic BaiLian
app endpoint) emit data:{...} with no space, and our previous strict
6-byte prefix check silently dropped every such line — yielding 0
input / 0 output / 0 ttft on every streaming request through that
upstream, across 13 supported models.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: TDD for OpenAI chat-completions stream interceptor (`chatcompletions_stream.go`)

**Files:**
- Modify: `internal/proxy/chatcompletions_stream_test.go`
- Modify: `internal/proxy/chatcompletions_stream.go`

**Interfaces:**
- Consumes: existing `newChatCompletionsStreamInterceptor(inner, startTime, onComplete)` and `StreamMetrics` callback shape.
- Produces: `parseLine` accepts both forms (same fix as Task 1, different file).

- [ ] **Step 1: Add the failing regression test**

Open `/root/coding/modelserver/internal/proxy/chatcompletions_stream_test.go`. After the existing `TestChatCompletionsStreamInterceptor` function, add this new function (near-copy of the existing test with `data:` lines lacking the trailing space):

```go
// TestChatCompletionsStreamInterceptor_DataNoSpace covers SSE streams
// where `data:` lines omit the optional trailing space (per HTML SSE
// spec §9.2.6). Symmetric to TestStreamInterceptor_DataNoSpace in
// stream_test.go but for the OpenAI chat-completions protocol path.
func TestChatCompletionsStreamInterceptor_DataNoSpace(t *testing.T) {
	sseData := strings.Join([]string{
		`data:{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"google/gemini-2.5-flash","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		``,
		`data:{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"google/gemini-2.5-flash","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		``,
		`data:{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"google/gemini-2.5-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		``,
		`data:[DONE]`,
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
		t.Errorf("output differs from input (pass-through must be byte-exact)")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callback not invoked within 1s")
	}

	if gotMetrics.Model != "google/gemini-2.5-flash" {
		t.Errorf("Model = %q, want %q", gotMetrics.Model, "google/gemini-2.5-flash")
	}
	if gotMetrics.RespID != "chatcmpl-1" {
		t.Errorf("RespID = %q, want %q", gotMetrics.RespID, "chatcmpl-1")
	}
	if gotMetrics.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", gotMetrics.Usage.PromptTokens)
	}
	if gotMetrics.Usage.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %d, want 5", gotMetrics.Usage.CompletionTokens)
	}
	if gotMetrics.TTFT <= 0 {
		t.Errorf("TTFT = %d, want > 0 (content delta should set it)", gotMetrics.TTFT)
	}
}
```

⚠️ Field names on `StreamMetrics` (`Model`, `RespID`, `Usage.PromptTokens`, `Usage.CompletionTokens`, `TTFT`) are read from the existing `TestChatCompletionsStreamInterceptor` test. If a field name differs in the actual struct, adjust the test to match the existing test's assertions — do NOT change the struct. Run a quick grep first:

```bash
grep -n "gotMetrics\." /root/coding/modelserver/internal/proxy/chatcompletions_stream_test.go
```

Use the same accessor names you see there.

- [ ] **Step 2: Run the new test and confirm RED**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/... -run TestChatCompletionsStreamInterceptor_DataNoSpace -count=1 -v
```
Expected: FAIL. Metrics fields all zero/empty for the same reason as Task 1.

- [ ] **Step 3: Apply the fix in `chatcompletions_stream.go`**

Open `/root/coding/modelserver/internal/proxy/chatcompletions_stream.go`. Find `parseLine` (currently lines 79–106). Replace the prefix-check and `TrimPrefix` lines (79–85) with the SSE-spec-conformant version. The exact target today:

```go
func (si *chatCompletionsStreamInterceptor) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return
	}
	data := bytes.TrimPrefix(line, []byte("data: "))
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}
```

Change them to:

```go
func (si *chatCompletionsStreamInterceptor) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	// SSE spec (whatwg §9.2.6): a `data:` field's value is the bytes after
	// the colon, with EXACTLY ONE leading U+0020 SPACE removed if present.
	// See internal/proxy/stream.go's parseLine for the full rationale.
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := line[len("data:"):]
	if len(data) > 0 && data[0] == ' ' {
		data = data[1:]
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}
```

Leave the rest of `parseLine` (lines 89–106: `ParseChatCompletionsStreamEvent` call, model/respID/ttft/usage capture) **unchanged**.

- [ ] **Step 4: Run the new test and confirm GREEN; run the existing test too**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/... -run "TestChatCompletionsStreamInterceptor" -count=1 -v
```
Expected: BOTH `TestChatCompletionsStreamInterceptor` (existing) and `TestChatCompletionsStreamInterceptor_DataNoSpace` (new) PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/chatcompletions_stream.go internal/proxy/chatcompletions_stream_test.go
git commit -m "fix(proxy): tolerate SSE data: lines without trailing space (openai)

Same SSE spec conformance fix as the previous commit, applied to the
OpenAI chat-completions stream interceptor. Future OpenAI-compatible
upstreams that emit data:{...} without the optional space will now have
usage / TTFT captured correctly instead of recording zero.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Whole-project regression

**Files:** none modified — verification only.

- [ ] **Step 1: Build + full test run**

Run:
```bash
cd /root/coding/modelserver && go build ./... && go test ./... -count=1
```
Expected: build exits 0; every package either `ok` or `[no test files]`.

If anything fails, STOP and fix before moving on.

- [ ] **Step 2: Confirm the two commits land in order**

Run:
```bash
git log --oneline -3
```
Expected (top to bottom):
```
xxxxxxx fix(proxy): tolerate SSE data: lines without trailing space (openai)
xxxxxxx fix(proxy): tolerate SSE data: lines without trailing space (anthropic)
xxxxxxx docs(spec): SSE data: prefix strict-parsing bug + fix design
```

---

## Post-deployment verification (manual, by operator)

Not part of plan execution — listed for the deployer:

1. After deploy, repeat the live capture against codeapi.cs.ac.cn with `model=glm-5.2`, `stream=true`. The resulting row in the admin `/requests` view should now show non-zero `input_tokens`, `output_tokens`, and `ttft_ms`.
2. Sanity-check that other streaming traffic (claude-opus-4-8 via real Anthropic, claude-sonnet-4-6 via Vertex) still records usage correctly — same numbers as before the deploy.
3. Spot-check the dashscope upstream for one more model from each family (e.g., kimi-k2.5, qwen3-max, deepseek-v4-pro) — they should now all record usage.

---

## Self-review

- **Spec coverage**:
  - Spec §"File changes" item 1 (stream.go) → Task 1 Step 3 ✓
  - Spec §"File changes" item 2 (chatcompletions_stream.go) → Task 2 Step 3 ✓
  - Spec §"File changes" item 3 (stream_test.go regression test) → Task 1 Step 1 ✓
  - Spec §"File changes" item 4 (chatcompletions_stream_test.go regression test) → Task 2 Step 1 ✓
  - Spec §"Verification" steps 1–2 → Task 3 ✓
  - Spec §"Why strip exactly one space (not all leading whitespace)" — implemented as inline comment + code shape (single `if data[0] == ' ' { data = data[1:] }`, not `TrimLeft`). The Global Constraints section forbids `TrimLeft` explicitly. ✓
- **No placeholders / TBDs**.
- **Type consistency**: Tests use `anthropic.Usage` (Task 1) and `StreamMetrics` (Task 2) — both already imported by the files being modified.
- **TDD discipline**: each task writes failing test first, runs it to confirm RED, implements fix, runs it to confirm GREEN. Existing tests remain unmodified.
- **No restructuring**: confirmed — only two existing `parseLine` functions touched, no new files, no extracted helper.
