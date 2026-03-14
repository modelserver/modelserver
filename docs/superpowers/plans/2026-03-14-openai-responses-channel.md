# OpenAI Responses API Channel Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OpenAI Responses API proxy support with usage tracking and credit billing, transparent to existing infrastructure.

**Architecture:** New `/v1/responses` route handled by `HandleResponses` on the existing `Handler` struct. Requests are forwarded via `httputil.ReverseProxy` to OpenAI upstream. Response bodies (streaming and non-streaming) are intercepted to extract usage using the `openai-go` SDK's `responses.ResponseUsage` type, normalized to the internal 4-field model, then fed into the existing `ComputeCredits` and `store.CompleteRequest` pipeline.

**Tech Stack:** Go 1.26, chi router, `github.com/openai/openai-go/v3/responses` (types only), `httputil.ReverseProxy`

**Spec:** `docs/superpowers/specs/2026-03-14-openai-responses-channel-design.md`

---

## Chunk 1: SDK Dependency + Parsers

### Task 1: Add openai-go SDK dependency

**Files:**
- Modify: `go.mod`

- [ ] **Step 1: Add the openai-go v3 dependency**

Run:
```bash
cd /root/coding/modelserver && go get github.com/openai/openai-go/v3@latest
```

- [ ] **Step 2: Commit**

```bash
cd /root/coding/modelserver && git add go.mod go.sum && git commit -m "chore: add openai-go v3 SDK dependency"
```

---

### Task 2: OpenAI non-streaming response parser

**Files:**
- Create: `internal/proxy/openai_parser.go`
- Create: `internal/proxy/openai_parser_test.go`

- [ ] **Step 1: Write failing tests for non-streaming parser**

Create `internal/proxy/openai_parser_test.go`:

```go
package proxy

import (
	"testing"
)

func TestParseOpenAINonStreamingResponse(t *testing.T) {
	body := []byte(`{
		"id": "resp_abc123",
		"object": "response",
		"status": "completed",
		"model": "gpt-5.2",
		"usage": {
			"input_tokens": 120,
			"output_tokens": 50,
			"total_tokens": 170,
			"input_tokens_details": {
				"cached_tokens": 80
			},
			"output_tokens_details": {
				"reasoning_tokens": 0
			}
		},
		"output": []
	}`)

	model, respID, usage, err := ParseOpenAINonStreamingResponse(body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if model != "gpt-5.2" {
		t.Errorf("model = %q, want %q", model, "gpt-5.2")
	}
	if respID != "resp_abc123" {
		t.Errorf("respID = %q, want %q", respID, "resp_abc123")
	}
	if usage.InputTokens != 120 {
		t.Errorf("input_tokens = %d, want 120", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("output_tokens = %d, want 50", usage.OutputTokens)
	}
	if usage.InputTokensDetails.CachedTokens != 80 {
		t.Errorf("cached_tokens = %d, want 80", usage.InputTokensDetails.CachedTokens)
	}
}

func TestParseOpenAINonStreamingResponse_NoCachedTokens(t *testing.T) {
	body := []byte(`{
		"id": "resp_xyz",
		"model": "gpt-5.2",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 30,
			"total_tokens": 130,
			"input_tokens_details": {"cached_tokens": 0},
			"output_tokens_details": {"reasoning_tokens": 0}
		}
	}`)

	_, _, usage, err := ParseOpenAINonStreamingResponse(body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if usage.InputTokens != 100 {
		t.Errorf("input_tokens = %d, want 100", usage.InputTokens)
	}
	if usage.InputTokensDetails.CachedTokens != 0 {
		t.Errorf("cached_tokens = %d, want 0", usage.InputTokensDetails.CachedTokens)
	}
}

func TestParseOpenAINonStreamingResponse_InvalidJSON(t *testing.T) {
	_, _, _, err := ParseOpenAINonStreamingResponse([]byte(`{invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/ -run TestParseOpenAI -v
```

Expected: compilation error — `ParseOpenAINonStreamingResponse` undefined.

- [ ] **Step 3: Implement the non-streaming parser**

Create `internal/proxy/openai_parser.go`:

```go
package proxy

import (
	"encoding/json"

	"github.com/openai/openai-go/v3/responses"
)

type openaiResponseEnvelope struct {
	ID    string                  `json:"id"`
	Model string                  `json:"model"`
	Usage responses.ResponseUsage `json:"usage"`
}

// ParseOpenAINonStreamingResponse extracts model, response ID, and usage from a complete OpenAI Responses API response.
func ParseOpenAINonStreamingResponse(body []byte) (model, respID string, u responses.ResponseUsage, err error) {
	var resp openaiResponseEnvelope
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", responses.ResponseUsage{}, err
	}
	return resp.Model, resp.ID, resp.Usage, nil
}
```

The SDK's `ResponseUsage` has `json:"-"` on its internal metadata field, so standard `json.Unmarshal` works correctly for the data fields we need (`InputTokens`, `OutputTokens`, `InputTokensDetails.CachedTokens`).

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/ -run TestParseOpenAI -v
```

