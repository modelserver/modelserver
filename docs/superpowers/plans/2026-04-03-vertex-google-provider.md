# Vertex Google Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `vertex-google` provider type that routes Gemini-native-format requests to Vertex AI's `generateContent` / `streamGenerateContent` endpoints using OAuth2 service account authentication.

**Architecture:** New `ProviderVertexGoogle` provider mirrors `ProviderVertex` for auth (shared `VertexTokenManager`) but uses Gemini request/response format (reusing `GeminiTransformer`'s stream interceptor and response parser). URL pattern is `{baseURL}/{model}:generateContent` (vs `rawPredict` for Anthropic).

**Tech Stack:** Go, existing `VertexTokenManager`, existing Gemini SSE parser, React dashboard.

**Spec:** `docs/superpowers/specs/2026-04-03-vertex-google-provider-design.md`

---

### Task 1: Add Provider Constant

**Files:**
- Modify: `internal/types/upstream.go:6-13`

- [ ] **Step 1: Add the constant**

In `internal/types/upstream.go`, add `ProviderVertexGoogle` to the provider constants block:

```go
const (
	ProviderAnthropic    = "anthropic"
	ProviderOpenAI       = "openai"
	ProviderGemini       = "gemini"
	ProviderBedrock      = "bedrock"
	ProviderClaudeCode   = "claudecode"
	ProviderVertex       = "vertex"
	ProviderVertexGoogle = "vertex-google"
)
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /root/coding/modelserver && go build ./internal/types/...`
Expected: success, no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/types/upstream.go
git commit -m "feat: add ProviderVertexGoogle constant for vertex-google provider"
```

---

### Task 2: URL Construction & Director

**Files:**
- Create: `internal/proxy/vertex_google.go`
- Test: `internal/proxy/vertex_google_test.go`

- [ ] **Step 1: Write the URL construction tests**

Create `internal/proxy/vertex_google_test.go`:

```go
package proxy

import (
	"testing"
)

func TestVertexGoogleEndpointURL(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		model     string
		streaming bool
		wantURL   string
	}{
		{
			name:      "non-streaming generateContent",
			baseURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/my-proj/locations/us-central1/publishers/google/models",
			model:     "gemini-2.5-flash",
			streaming: false,
			wantURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/my-proj/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent",
		},
		{
			name:      "streaming streamGenerateContent",
			baseURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/my-proj/locations/us-central1/publishers/google/models",
			model:     "gemini-2.5-pro",
			streaming: true,
			wantURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/my-proj/locations/us-central1/publishers/google/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
		},
		{
			name:      "trailing slash in base URL",
			baseURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models/",
			model:     "gemini-3-flash",
			streaming: false,
			wantURL:   "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models/gemini-3-flash:generateContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vertexGoogleEndpointURL(tt.baseURL, tt.model, tt.streaming)
			if got != tt.wantURL {
				t.Errorf("vertexGoogleEndpointURL() = %q, want %q", got, tt.wantURL)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestVertexGoogleEndpointURL -v`
Expected: FAIL — `vertexGoogleEndpointURL` not defined.

- [ ] **Step 3: Implement URL construction and director**

Create `internal/proxy/vertex_google.go`:

```go
package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// vertexGoogleContextKey is used to pass Vertex Google-specific parameters through
// the request context from the Executor to SetUpstream.
type vertexGoogleContextKey struct{}

// vertexGoogleParams holds the resolved model and streaming flag that SetUpstream
// needs to construct the Vertex AI Gemini endpoint URL.
type vertexGoogleParams struct {
	Model    string
	IsStream bool
}

// withVertexGoogleParams returns a new request with Vertex Google routing parameters
// stored in its context.
func withVertexGoogleParams(r *http.Request, model string, isStream bool) *http.Request {
	ctx := context.WithValue(r.Context(), vertexGoogleContextKey{}, vertexGoogleParams{
		Model:    model,
		IsStream: isStream,
	})
	return r.WithContext(ctx)
}

// vertexGoogleEndpointURL constructs the full Vertex AI Gemini endpoint URL.
// Format: {baseURL}/{model}:generateContent or {baseURL}/{model}:streamGenerateContent?alt=sse
func vertexGoogleEndpointURL(baseURL, model string, streaming bool) string {
	base := strings.TrimRight(baseURL, "/")
	method := "generateContent"
	if streaming {
		method = "streamGenerateContent"
	}
	endpoint := fmt.Sprintf("%s/%s:%s", base, model, method)
	if streaming {
		endpoint += "?alt=sse"
	}
	return endpoint
}

// directorSetVertexGoogleUpstream configures the outbound request for a Vertex AI
// Gemini upstream. Uses Bearer token auth (same as Vertex Anthropic) but targets
// the generateContent endpoint instead of rawPredict.
func directorSetVertexGoogleUpstream(req *http.Request, baseURL, accessToken, model string, streaming bool) {
	endpoint := vertexGoogleEndpointURL(baseURL, model, streaming)
	target, err := url.Parse(endpoint)
	if err != nil {
		req.Header.Set("Authorization", "Bearer "+accessToken)
		return
	}

	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = target.Path
	req.URL.RawPath = target.RawPath
	req.URL.RawQuery = target.RawQuery
	req.Host = target.Host

	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Remove client headers that Vertex AI Gemini does not use.
	req.Header.Del("x-api-key")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")
}
```

- [ ] **Step 4: Run URL test to verify it passes**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestVertexGoogleEndpointURL -v`
Expected: PASS.

- [ ] **Step 5: Write the director test**

Append to `internal/proxy/vertex_google_test.go`:

```go
func TestDirectorSetVertexGoogleUpstream(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req.Header.Set("x-api-key", "user-key")
	req.Header.Set("anthropic-version", "2023-06-01")

	directorSetVertexGoogleUpstream(req,
		"https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models",
		"ya29.fake-access-token",
		"gemini-2.5-flash",
		false,
	)

	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
	if req.URL.Host != "us-central1-aiplatform.googleapis.com" {
		t.Errorf("host = %s, want us-central1-aiplatform.googleapis.com", req.URL.Host)
	}
	wantPath := "/v1beta1/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
	if req.Header.Get("Authorization") != "Bearer ya29.fake-access-token" {
		t.Errorf("Authorization = %s, want Bearer ya29.fake-access-token", req.Header.Get("Authorization"))
	}
	if req.Header.Get("x-api-key") != "" {
		t.Errorf("x-api-key should be removed")
	}
	if req.Header.Get("anthropic-version") != "" {
		t.Errorf("anthropic-version should be removed")
	}
}

func TestDirectorSetVertexGoogleUpstream_Streaming(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-pro:streamGenerateContent", nil)

	directorSetVertexGoogleUpstream(req,
		"https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models",
		"ya29.token",
		"gemini-2.5-pro",
		true,
	)

	wantPath := "/v1beta1/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-pro:streamGenerateContent"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
	if req.URL.RawQuery != "alt=sse" {
		t.Errorf("query = %s, want alt=sse", req.URL.RawQuery)
	}
}
```

- [ ] **Step 6: Run all tests**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestVertexGoogle -v`
Expected: all 4 tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/proxy/vertex_google.go internal/proxy/vertex_google_test.go
git commit -m "feat: add vertex-google URL construction and director"
```

---

### Task 3: Transformer Implementation

**Files:**
- Create: `internal/proxy/provider_vertex_google.go`
- Modify: `internal/proxy/provider_transform.go:54-82`
- Test: `internal/proxy/vertex_google_test.go` (append)

- [ ] **Step 1: Write the transformer tests**

Append to `internal/proxy/vertex_google_test.go`:

```go
import (
	"net/http"

	"github.com/modelserver/modelserver/internal/types"
	"golang.org/x/oauth2"
)

func TestVertexGoogleTransformer_TransformBody_NoOp(t *testing.T) {
	transformer := &VertexGoogleTransformer{}
	input := []byte(`{"contents":[{"parts":[{"text":"hello"}],"role":"user"}]}`)
	output, err := transformer.TransformBody(input, "gemini-2.5-flash", false, http.Header{})
	if err != nil {
		t.Fatalf("TransformBody() error = %v", err)
	}
	if string(output) != string(input) {
		t.Errorf("TransformBody() should be no-op, got %s", string(output))
	}
}

func TestVertexGoogleTransformer_SetUpstream(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "ya29.test-token"},
	}
	tm.registerWithSource("u1", mock)

	transformer := &VertexGoogleTransformer{}
	transformer.SetTokenManager(tm)

	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req = withVertexGoogleParams(req, "gemini-2.5-flash", false)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models",
	}

	err := transformer.SetUpstream(req, upstream, "ignored-api-key")
	if err != nil {
		t.Fatalf("SetUpstream() error = %v", err)
	}

	if req.Header.Get("Authorization") != "Bearer ya29.test-token" {
		t.Errorf("Authorization = %q, want %q", req.Header.Get("Authorization"), "Bearer ya29.test-token")
	}
	wantPath := "/v1beta1/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"
	if req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", req.URL.Path, wantPath)
	}
}

func TestVertexGoogleTransformer_SetUpstream_Streaming(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "ya29.stream-token"},
	}
	tm.registerWithSource("u1", mock)

	transformer := &VertexGoogleTransformer{}
	transformer.SetTokenManager(tm)

	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:streamGenerateContent", nil)
	req = withVertexGoogleParams(req, "gemini-2.5-flash", true)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://us-central1-aiplatform.googleapis.com/v1beta1/projects/p/locations/us-central1/publishers/google/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err != nil {
		t.Fatalf("SetUpstream() error = %v", err)
	}

	wantPath := "/v1beta1/projects/p/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent"
	if req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", req.URL.Path, wantPath)
	}
	if req.URL.RawQuery != "alt=sse" {
		t.Errorf("query = %q, want alt=sse", req.URL.RawQuery)
	}
}

func TestVertexGoogleTransformer_SetUpstream_TokenError(t *testing.T) {
	tm := NewVertexTokenManager()
	transformer := &VertexGoogleTransformer{}
	transformer.SetTokenManager(tm)

	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req = withVertexGoogleParams(req, "gemini-2.5-flash", false)

	upstream := &types.Upstream{
		ID:      "unknown",
		BaseURL: "https://example.com/v1beta1/projects/p/locations/r/publishers/google/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err == nil {
		t.Fatal("expected error for unregistered upstream, got nil")
	}
}

func TestVertexGoogleTransformer_SetUpstream_NilTokenManager(t *testing.T) {
	transformer := &VertexGoogleTransformer{} // no SetTokenManager called

	req := mustNewRequest(t, "POST", "http://localhost/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req = withVertexGoogleParams(req, "gemini-2.5-flash", false)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://example.com/v1beta1/projects/p/locations/r/publishers/google/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err == nil {
		t.Fatal("expected error for nil token manager, got nil")
	}
}
```

Note: The import block at the top of the file needs to include the new imports. The final file should have a single combined import block with all needed imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestVertexGoogleTransformer -v`
Expected: FAIL — `VertexGoogleTransformer` not defined.

- [ ] **Step 3: Implement the transformer**

Create `internal/proxy/provider_vertex_google.go`:

```go
package proxy

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// VertexGoogleTransformer handles Vertex AI Gemini request/response transformations.
// It uses OAuth2 Bearer token auth (via VertexTokenManager) like the Anthropic Vertex
// transformer, but forwards Gemini native format requests and uses generateContent
// endpoints instead of rawPredict.
type VertexGoogleTransformer struct {
	tokenManager atomic.Pointer[VertexTokenManager]
}

var _ ProviderTransformer = (*VertexGoogleTransformer)(nil)

// TransformBody is a no-op for Vertex Google. The body is already in Gemini native
// format and is forwarded as-is.
func (t *VertexGoogleTransformer) TransformBody(body []byte, _ string, _ bool, _ http.Header) ([]byte, error) {
	return body, nil
}

// SetTokenManager atomically sets the token manager. Called by Router init.
func (t *VertexGoogleTransformer) SetTokenManager(tm *VertexTokenManager) {
	t.tokenManager.Store(tm)
}

// SetUpstream configures the outbound request for a Vertex AI Gemini upstream.
// Gets an OAuth2 token from the shared VertexTokenManager and constructs the
// generateContent / streamGenerateContent endpoint URL.
func (t *VertexGoogleTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, _ string) error {
	params, _ := r.Context().Value(vertexGoogleContextKey{}).(vertexGoogleParams)

	tm := t.tokenManager.Load()
	if tm == nil {
		return fmt.Errorf("vertex google token manager not initialized")
	}
	accessToken, err := tm.GetToken(upstream.ID)
	if err != nil {
		return err
	}

	directorSetVertexGoogleUpstream(r, upstream.BaseURL, accessToken, params.Model, params.IsStream)
	return nil
}

// WrapStream wraps the response body with the Gemini SSE stream interceptor.
// Vertex AI Gemini streaming returns the same SSE format as the native Gemini API.
func (t *VertexGoogleTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
	return newGeminiStreamInterceptor(body, startTime, onComplete)
}

// ParseResponse extracts metrics from a non-streaming Vertex AI Gemini response body.
func (t *VertexGoogleTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
	return ParseGeminiResponse(body)
}
```

- [ ] **Step 4: Register the transformer**

In `internal/proxy/provider_transform.go`, add the registration in `init()` and add a `SetVertexGoogleTokenManager` helper:

Add to the `init()` function after the existing vertex line:

```go
providerTransformers[types.ProviderVertexGoogle] = &VertexGoogleTransformer{} // tokenManager set by Router init via SetVertexGoogleTokenManager
```

Add this function after the existing `SetVertexTokenManager`:

```go
// SetVertexGoogleTokenManager sets the token manager on the already-registered
// VertexGoogleTransformer. Called by Router init after creating the token manager.
func SetVertexGoogleTokenManager(tm *VertexTokenManager) {
	if vt, ok := providerTransformers[types.ProviderVertexGoogle].(*VertexGoogleTransformer); ok {
		vt.SetTokenManager(tm)
	}
}
```

- [ ] **Step 5: Run transformer tests**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestVertexGoogleTransformer -v`
Expected: all 5 tests PASS.

- [ ] **Step 6: Run all vertex_google tests**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestVertexGoogle -v`
Expected: all 9 tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/proxy/provider_vertex_google.go internal/proxy/provider_transform.go internal/proxy/vertex_google_test.go
git commit -m "feat: add VertexGoogleTransformer with OAuth2 auth and Gemini format"
```

---

### Task 4: Router & Executor Integration

**Files:**
- Modify: `internal/proxy/router_engine.go:126-139`
- Modify: `internal/proxy/executor.go:257-271`
- Modify: `internal/proxy/executor.go:200-207`
- Modify: `internal/proxy/handler.go:160`

- [ ] **Step 1: Register vertex-google upstreams with token manager**

In `internal/proxy/router_engine.go`, find the block that wires `SetVertexTokenManager` (around line 95) and add `SetVertexGoogleTokenManager` right after it:

```go
// Wire the token manager into the already-registered VertexTransformer.
SetVertexTokenManager(r.vertexTokenManager)

// Wire the same token manager into the VertexGoogleTransformer.
SetVertexGoogleTokenManager(r.vertexTokenManager)
```

Then in the `buildMaps` function, find the loop that registers Vertex service account keys (around line 130) and extend the condition to also match `ProviderVertexGoogle`:

Change:
```go
if u.Provider == types.ProviderVertex {
```

To:
```go
if u.Provider == types.ProviderVertex || u.Provider == types.ProviderVertexGoogle {
```

- [ ] **Step 2: Add context injection in Executor**

In `internal/proxy/executor.go`, find the Gemini context injection block (around line 269) and add a block for `ProviderVertexGoogle` right after it:

```go
// For Vertex Google, inject the resolved model and streaming flag into the
// request context so SetUpstream can construct the correct URL path.
if upstream.Provider == types.ProviderVertexGoogle {
	outReq = withVertexGoogleParams(outReq, actualModel, reqCtx.IsStream)
}
```

Also in `executor.go`, find where model field rewriting is skipped (around line 205). Add `ProviderVertexGoogle` to the skip list:

Change:
```go
if actualModel != reqCtx.Model && upstream.Provider != types.ProviderBedrock && upstream.Provider != types.ProviderVertex && upstream.Provider != types.ProviderGemini {
```

To:
```go
if actualModel != reqCtx.Model && upstream.Provider != types.ProviderBedrock && upstream.Provider != types.ProviderVertex && upstream.Provider != types.ProviderGemini && upstream.Provider != types.ProviderVertexGoogle {
```

- [ ] **Step 3: Add ProviderVertexGoogle to HandleGemini allowed providers**

In `internal/proxy/handler.go`, find line 160 where `AllowedProviders` is set for `HandleGemini`:

Change:
```go
AllowedProviders: []string{types.ProviderGemini},
```

To:
```go
AllowedProviders: []string{types.ProviderGemini, types.ProviderVertexGoogle},
```

- [ ] **Step 4: Verify it compiles**

Run: `cd /root/coding/modelserver && go build ./internal/proxy/...`
Expected: success, no errors.

- [ ] **Step 5: Run all existing tests to ensure no regressions**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -v -count=1 2>&1 | tail -30`
Expected: all existing tests still pass.

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/router_engine.go internal/proxy/executor.go internal/proxy/handler.go
git commit -m "feat: wire vertex-google into router, executor, and handler"
```

---

### Task 5: Health Check Probe

**Files:**
- Modify: `internal/proxy/lb/health_checker.go:248-266`

- [ ] **Step 1: Add the buildVertexGoogleProbe method**

In `internal/proxy/lb/health_checker.go`, add a new case in the `buildProbeRequest` switch (around line 262, before the `default` case):

```go
case "vertex-google":
	return hc.buildVertexGoogleProbe(entry)
```

Then add the probe builder function (after `buildVertexProbe`, around line 424):

```go
func (hc *HealthChecker) buildVertexGoogleProbe(entry *healthEntry) (*http.Request, error) {
	body := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{{"text": "hi"}},
				"role":  "user",
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 1,
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal probe body: %w", err)
	}

	base := entry.baseURL
	if len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	url := fmt.Sprintf("%s/%s:generateContent", base, entry.testModel)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Get access token via the token fetcher callback (shared with vertex).
	if hc.tokenFetcher != nil {
		token, err := hc.tokenFetcher(entry.upstreamID)
		if err != nil {
			return nil, fmt.Errorf("get vertex google token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /root/coding/modelserver && go build ./internal/proxy/lb/...`
Expected: success, no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/proxy/lb/health_checker.go
git commit -m "feat: add vertex-google health check probe"
```

---

### Task 6: Dashboard UI

**Files:**
- Modify: `dashboard/src/pages/admin/UpstreamsPage.tsx`

- [ ] **Step 1: Add provider option to the select dropdown**

Find the `<SelectContent>` block (around line 618) and add the new option after the existing Vertex option:

```tsx
<SelectItem value="vertex">Google Vertex AI (Anthropic)</SelectItem>
<SelectItem value="vertex-google">Google Vertex AI (Gemini)</SelectItem>
```

Also rename the existing Vertex option from "Google Vertex AI" to "Google Vertex AI (Anthropic)" for clarity.

- [ ] **Step 2: Update baseURL placeholder**

Find the placeholder conditional (around line 641) and extend it to include `vertex-google`:

```tsx
placeholder={form.provider === "vertex"
  ? "https://REGION-aiplatform.googleapis.com/v1/projects/PROJECT/locations/REGION/publishers/anthropic/models"
  : form.provider === "vertex-google"
  ? "https://REGION-aiplatform.googleapis.com/v1beta1/projects/PROJECT/locations/REGION/publishers/google/models"
  : form.provider === "gemini"
  ? "https://generativelanguage.googleapis.com"
  : "https://api.anthropic.com"}
```

- [ ] **Step 3: Show service account JSON textarea for vertex-google**

Find the credential field conditional (around line 727) and extend the Textarea condition to include `vertex-google`:

Change:
```tsx
) : form.provider === "vertex" ? (
```

To:
```tsx
) : form.provider === "vertex" || form.provider === "vertex-google" ? (
```

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/pages/admin/UpstreamsPage.tsx
git commit -m "feat: add vertex-google provider option to dashboard"
```

---

### Task 7: Full Integration Verification

- [ ] **Step 1: Run all proxy tests**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/... -v -count=1 2>&1 | tail -50`
Expected: all tests PASS including the new vertex-google tests.

- [ ] **Step 2: Run the full test suite**

Run: `cd /root/coding/modelserver && go test ./... 2>&1 | tail -30`
Expected: all tests PASS.

- [ ] **Step 3: Verify dashboard builds**

Run: `cd /root/coding/modelserver/dashboard && npm run build 2>&1 | tail -10`
Expected: build succeeds.
