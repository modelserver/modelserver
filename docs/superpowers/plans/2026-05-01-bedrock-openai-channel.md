# Bedrock OpenAI Channel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `bedrock-openai` upstream provider that proxies OpenAI Chat Completions to Amazon Bedrock's `/openai/v1/chat/completions` endpoint, and rename the existing `bedrock` provider to `bedrock-anthropic` for symmetry with the `vertex-*` family.

**Architecture:** Mirror the per-provider double-file split (`vertex_openai.go` + `provider_vertex_openai.go`). The new provider's `BedrockOpenAITransformer` reuses the OpenAI Chat Completions stream interceptor and non-streaming parser. Bearer-token auth, no token manager, no AWS EventStream framing. Foundational rename is compiler-driven: the old `ProviderBedrock` constant is **deleted** so `go build` flags every missed call site.

**Tech Stack:** Go (proxy backend), React/TypeScript (dashboard), PostgreSQL (migration). Body parsing via `tidwall/gjson` + `tidwall/sjson`.

**Spec:** `docs/superpowers/specs/2026-05-01-bedrock-openai-channel-design.md`

---

## File Map

**Created:**
- `internal/proxy/bedrock_openai.go` — director (URL + headers wiring)
- `internal/proxy/bedrock_openai_test.go` — director tests
- `internal/proxy/provider_bedrock_openai.go` — `ProviderTransformer` impl
- `internal/proxy/provider_bedrock_openai_test.go` — transformer tests
- `internal/store/migrations/034_rename_bedrock_provider.sql` — DB rename

**Modified (Go):**
- `internal/types/upstream.go` — constant rename + addition
- `internal/proxy/provider_transform.go` — registry entries
- `internal/proxy/executor.go` — three call sites referencing the old constant
- `internal/proxy/lb/health_checker.go` — switch case + new probe builder
- `internal/proxy/lb/health_checker_test.go` — new probe test
- `internal/admin/handle_upstreams.go` — endpoint switch + header switch (two cases each)

**Modified (Dashboard):**
- `dashboard/src/api/types.ts` — provider union literal
- `dashboard/src/pages/admin/UpstreamsPage.tsx` — `<SelectItem>` rename + new entry
- `dashboard/src/pages/admin/RoutesPage.tsx` — `subsetOf` provider list rename

---

## Phase 1 — Foundational rename + new constant

The single rename commit must compile. The Go compiler enforces that every call site is updated; deleting the old constant is the safety net.

### Task 1: Rename `ProviderBedrock` → `ProviderBedrockAnthropic` and add `ProviderBedrockOpenAI`

**Files:**
- Modify: `internal/types/upstream.go:7-16`

- [ ] **Step 1: Edit constants block**

In `internal/types/upstream.go`, replace the existing `ProviderBedrock` line with two constants. Final block:

```go
const (
    ProviderAnthropic        = "anthropic"
    ProviderOpenAI           = "openai"
    ProviderGemini           = "gemini"
    ProviderBedrockAnthropic = "bedrock-anthropic"
    ProviderBedrockOpenAI    = "bedrock-openai"
    ProviderClaudeCode       = "claudecode"
    ProviderVertexAnthropic  = "vertex-anthropic"
    ProviderVertexGoogle     = "vertex-google"
    ProviderVertexOpenAI     = "vertex-openai"
    ProviderCodex            = "codex"
)
```

- [ ] **Step 2: Verify the rename breaks the build (intentional)**

Run: `go build ./...`
Expected: Compile errors in `internal/proxy/provider_transform.go`, `internal/proxy/executor.go`, `internal/admin/handle_upstreams.go`. These are the call sites Task 2 fixes.

### Task 2: Fix Go call sites of the renamed constant

**Files:**
- Modify: `internal/proxy/provider_transform.go:59`
- Modify: `internal/proxy/executor.go:294,357,868`
- Modify: `internal/admin/handle_upstreams.go:284,414`