Expected: all 3 tests PASS. If the SDK's `ResponseUsage` doesn't unmarshal correctly with standard `json.Unmarshal` (e.g., if it has a custom unmarshaler that requires SDK internals), fall back to defining local structs mirroring the same fields and JSON tags.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver && git add internal/proxy/openai_parser.go internal/proxy/openai_parser_test.go && git commit -m "feat: add OpenAI non-streaming response parser using openai-go SDK types"
```

---

### Task 3: OpenAI streaming event parser

**Files:**
- Modify: `internal/proxy/openai_parser.go`
- Modify: `internal/proxy/openai_parser_test.go`

- [ ] **Step 1: Write failing tests for streaming parser**

Append to `internal/proxy/openai_parser_test.go`:

```go
func TestParseOpenAIStreamEvent_ResponseCreated(t *testing.T) {
	data := []byte(`{"type":"response.created","response":{"id":"resp_001","model":"gpt-5.2","status":"in_progress"}}`)

	eventType, model, respID, _, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "response.created" {
		t.Errorf("eventType = %q, want %q", eventType, "response.created")
	}
	if model != "gpt-5.2" {
		t.Errorf("model = %q, want %q", model, "gpt-5.2")
	}
	if respID != "resp_001" {
		t.Errorf("respID = %q, want %q", respID, "resp_001")
	}
	if hasUsage {
		t.Error("expected hasUsage = false for response.created")
	}
}

func TestParseOpenAIStreamEvent_OutputTextDelta(t *testing.T) {
	data := []byte(`{"type":"response.output_text.delta","delta":"Hello"}`)

	eventType, _, _, _, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "response.output_text.delta" {
		t.Errorf("eventType = %q", eventType)
	}
	if hasUsage {
		t.Error("expected hasUsage = false for delta")
	}
}