- [ ] **Step 1: Update `provider_transform.go`**

Change line 59 from:

```go
providerTransformers[types.ProviderBedrock] = &BedrockTransformer{}
```

to:

```go
providerTransformers[types.ProviderBedrockAnthropic] = &BedrockTransformer{}
```

- [ ] **Step 2: Update `executor.go`**

Change three references. Line 294 (the body-rewrite exclusion list):

```go
if !isImagesEditMultipart && actualModel != reqCtx.Model && upstream.Provider != types.ProviderBedrockAnthropic && upstream.Provider != types.ProviderVertexAnthropic && upstream.Provider != types.ProviderGemini && upstream.Provider != types.ProviderVertexGoogle {
```

Line 357 (Bedrock context-injection guard):

```go
if upstream.Provider == types.ProviderBedrockAnthropic {
    outReq = withBedrockParams(outReq, actualModel, reqCtx.IsStream)
}
```

Line 868 (Bedrock-specific Content-Type override in `commitStreamingResponse`):

```go
if candidate.Upstream.Provider == types.ProviderBedrockAnthropic {
    w.Header().Set("Content-Type", "text/event-stream")
}
```

Note: `bedrock-openai` is intentionally NOT added to any of these — it needs the model in the body, has no context params, and its upstream returns native SSE.

- [ ] **Step 3: Update `handle_upstreams.go`**

Both `case types.ProviderBedrock:` arms (one in the endpoint-construction switch around line 284, one in the auth-header switch around line 414) become `case types.ProviderBedrockAnthropic:`.

- [ ] **Step 4: Verify build is green**

Run: `go build ./...`
Expected: No errors.

- [ ] **Step 5: Run existing Go tests**

Run: `go test ./internal/...`
Expected: All pass. (Some tests reference `ProviderBedrock`; they should already use the constant via Go imports — fix any string literal `"bedrock"` in tests by searching.)

If any test references the literal string `"bedrock"` for the provider field, replace with `"bedrock-anthropic"`. Search:

Run: `grep -rn '"bedrock"' internal/ --include="*.go"`
Expected: No matches (the rename touches all of them via the constant).

### Task 3: Rename the health-checker switch case

**Files:**
- Modify: `internal/proxy/lb/health_checker.go:259`

- [ ] **Step 1: Update the switch case**

Change line 259 from:

```go
case "bedrock":
    return hc.buildBedrockProbe(entry)
```

to:

```go
case "bedrock-anthropic":
    return hc.buildBedrockProbe(entry)
```

- [ ] **Step 2: Run health checker tests**

Run: `go test ./internal/proxy/lb/...`
Expected: All pass. If any test sets `provider: "bedrock"` on an entry, change to `"bedrock-anthropic"`.

### Task 4: Update dashboard `bedrock` references

**Files:**
- Modify: `dashboard/src/api/types.ts:379`
- Modify: `dashboard/src/pages/admin/UpstreamsPage.tsx:794`
- Modify: `dashboard/src/pages/admin/RoutesPage.tsx:108`

- [ ] **Step 1: Update the provider union in `types.ts`**

Change the literal union on line 379 from:

```ts
provider: "anthropic" | "openai" | "gemini" | "bedrock" | "claudecode" | "codex" | "vertex-anthropic" | "vertex-google" | "vertex-openai";
```

to:

```ts
provider: "anthropic" | "openai" | "gemini" | "bedrock-anthropic" | "bedrock-openai" | "claudecode" | "codex" | "vertex-anthropic" | "vertex-google" | "vertex-openai";
```

- [ ] **Step 2: Update the `<SelectItem>` in `UpstreamsPage.tsx`**

Change line 794 from:

```tsx
<SelectItem value="bedrock">AWS Bedrock</SelectItem>
```

to:

```tsx
<SelectItem value="bedrock-anthropic">AWS Bedrock (Anthropic)</SelectItem>
```

(Task 11 inserts the `bedrock-openai` entry beside it.)

- [ ] **Step 3: Update the `subsetOf` allow-list in `RoutesPage.tsx`**

Change line 108 from:

```ts
} else if (subsetOf(["anthropic", "claudecode", "bedrock", "vertex-anthropic"])) {
```

to:

```ts
} else if (subsetOf(["anthropic", "claudecode", "bedrock-anthropic", "vertex-anthropic"])) {
```

- [ ] **Step 4: Verify dashboard typechecks**

Run: `cd dashboard && npm run typecheck` (or `pnpm typecheck` — use whatever the project uses; check `package.json` `scripts` if unsure)
Expected: No errors.

### Task 5: Add DB migration to rename existing rows

**Files:**
- Create: `internal/store/migrations/034_rename_bedrock_provider.sql`

- [ ] **Step 1: Create the migration file**

```sql
-- Rename "bedrock" provider to "bedrock-anthropic" so the existing channel
-- can coexist with the new "bedrock-openai" provider added in this release.
-- Mirrors migration 012 (vertex → vertex-anthropic) but extends the rename
-- to the requests table so historical analytics rows do not split across
-- two values for the same channel.
UPDATE upstreams SET provider = 'bedrock-anthropic' WHERE provider = 'bedrock';
UPDATE requests  SET provider = 'bedrock-anthropic' WHERE provider = 'bedrock';
```

- [ ] **Step 2: Verify the file is sequenced correctly**

Run: `ls internal/store/migrations/ | sort | tail -5`
Expected: `033_deepseek_v4.sql` followed by `034_rename_bedrock_provider.sql` as the highest-numbered file.

### Task 6: Commit Phase 1

- [ ] **Step 1: Stage and commit**

```bash
git add internal/types/upstream.go internal/proxy/provider_transform.go internal/proxy/executor.go internal/admin/handle_upstreams.go internal/proxy/lb/health_checker.go dashboard/src/api/types.ts dashboard/src/pages/admin/UpstreamsPage.tsx dashboard/src/pages/admin/RoutesPage.tsx internal/store/migrations/034_rename_bedrock_provider.sql
git commit -m "refactor(provider): rename bedrock to bedrock-anthropic"
```

---

## Phase 2 — New `BedrockOpenAITransformer` (TDD)

### Task 7: Write director tests (failing)

**Files:**
- Create: `internal/proxy/bedrock_openai_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package proxy

import (
    "net/http"
    "testing"
)

func TestDirectorSetBedrockOpenAIUpstream(t *testing.T) {
    req := mustNewRequest(t, "POST", "http://localhost/v1/chat/completions", nil)
    req.Header.Set("x-api-key", "client-key")
    req.Header.Set("anthropic-version", "2023-06-01")
    req.Header.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
    req.Header.Set("x-goog-api-key", "stale")

    directorSetBedrockOpenAIUpstream(req,
        "https://bedrock-runtime.us-west-2.amazonaws.com",
        "aws-bearer-token",
    )

    if req.URL.Scheme != "https" {
        t.Errorf("scheme = %s, want https", req.URL.Scheme)
    }
    if req.URL.Host != "bedrock-runtime.us-west-2.amazonaws.com" {
        t.Errorf("host = %s", req.URL.Host)
    }
    if req.URL.Path != "/openai/v1/chat/completions" {
        t.Errorf("path = %s, want /openai/v1/chat/completions", req.URL.Path)
    }
    if req.Host != "bedrock-runtime.us-west-2.amazonaws.com" {
        t.Errorf("Host header = %s", req.Host)
    }
    if got := req.Header.Get("Authorization"); got != "Bearer aws-bearer-token" {
        t.Errorf("Authorization = %s, want Bearer aws-bearer-token", got)
    }
    for _, h := range []string{"x-api-key", "anthropic-version", "anthropic-beta", "x-goog-api-key"} {
        if v := req.Header.Get(h); v != "" {
            t.Errorf("%s should be removed, got %q", h, v)
        }
    }
}

func TestDirectorSetBedrockOpenAIUpstream_TrailingSlash(t *testing.T) {
    req := mustNewRequest(t, "POST", "http://localhost/v1/chat/completions", nil)

    directorSetBedrockOpenAIUpstream(req,
        "https://bedrock-runtime.us-east-1.amazonaws.com/",
        "tok",
    )

    if req.URL.Path != "/openai/v1/chat/completions" {
        t.Errorf("path = %s, want /openai/v1/chat/completions", req.URL.Path)
    }
    if req.URL.Host != "bedrock-runtime.us-east-1.amazonaws.com" {
        t.Errorf("host = %s", req.URL.Host)
    }
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/proxy/ -run TestDirectorSetBedrockOpenAIUpstream`
Expected: Compile error — `undefined: directorSetBedrockOpenAIUpstream`.