func TestParseOpenAIStreamEvent_ResponseCompleted(t *testing.T) {
	data := []byte(`{"type":"response.completed","response":{"id":"resp_001","model":"gpt-5.2","status":"completed","usage":{"input_tokens":120,"output_tokens":50,"total_tokens":170,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":0}}}}`)

	eventType, model, respID, usage, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "response.completed" {
		t.Errorf("eventType = %q", eventType)
	}
	if model != "gpt-5.2" {
		t.Errorf("model = %q", model)
	}
	if respID != "resp_001" {
		t.Errorf("respID = %q", respID)
	}
	if !hasUsage {
		t.Fatal("expected hasUsage = true")
	}
	if usage.InputTokens != 120 {
		t.Errorf("input_tokens = %d", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("output_tokens = %d", usage.OutputTokens)
	}
	if usage.InputTokensDetails.CachedTokens != 80 {
		t.Errorf("cached_tokens = %d", usage.InputTokensDetails.CachedTokens)
	}
}

func TestParseOpenAIStreamEvent_ResponseIncomplete(t *testing.T) {
	data := []byte(`{"type":"response.incomplete","response":{"id":"resp_002","model":"gpt-5.2","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`)

	eventType, _, _, usage, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "response.incomplete" {
		t.Errorf("eventType = %q", eventType)
	}
	if !hasUsage {
		t.Fatal("expected hasUsage = true for incomplete")
	}
	if usage.InputTokens != 100 {
		t.Errorf("input_tokens = %d", usage.InputTokens)
	}
}

func TestParseOpenAIStreamEvent_ResponseFailed(t *testing.T) {
	data := []byte(`{"type":"response.failed","response":{"id":"resp_003","model":"gpt-5.2","usage":{"input_tokens":50,"output_tokens":0,"total_tokens":50,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`)

	eventType, _, _, usage, hasUsage := ParseOpenAIStreamEvent(data)
	if eventType != "response.failed" {
		t.Errorf("eventType = %q", eventType)
	}
	if !hasUsage {
		t.Fatal("expected hasUsage = true for failed")
	}
	if usage.InputTokens != 50 {
		t.Errorf("input_tokens = %d", usage.InputTokens)
	}
}

func TestParseOpenAIStreamEvent_InvalidJSON(t *testing.T) {
	eventType, _, _, _, hasUsage := ParseOpenAIStreamEvent([]byte(`{invalid`))
	if eventType != "" {
		t.Errorf("expected empty eventType, got %q", eventType)
	}
	if hasUsage {
		t.Error("expected hasUsage = false for invalid JSON")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/ -run TestParseOpenAIStreamEvent -v
```

Expected: compilation error — `ParseOpenAIStreamEvent` undefined.

- [ ] **Step 3: Implement the streaming event parser**

Append to `internal/proxy/openai_parser.go`:

```go
type openaiStreamEventData struct {
	Type     string                 `json:"type"`
	Response openaiResponseEnvelope `json:"response"`
}

// ParseOpenAIStreamEvent extracts data from a single OpenAI Responses API SSE event payload.
// Returns usage for terminal events: response.completed, response.incomplete, response.failed.
func ParseOpenAIStreamEvent(data []byte) (eventType, model, respID string, u responses.ResponseUsage, hasUsage bool) {
	var evt openaiStreamEventData
	if err := json.Unmarshal(data, &evt); err != nil {
		return "", "", "", responses.ResponseUsage{}, false
	}

	eventType = evt.Type

	switch evt.Type {
	case "response.created":
		model = evt.Response.Model
		respID = evt.Response.ID
	case "response.completed", "response.incomplete", "response.failed":
		model = evt.Response.Model
		respID = evt.Response.ID
		u = evt.Response.Usage
		hasUsage = true
	}

	return eventType, model, respID, u, hasUsage
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/ -run TestParseOpenAIStreamEvent -v
```

Expected: all 6 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver && git add internal/proxy/openai_parser.go internal/proxy/openai_parser_test.go && git commit -m "feat: add OpenAI streaming event parser"
```

---

## Chunk 2: Stream Interceptor

### Task 4: OpenAI stream interceptor

**Files:**
- Create: `internal/proxy/openai_stream.go`
- Create: `internal/proxy/openai_stream_test.go`

- [ ] **Step 1: Write failing test for the stream interceptor**

Create `internal/proxy/openai_stream_test.go`:

```go
package proxy

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestOpenAIStreamInterceptor(t *testing.T) {
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_001","model":"gpt-5.2","status":"in_progress"}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":" world"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_001","model":"gpt-5.2","status":"completed","usage":{"input_tokens":120,"output_tokens":50,"total_tokens":170,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		"",
	}, "\n")

	var gotModel, gotRespID string
	var gotInput, gotOutput, gotCacheRead, gotTTFT int64
	done := make(chan struct{})

	interceptor := newOpenAIStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		"gpt-5.2", // model fallback
		func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64) {
			gotModel = model
			gotRespID = respID
			gotInput = inputTokens
			gotOutput = outputTokens
			gotCacheRead = cacheReadTokens
			gotTTFT = ttft
			close(done)
		},
	)

	output, err := io.ReadAll(interceptor)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	// Original data must pass through unchanged.
	if string(output) != sseData {
		t.Error("output differs from input")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete callback not called")
	}

	if gotModel != "gpt-5.2" {
		t.Errorf("model = %q, want %q", gotModel, "gpt-5.2")
	}
	if gotRespID != "resp_001" {
		t.Errorf("respID = %q, want %q", gotRespID, "resp_001")
	}
	// Normalized: 120 - 80 = 40
	if gotInput != 40 {
		t.Errorf("input_tokens = %d, want 40 (normalized: 120 - 80)", gotInput)
	}
	if gotOutput != 50 {
		t.Errorf("output_tokens = %d, want 50", gotOutput)
	}
	if gotCacheRead != 80 {
		t.Errorf("cache_read_tokens = %d, want 80", gotCacheRead)
	}
	if gotTTFT < 0 {
		t.Errorf("ttft = %d, want >= 0", gotTTFT)
	}
}

func TestOpenAIStreamInterceptor_Incomplete(t *testing.T) {
	sseData := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_002","model":"gpt-5.2"}}`,
		"",
		"event: response.incomplete",
		`data: {"type":"response.incomplete","response":{"id":"resp_002","model":"gpt-5.2","usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		"",
	}, "\n")

	var gotInput, gotOutput, gotCacheRead int64
	done := make(chan struct{})

	interceptor := newOpenAIStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		"gpt-5.2",
		func(_, _ string, inputTokens, outputTokens, cacheReadTokens, _ int64) {
			gotInput = inputTokens
			gotOutput = outputTokens
			gotCacheRead = cacheReadTokens
			close(done)
		},
	)

	io.ReadAll(interceptor)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete callback not called for incomplete")
	}

	if gotInput != 100 {
		t.Errorf("input_tokens = %d, want 100", gotInput)
	}
	if gotOutput != 20 {
		t.Errorf("output_tokens = %d, want 20", gotOutput)
	}
	if gotCacheRead != 0 {
		t.Errorf("cache_read_tokens = %d, want 0", gotCacheRead)
	}
}

func TestOpenAIStreamInterceptor_ModelFallback(t *testing.T) {
	// Stream without response.created — interceptor should use the fallback model.
	sseData := strings.Join([]string{
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_003","model":"","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		"",
	}, "\n")

	var gotModel string
	done := make(chan struct{})

	interceptor := newOpenAIStreamInterceptor(
		io.NopCloser(strings.NewReader(sseData)),
		time.Now(),
		"gpt-5.2-fallback",
		func(model, _ string, _, _, _, _ int64) {
			gotModel = model
			close(done)
		},
	)

	io.ReadAll(interceptor)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("onComplete callback not called")
	}

	if gotModel != "gpt-5.2-fallback" {
		t.Errorf("model = %q, want %q", gotModel, "gpt-5.2-fallback")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/ -run TestOpenAIStream -v
```

Expected: compilation error — `newOpenAIStreamInterceptor` undefined.

- [ ] **Step 3: Implement the OpenAI stream interceptor**

Create `internal/proxy/openai_stream.go`:

```go
package proxy

import (
	"bytes"
	"io"
	"sync"
	"time"

	"github.com/openai/openai-go/v3/responses"
)

// openaiStreamInterceptor wraps a response body, transparently passing through
// all bytes while parsing OpenAI Responses API SSE events to extract usage and TTFT.
type openaiStreamInterceptor struct {
	inner         io.ReadCloser
	buf           bytes.Buffer
	startTime     time.Time
	modelFallback string
	model         string
	respID        string
	usage         responses.ResponseUsage
	hasUsage      bool
	ttft          int64
	gotFirst      bool
	onComplete    func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64)
	once          sync.Once
}

func newOpenAIStreamInterceptor(
	inner io.ReadCloser,
	startTime time.Time,
	modelFallback string,
	onComplete func(model, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64),
) *openaiStreamInterceptor {
	return &openaiStreamInterceptor{
		inner:         inner,
		startTime:     startTime,
		modelFallback: modelFallback,
		onComplete:    onComplete,
	}
}

func (si *openaiStreamInterceptor) Read(p []byte) (int, error) {
	n, err := si.inner.Read(p)
	if n > 0 {
		si.buf.Write(p[:n])
		si.processLines()
	}
	if err == io.EOF {
		si.flushRemaining()
		si.finish()
	}
	return n, err
}

func (si *openaiStreamInterceptor) Close() error {
	si.flushRemaining()
	si.finish()
	return si.inner.Close()
}

func (si *openaiStreamInterceptor) processLines() {
	for {
		line, err := si.buf.ReadBytes('\n')
		if err != nil {
			si.buf.Write(line)
			return
		}
		si.parseLine(line)
	}
}

func (si *openaiStreamInterceptor) flushRemaining() {
	if si.buf.Len() > 0 {
		si.parseLine(si.buf.Bytes())
		si.buf.Reset()
	}
}

func (si *openaiStreamInterceptor) parseLine(line []byte) {
	line = bytes.TrimSpace(line)
	// Skip event: lines and empty lines — we get the type from the data JSON.
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return
	}
	data := bytes.TrimPrefix(line, []byte("data: "))
	if bytes.Equal(data, []byte("[DONE]")) {
		return
	}

	eventType, model, respID, usage, hasUsage := ParseOpenAIStreamEvent(data)
	if model != "" {
		si.model = model
	}
	if respID != "" {
		si.respID = respID
	}

	if !si.gotFirst && eventType == "response.output_text.delta" {
		si.gotFirst = true
		si.ttft = time.Since(si.startTime).Milliseconds()
	}

	if hasUsage {
		si.usage = usage
		si.hasUsage = true
	}
}

func (si *openaiStreamInterceptor) finish() {
	si.once.Do(func() {
		if si.onComplete == nil || !si.hasUsage {
			return
		}
		model := si.model
		if model == "" {
			model = si.modelFallback
		}
		// Normalize: OpenAI input_tokens includes cached, our model separates them.
		cachedTokens := si.usage.InputTokensDetails.CachedTokens
		inputTokens := si.usage.InputTokens - cachedTokens
		if inputTokens < 0 {
			inputTokens = 0
		}
		si.onComplete(model, si.respID, inputTokens, si.usage.OutputTokens, cachedTokens, si.ttft)
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/ -run TestOpenAIStream -v
```

Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver && git add internal/proxy/openai_stream.go internal/proxy/openai_stream_test.go && git commit -m "feat: add OpenAI SSE stream interceptor with usage normalization"
```

---

## Chunk 3: Handler + Route Wiring

### Task 5: OpenAI handler and director

**Files:**
- Create: `internal/proxy/openai_handler.go`
- Modify: `internal/proxy/router.go`

- [ ] **Step 1: Create the OpenAI handler**

Create `internal/proxy/openai_handler.go`:

```go
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// HandleResponses proxies OpenAI /v1/responses requests.
func (h *Handler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	apiKey := APIKeyFromContext(r.Context())
	project := ProjectFromContext(r.Context())
	if apiKey == nil || project == nil {
		writeProxyError(w, http.StatusInternalServerError, "missing auth context")
		return
	}

	clientIP := r.RemoteAddr

	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBodySize))
	if err != nil {
		writeProxyError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var reqShape struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	json.Unmarshal(bodyBytes, &reqShape)
	isStreaming := reqShape.Stream
	model := reqShape.Model

	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, model) {
		writeProxyError(w, http.StatusForbidden, "model not allowed for this API key")
		return
	}

	candidates := h.channelRouter.MatchChannels(project.ID, model)
	if len(candidates) == 0 {
		writeProxyError(w, http.StatusServiceUnavailable, "no channels available for model "+model)
		return
	}

	channel := h.channelRouter.SelectChannelForSession(candidates, TraceIDFromContext(r.Context()))
	if channel == nil {
		writeProxyError(w, http.StatusServiceUnavailable, "no channels available")
		return
	}

	if channel.Provider != types.ProviderOpenAI {
		writeProxyError(w, http.StatusServiceUnavailable, "selected channel is not an OpenAI provider")
		return
	}

	channelAPIKey := h.channelRouter.GetChannelKey(channel.ID)
	if channelAPIKey == "" {
		h.logger.Error("no decrypted key for channel", "channel_id", channel.ID)
		writeProxyError(w, http.StatusInternalServerError, "channel configuration error")
		return
	}

	policy := PolicyFromContext(r.Context())
	traceID := TraceIDFromContext(r.Context())
	threadID := ThreadIDFromContext(r.Context())

	logger := h.logger.With(
		"project_id", project.ID,
		"api_key_id", apiKey.ID,
		"channel_id", channel.ID,
		"model", model,
		"trace_id", traceID,
		"streaming", isStreaming,
	)

	if traceID != "" {
		source := TraceSourceFromContext(r.Context())
		if err := h.store.EnsureTrace(project.ID, traceID, threadID, source); err != nil {
			logger.Warn("failed to ensure trace", "error", err)
		}
	}

	pendingReq := &types.Request{
		ProjectID: project.ID,
		APIKeyID:  apiKey.ID,
		ChannelID: channel.ID,
		TraceID:   traceID,
		Provider:  channel.Provider,
		Model:     model,
		Streaming: isStreaming,
		Status:    types.RequestStatusProcessing,
		ClientIP:  clientIP,
	}
	if err := h.store.CreateRequest(pendingReq); err != nil {
		logger.Warn("failed to insert pending request", "error", err)
		pendingReq.ID = ""
	}

	startTime := time.Now()

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			directorSetOpenAIUpstream(req, channel.BaseURL, channelAPIKey)
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				duration := time.Since(startTime).Milliseconds()
				status := types.RequestStatusError
				if resp.StatusCode == http.StatusTooManyRequests {
					status = types.RequestStatusRateLimited
				}
				req := types.Request{
					ProjectID: project.ID,
					APIKeyID:  apiKey.ID,
					ChannelID: channel.ID,
					TraceID:   traceID,
					Provider:  channel.Provider,
					Model:     model,
					Streaming: isStreaming,
					Status:    status,
					LatencyMs: duration,
					ClientIP:  clientIP,
				}
				if pendingReq.ID != "" {
					go func() {
						if err := h.store.CompleteRequest(pendingReq.ID, &req); err != nil {
							logger.Error("failed to complete request", "request_id", pendingReq.ID, "error", err)
						}
					}()
				} else {
					h.collector.Record(req)
				}
				return nil
			}

			if isStreaming {
				return h.interceptOpenAIStreaming(resp, pendingReq.ID, project, apiKey, channel, model, traceID, policy, clientIP, startTime, logger)
			}
			return h.interceptOpenAINonStreaming(resp, pendingReq.ID, project, apiKey, channel, model, traceID, policy, clientIP, startTime, logger)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("proxy error", "error", err)
			if pendingReq.ID != "" {
				duration := time.Since(startTime).Milliseconds()
				req := types.Request{
					Status:       types.RequestStatusError,
					LatencyMs:    duration,
					ErrorMessage: err.Error(),
					ClientIP:     clientIP,
				}
				go func() {
					if err := h.store.CompleteRequest(pendingReq.ID, &req); err != nil {
						logger.Error("failed to complete request", "request_id", pendingReq.ID, "error", err)
					}
				}()
			}
			writeProxyError(w, http.StatusBadGateway, "upstream error")
		},
	}

	proxy.ServeHTTP(w, r)
}