### Task 8: Implement the director

**Files:**
- Create: `internal/proxy/bedrock_openai.go`

- [ ] **Step 1: Write the minimal implementation**

```go
package proxy

import (
    "net/http"
    "net/url"
    "strings"
)

const bedrockOpenAIPath = "/openai/v1/chat/completions"

// directorSetBedrockOpenAIUpstream configures the outbound request for an Amazon
// Bedrock OpenAI-compatible upstream. The base URL should point to the Bedrock
// Runtime regional endpoint (e.g. https://bedrock-runtime.us-west-2.amazonaws.com).
// The /openai/v1/chat/completions path is appended automatically.
func directorSetBedrockOpenAIUpstream(req *http.Request, baseURL, apiKey string) {
    endpoint := strings.TrimRight(baseURL, "/") + bedrockOpenAIPath
    target, err := url.Parse(endpoint)
    if err != nil {
        req.Header.Set("Authorization", "Bearer "+apiKey)
        return
    }

    req.URL.Scheme = target.Scheme
    req.URL.Host = target.Host
    req.URL.Path = target.Path
    req.URL.RawPath = target.RawPath
    req.Host = target.Host

    req.Header.Set("Authorization", "Bearer "+apiKey)

    // Strip headers from other providers so they cannot leak upstream.
    req.Header.Del("x-api-key")
    req.Header.Del("anthropic-version")
    req.Header.Del("anthropic-beta")
    req.Header.Del("x-goog-api-key")
}
```

- [ ] **Step 2: Run the director tests and watch them pass**

Run: `go test ./internal/proxy/ -run TestDirectorSetBedrockOpenAIUpstream`
Expected: PASS.

### Task 9: Write transformer body-transform tests (failing)

**Files:**
- Create: `internal/proxy/provider_bedrock_openai_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package proxy

import (
    "net/http"
    "testing"

    "github.com/tidwall/gjson"
)

func TestBedrockOpenAITransformerTransformBody_InjectsStreamOptionsForStreaming(t *testing.T) {
    transformer := &BedrockOpenAITransformer{}
    input := []byte(`{"model":"openai.gpt-oss-20b-1:0","messages":[{"role":"user","content":"hi"}]}`)
    output, err := transformer.TransformBody(input, "openai.gpt-oss-20b-1:0", true, http.Header{})
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if !gjson.GetBytes(output, "stream_options.include_usage").Bool() {
        t.Errorf("stream_options.include_usage should be true, got %s", output)
    }
}

func TestBedrockOpenAITransformerTransformBody_NoOpForNonStreaming(t *testing.T) {
    transformer := &BedrockOpenAITransformer{}
    input := []byte(`{"model":"openai.gpt-oss-20b-1:0","messages":[{"role":"user","content":"hi"}]}`)
    output, err := transformer.TransformBody(input, "openai.gpt-oss-20b-1:0", false, http.Header{})
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if string(output) != string(input) {
        t.Errorf("non-streaming TransformBody should be no-op:\nin:  %s\nout: %s", input, output)
    }
}

func TestBedrockOpenAITransformerTransformBody_PreservesExistingStreamOptions(t *testing.T) {
    transformer := &BedrockOpenAITransformer{}
    input := []byte(`{"model":"openai.gpt-oss-20b-1:0","stream_options":{"include_usage":true,"foo":"bar"},"messages":[{"role":"user","content":"hi"}]}`)
    output, err := transformer.TransformBody(input, "openai.gpt-oss-20b-1:0", true, http.Header{})
    if err != nil {
        t.Fatalf("error = %v", err)
    }
    if gjson.GetBytes(output, "stream_options.foo").String() != "bar" {
        t.Errorf("existing stream_options.foo should be preserved, got %s", output)
    }
    if !gjson.GetBytes(output, "stream_options.include_usage").Bool() {
        t.Errorf("stream_options.include_usage should remain true")
    }
}
```