func (h *Handler) interceptOpenAINonStreaming(resp *http.Response, requestID string, project *types.Project, apiKey *types.APIKey, channel *types.Channel, model, traceID string, policy *types.RateLimitPolicy, clientIP string, startTime time.Time, logger *slog.Logger) error {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		logger.Error("failed to read response body", "error", err)
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	parsedModel, msgID, usage, err := ParseOpenAINonStreamingResponse(body)
	if err != nil {
		logger.Warn("failed to parse response", "error", err)
		return nil
	}
	if parsedModel != "" {
		model = parsedModel
	}

	// Normalize: OpenAI input_tokens includes cached tokens.
	cachedTokens := usage.InputTokensDetails.CachedTokens
	inputTokens := usage.InputTokens - cachedTokens
	if inputTokens < 0 {
		inputTokens = 0
	}

	duration := time.Since(startTime).Milliseconds()

	var credits float64
	if policy != nil {
		credits = policy.ComputeCredits(model, inputTokens, usage.OutputTokens, 0, cachedTokens)
	}

	req := types.Request{
		ProjectID:           project.ID,
		APIKeyID:            apiKey.ID,
		ChannelID:           channel.ID,
		TraceID:             traceID,
		MsgID:               msgID,
		Provider:            channel.Provider,
		Model:               model,
		Streaming:           false,
		Status:              types.RequestStatusSuccess,
		InputTokens:         inputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: 0,
		CacheReadTokens:     cachedTokens,
		CreditsConsumed:     credits,
		LatencyMs:           duration,
		ClientIP:            clientIP,
	}
	if requestID != "" {
		go func() {
			if err := h.store.CompleteRequest(requestID, &req); err != nil {
				logger.Error("failed to complete request", "request_id", requestID, "error", err)
			}
		}()
	} else {
		h.collector.Record(req)
	}

	logger.Info("request completed",
		"msg_id", msgID,
		"status", types.RequestStatusSuccess,
		"streaming", false,
		"input_tokens", inputTokens,
		"output_tokens", usage.OutputTokens,
		"cache_read_tokens", cachedTokens,
		"credits", credits,
		"duration_ms", duration,
	)

	if h.rateLimiter != nil {
		h.rateLimiter.PostRecord(context.Background(), project.ID, apiKey.ID, model, types.TokenUsage{
			InputTokens:     inputTokens,
			OutputTokens:    usage.OutputTokens,
			CacheReadTokens: cachedTokens,
		})
	}

	return nil
}

func (h *Handler) interceptOpenAIStreaming(resp *http.Response, requestID string, project *types.Project, apiKey *types.APIKey, channel *types.Channel, model, traceID string, policy *types.RateLimitPolicy, clientIP string, startTime time.Time, logger *slog.Logger) error {
	resp.Body = newOpenAIStreamInterceptor(resp.Body, startTime, model, func(parsedModel, respID string, inputTokens, outputTokens, cacheReadTokens, ttft int64) {
		if parsedModel != "" {
			model = parsedModel
		}
		duration := time.Since(startTime).Milliseconds()

		var credits float64
		if policy != nil {
			credits = policy.ComputeCredits(model, inputTokens, outputTokens, 0, cacheReadTokens)
		}

		req := types.Request{
			ProjectID:           project.ID,
			APIKeyID:            apiKey.ID,
			ChannelID:           channel.ID,
			TraceID:             traceID,
			MsgID:               respID,
			Provider:            channel.Provider,
			Model:               model,
			Streaming:           true,
			Status:              types.RequestStatusSuccess,
			InputTokens:         inputTokens,
			OutputTokens:        outputTokens,
			CacheCreationTokens: 0,
			CacheReadTokens:     cacheReadTokens,
			CreditsConsumed:     credits,
			LatencyMs:           duration,
			TTFTMs:              ttft,
			ClientIP:            clientIP,
		}
		if requestID != "" {
			go func() {
				if err := h.store.CompleteRequest(requestID, &req); err != nil {
					logger.Error("failed to complete request", "request_id", requestID, "error", err)
				}
			}()
		} else {
			h.collector.Record(req)
		}

		logger.Info("request completed",
			"msg_id", respID,
			"status", types.RequestStatusSuccess,
			"streaming", true,
			"input_tokens", inputTokens,
			"output_tokens", outputTokens,
			"cache_read_tokens", cacheReadTokens,
			"credits", credits,
			"duration_ms", duration,
			"ttft_ms", ttft,
		)

		if h.rateLimiter != nil {
			h.rateLimiter.PostRecord(context.Background(), project.ID, apiKey.ID, model, types.TokenUsage{
				InputTokens:     inputTokens,
				OutputTokens:    outputTokens,
				CacheReadTokens: cacheReadTokens,
			})
		}
	})
	return nil
}