- [ ] **Step 2: Run the tests and watch them fail**

Run: `go test ./internal/proxy/ -run TestBedrockOpenAITransformer`
Expected: Compile error — `undefined: BedrockOpenAITransformer`.

### Task 10: Implement the transformer

**Files:**
- Create: `internal/proxy/provider_bedrock_openai.go`

- [ ] **Step 1: Write the implementation**

```go
package proxy

import (
    "io"
    "net/http"
    "time"

    "github.com/modelserver/modelserver/internal/types"
    "github.com/tidwall/gjson"
    "github.com/tidwall/sjson"
)

// BedrockOpenAITransformer handles Amazon Bedrock's OpenAI-compatible Chat
// Completions endpoint at /openai/v1/chat/completions. Auth is a static
// Bearer token (the Bedrock API key); body and response are standard OpenAI
// Chat Completions JSON, so the existing chatcompletions parsers apply.
type BedrockOpenAITransformer struct{}

var _ ProviderTransformer = (*BedrockOpenAITransformer)(nil)

// TransformBody ensures stream_options.include_usage is set for streaming
// requests so the upstream emits a final usage event for token accounting.
func (t *BedrockOpenAITransformer) TransformBody(body []byte, _ string, isStream bool, _ http.Header) ([]byte, error) {
    if isStream && !gjson.GetBytes(body, "stream_options.include_usage").Bool() {
        body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)
    }
    return body, nil
}

// SetUpstream configures the outbound request URL and Bearer auth header.
func (t *BedrockOpenAITransformer) SetUpstream(r *http.Request, upstream *types.Upstream, apiKey string) error {
    directorSetBedrockOpenAIUpstream(r, upstream.BaseURL, apiKey)
    return nil
}

// WrapStream reuses the OpenAI Chat Completions SSE stream interceptor.
func (t *BedrockOpenAITransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
    return newChatCompletionsStreamInterceptor(body, startTime, onComplete)
}

// ParseResponse reuses the OpenAI Chat Completions non-streaming parser.
func (t *BedrockOpenAITransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
    return ParseChatCompletionsResponse(body)
}
```

- [ ] **Step 2: Run the transformer tests and watch them pass**

Run: `go test ./internal/proxy/ -run TestBedrockOpenAITransformer`
Expected: PASS (all three).

### Task 11: Register the transformer

**Files:**
- Modify: `internal/proxy/provider_transform.go:57-67`

- [ ] **Step 1: Add the registration**

After the line registering `ProviderBedrockAnthropic` (currently at line 59 after Phase 1), add:

```go
providerTransformers[types.ProviderBedrockOpenAI] = &BedrockOpenAITransformer{}
```

The init function should now contain (order does not matter functionally):

```go
func init() {
    providerTransformers[types.ProviderAnthropic] = &AnthropicTransformer{}
    providerTransformers[types.ProviderBedrockAnthropic] = &BedrockTransformer{}
    providerTransformers[types.ProviderBedrockOpenAI] = &BedrockOpenAITransformer{}
    providerTransformers[types.ProviderOpenAI] = &OpenAITransformer{}
    providerTransformers[types.ProviderClaudeCode] = &ClaudeCodeTransformer{}
    providerTransformers[types.ProviderVertexAnthropic] = &VertexAnthropicTransformer{}
    providerTransformers[types.ProviderVertexGoogle] = &VertexGoogleTransformer{}
    providerTransformers[types.ProviderGemini] = &GeminiTransformer{}
    providerTransformers[types.ProviderVertexOpenAI] = &VertexOpenAITransformer{}
    providerTransformers[types.ProviderCodex] = &CodexTransformer{}
}
```

- [ ] **Step 2: Verify build is green**

Run: `go build ./...`
Expected: No errors.

### Task 12: Commit Phase 2

- [ ] **Step 1: Stage and commit**

```bash
git add internal/proxy/bedrock_openai.go internal/proxy/bedrock_openai_test.go internal/proxy/provider_bedrock_openai.go internal/proxy/provider_bedrock_openai_test.go internal/proxy/provider_transform.go
git commit -m "feat(provider): add bedrock-openai transformer"
```

---

## Phase 3 — Health probe (TDD)

### Task 13: Write health probe test (failing)

**Files:**
- Modify: `internal/proxy/lb/health_checker_test.go` (append)

- [ ] **Step 1: Look at existing probe-test patterns**

Run: `grep -n "buildVertexOpenAIProbe\|TestBuildVertex" internal/proxy/lb/health_checker_test.go`