func directorSetOpenAIUpstream(req *http.Request, baseURL, apiKey string) {
	req.URL.Scheme = "https"
	if baseURL != "" {
		req.URL.Host = stripScheme(baseURL)
		if hasScheme(baseURL, "http") {
			req.URL.Scheme = "http"
		}
	}
	req.Host = req.URL.Host

	// Remove credentials and Anthropic-specific headers.
	req.Header.Del("x-api-key")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")

	// Remove Accept-Encoding so Go's Transport controls compression.
	// Same rationale as directorSetUpstream — prevents compressed SSE
	// that the stream interceptor cannot parse.
	req.Header.Del("Accept-Encoding")

	// Set OpenAI Bearer token auth.
	req.Header.Set("Authorization", "Bearer "+apiKey)
}
```

- [ ] **Step 2: Wire up the route in router.go**

In `internal/proxy/router.go`, add the new route after the existing `/messages/count_tokens` line:

Add `r.Post("/responses", handler.HandleResponses)` after line 24.

The result should be:

```go
r.Post("/messages", handler.HandleMessages)
r.Post("/messages/count_tokens", handler.HandleCountTokens)
r.Post("/responses", handler.HandleResponses)
r.Get("/models", handler.HandleListModels)
r.Get("/usage", handler.HandleUsage)
```

- [ ] **Step 3: Verify compilation**

Run:
```bash
cd /root/coding/modelserver && go build ./...
```

Expected: compiles without errors.

- [ ] **Step 4: Run all existing tests to ensure no regressions**

Run:
```bash
cd /root/coding/modelserver && go test ./internal/proxy/ -v
```

Expected: all tests pass (existing + new).

- [ ] **Step 5: Commit**

```bash
cd /root/coding/modelserver && git add internal/proxy/openai_handler.go internal/proxy/router.go && git commit -m "feat: add HandleResponses for OpenAI Responses API proxy"
```

---

### Task 6: OpenAI provider (optional, for Provider interface)

**Files:**
- Create: `internal/proxy/provider/openai.go`

Note: The `HandleResponses` handler uses its own `directorSetOpenAIUpstream` function directly (same pattern as the Anthropic handler using `directorSetUpstream`). This `provider/openai.go` file implements the `Provider` interface for consistency with the existing architecture, but is not called by the handler in this iteration.

- [ ] **Step 1: Create the OpenAI provider**

Create `internal/proxy/provider/openai.go`:

```go
package provider