Use the existing `TestBuild...Probe` style as the template (the file is the canonical place for these tests; if there isn't one for vertex-openai yet, fall back to inspecting the production code in `health_checker.go:471`).

- [ ] **Step 2: Append the failing test**

```go
func TestBuildBedrockOpenAIProbe(t *testing.T) {
    hc := &HealthChecker{}
    entry := &healthEntry{
        upstreamID: "u-1",
        baseURL:    "https://bedrock-runtime.us-west-2.amazonaws.com",
        apiKey:     "tok",
        testModel:  "openai.gpt-oss-20b-1:0",
    }

    req, err := hc.buildBedrockOpenAIProbe(entry)
    if err != nil {
        t.Fatalf("buildBedrockOpenAIProbe: %v", err)
    }

    wantURL := "https://bedrock-runtime.us-west-2.amazonaws.com/openai/v1/chat/completions"
    if req.URL.String() != wantURL {
        t.Errorf("URL = %s, want %s", req.URL.String(), wantURL)
    }
    if got := req.Header.Get("Authorization"); got != "Bearer tok" {
        t.Errorf("Authorization = %s, want Bearer tok", got)
    }
    if got := req.Header.Get("Content-Type"); got != "application/json" {
        t.Errorf("Content-Type = %s, want application/json", got)
    }
}
```

- [ ] **Step 3: Run the test and watch it fail**

Run: `go test ./internal/proxy/lb/ -run TestBuildBedrockOpenAIProbe`
Expected: Compile error — `undefined: (*HealthChecker).buildBedrockOpenAIProbe`.

### Task 14: Implement the probe + register the case

**Files:**
- Modify: `internal/proxy/lb/health_checker.go` (switch + new builder)

- [ ] **Step 1: Add the switch case**

In the `buildProbeRequest` switch (around line 250), insert after `case "bedrock-anthropic"`:

```go
case "bedrock-openai":
    return hc.buildBedrockOpenAIProbe(entry)
```

- [ ] **Step 2: Append the new builder**

Add after `buildVertexOpenAIProbe` at the end of the builder section:

```go
func (hc *HealthChecker) buildBedrockOpenAIProbe(entry *healthEntry) (*http.Request, error) {
    body := map[string]interface{}{
        "model":      entry.testModel,
        "max_tokens": 1,
        "messages": []map[string]string{
            {"role": "user", "content": "hi"},
        },
    }
    data, err := json.Marshal(body)
    if err != nil {
        return nil, fmt.Errorf("marshal probe body: %w", err)
    }

    base := strings.TrimRight(entry.baseURL, "/")
    url := base + "/openai/v1/chat/completions"

    req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
    if err != nil {
        return nil, fmt.Errorf("create request: %w", err)
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", "Bearer "+entry.apiKey)
    return req, nil
}
```

- [ ] **Step 3: Run the probe test and watch it pass**

Run: `go test ./internal/proxy/lb/ -run TestBuildBedrockOpenAIProbe`
Expected: PASS.

- [ ] **Step 4: Run the full lb test suite to catch regressions**

Run: `go test ./internal/proxy/lb/...`
Expected: All pass.

### Task 15: Commit Phase 3

- [ ] **Step 1: Stage and commit**

```bash
git add internal/proxy/lb/health_checker.go internal/proxy/lb/health_checker_test.go
git commit -m "feat(health): add bedrock-openai probe"
```

---

## Phase 4 — Admin connectivity test

### Task 16: Add `bedrock-openai` cases to admin test handler

**Files:**
- Modify: `internal/admin/handle_upstreams.go` (around lines 280–460)

- [ ] **Step 1: Add endpoint+body case**

In the first switch (the one that builds `endpoint` + `reqBody` per provider, around line 280), add a new case after `ProviderBedrockAnthropic`:

```go
case types.ProviderBedrockOpenAI:
    base := baseURL
    if len(base) > 0 && base[len(base)-1] == '/' {
        base = base[:len(base)-1]
    }
    endpoint = base + "/openai/v1/chat/completions"
    reqBody, _ = json.Marshal(map[string]interface{}{
        "model":      upstreamTestModel,
        "max_tokens": 10,
        "messages":   []map[string]string{{"role": "user", "content": "Hi"}},
    })
```

- [ ] **Step 2: Add header case**

In the second switch (the one that sets auth headers, around line 411), add after the `ProviderBedrockAnthropic` case:

```go
case types.ProviderBedrockOpenAI:
    req.Header.Set("Authorization", "Bearer "+string(apiKey))
```

- [ ] **Step 3: Verify build is green**

Run: `go build ./...`
Expected: No errors.

- [ ] **Step 4: Run admin tests if any exist**

Run: `go test ./internal/admin/...`
Expected: All pass.

### Task 17: Commit Phase 4

- [ ] **Step 1: Stage and commit**

```bash
git add internal/admin/handle_upstreams.go
git commit -m "feat(admin): add bedrock-openai connectivity test"
```

---

## Phase 5 — Dashboard new option

### Task 18: Add the new dropdown entry and provider union

**Files:**
- Modify: `dashboard/src/api/types.ts` (already touched in Task 4)
- Modify: `dashboard/src/pages/admin/UpstreamsPage.tsx` (around line 794)

- [ ] **Step 1: Confirm Task 4 already added the union literal**

Open `dashboard/src/api/types.ts:379`. The provider union should already include both `"bedrock-anthropic"` and `"bedrock-openai"` (Task 4, Step 1).

If the literal `"bedrock-openai"` is missing, add it.

- [ ] **Step 2: Insert the new `<SelectItem>` next to the renamed one**

In `dashboard/src/pages/admin/UpstreamsPage.tsx`, immediately after:

```tsx
<SelectItem value="bedrock-anthropic">AWS Bedrock (Anthropic)</SelectItem>
```

add:

```tsx
<SelectItem value="bedrock-openai">AWS Bedrock (OpenAI)</SelectItem>
```

- [ ] **Step 3: Check whether the form needs a per-provider branch**

Run: `grep -n 'form.provider ===' dashboard/src/pages/admin/UpstreamsPage.tsx`

For each branch found, check whether `bedrock-openai` should follow the same path as `openai` / `bedrock-anthropic` (plain `base_url` + `api_key` text inputs) or `vertex-openai` (service-account JSON). For `bedrock-openai`, the inputs are `base_url` + `api_key` (Bearer token), same as `openai` / `bedrock-anthropic`. If a branch exclusively gates on `form.provider === "bedrock-anthropic"`, decide whether `bedrock-openai` belongs alongside it (e.g. the same model-list editor). Apply the change only if the existing condition would otherwise hide a needed input.

- [ ] **Step 4: Verify dashboard typechecks and builds**

Run: `cd dashboard && npm run typecheck && npm run build`
Expected: No errors.

### Task 19: Commit Phase 5

- [ ] **Step 1: Stage and commit**

```bash
git add dashboard/
git commit -m "feat(dashboard): add bedrock-openai upstream option"
```

---

## Phase 6 — End-to-end verification

### Task 20: Full backend test sweep + race detector

**Files:** none

- [ ] **Step 1: Run the full Go test suite with the race detector**

Run: `go test -race ./...`
Expected: All packages PASS.

- [ ] **Step 2: Run vet**

Run: `go vet ./...`
Expected: No warnings.

### Task 21: Manual smoke check (optional but recommended)

**Files:** none

- [ ] **Step 1: Start the modelserver locally with a dev DB that has migration 034 applied**

Bring up the local stack however the project's tooling does (see top-level README or `docker-compose.yml`). Confirm the server boots without complaints about unknown providers in `internal/store/migrations`.

- [ ] **Step 2: Create a `bedrock-openai` upstream via the dashboard**

Open the admin Upstreams page, click "Create", and verify:
- The provider dropdown shows both "AWS Bedrock (Anthropic)" and "AWS Bedrock (OpenAI)".
- Selecting "AWS Bedrock (OpenAI)" shows the standard `base_url` + `api_key` inputs.
- Saving with `https://bedrock-runtime.us-west-2.amazonaws.com` + an AWS Bedrock API key + `openai.gpt-oss-20b-1:0` as the test model and clicking "Test" returns success (HTTP 200 from Bedrock).

- [ ] **Step 3: Issue a request through the proxy**

Route `openai_chat_completions` for `openai.gpt-oss-20b-1:0` to a group containing the new upstream and POST a Chat Completions request through the modelserver gateway. Confirm:
- A 2xx response with the expected OpenAI shape.
- A row appears in the `requests` table with `provider='bedrock-openai'`, populated `input_tokens` / `output_tokens`, and `streaming` set correctly.
- For `stream:true` requests, the final SSE chunk includes a `usage` object (proves `stream_options.include_usage` injection worked).

---

## Self-Review

**Spec coverage** — every section of the spec maps to tasks:
- §3 user config → Tasks 4, 18 (dashboard inputs + dropdown)
- §4.1 constants → Task 1
- §4.2 transformer files → Tasks 7–10
- §4.3 registration → Task 11
- §4.4 executor adjustments → Task 2
- §4.5 health check → Tasks 13–14
- §4.6 admin connectivity test → Task 16
- §4.7 dashboard → Tasks 4, 18
- §4.8 DB migration → Task 5
- §6 error handling (no new code) → covered by Task 20 sweep
- §7 testing matrix → Tasks 7, 9, 13 (each test case in the spec is a step)
- §8 rollout → migration is Task 5; deployment ordering is documented but not codified

**Placeholder scan** — no TBD/TODO/"appropriate"/"similar to". Task 18 Step 3 contains a conditional ("only if the existing condition would otherwise hide a needed input") — this is intentional because the dashboard form's structure isn't fully visible in the spec scope; the engineer is told exactly when to act and when not to.

**Type/identifier consistency** —
- `ProviderBedrockAnthropic`, `ProviderBedrockOpenAI` used identically across Tasks 1, 2, 11, 16.
- `BedrockOpenAITransformer` defined in Task 10, used in Task 11.
- `directorSetBedrockOpenAIUpstream` defined in Task 8, used in Task 10.
- `buildBedrockOpenAIProbe` defined in Task 14, used in same task's switch + tested in Task 13.
- `bedrockOpenAIPath = "/openai/v1/chat/completions"` defined in Task 8 and matches the URL used in Tasks 13–14, 16, 18.