import (
	"net/http"
	"net/url"
)

// OpenAI implements the Provider interface for the OpenAI API.
type OpenAI struct{}

func (o *OpenAI) Name() string { return "openai" }

func (o *OpenAI) Director(req *http.Request, baseURL, apiKey string) {
	target, err := url.Parse(baseURL)
	if err != nil {
		return
	}

	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.Host = target.Host

	req.Header.Del("x-api-key")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")
	req.Header.Del("Accept-Encoding")

	req.Header.Set("Authorization", "Bearer "+apiKey)
}
```

- [ ] **Step 2: Verify compilation**

Run:
```bash
cd /root/coding/modelserver && go build ./...
```

Expected: compiles without errors.

- [ ] **Step 3: Commit**

```bash
cd /root/coding/modelserver && git add internal/proxy/provider/openai.go && git commit -m "feat: add OpenAI Provider interface implementation"
```

---

## Chunk 4: Final Verification

### Task 7: Full test suite and cleanup

- [ ] **Step 1: Run the full test suite**

Run:
```bash
cd /root/coding/modelserver && go test ./... -v
```

Expected: all tests pass.

- [ ] **Step 2: Verify build is clean**

Run:
```bash
cd /root/coding/modelserver && go vet ./...
```

Expected: no issues.

- [ ] **Step 3: Review the file tree of new files**

Verify these files exist:
- `internal/proxy/openai_parser.go`
- `internal/proxy/openai_parser_test.go`
- `internal/proxy/openai_stream.go`
- `internal/proxy/openai_stream_test.go`
- `internal/proxy/openai_handler.go`
- `internal/proxy/provider/openai.go`

And these files were modified:
- `internal/proxy/router.go` (one line added)
- `go.mod` / `go.sum` (openai-go v3 dependency added)
