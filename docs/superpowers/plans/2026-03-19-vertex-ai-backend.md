# Vertex AI Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Google Vertex AI as a new upstream provider for Claude models, using service account JSON keys for authentication with in-process OAuth2 token management.

**Architecture:** Vertex AI serves Claude models via `rawPredict`/`streamRawPredict` endpoints with Anthropic-native request/response format. A `VertexTokenManager` handles OAuth2 token lifecycle using `golang.org/x/oauth2/google`. The `VertexTransformer` implements `ProviderTransformer` following the Bedrock pattern (URL path manipulation, body transform, context passing).

**Tech Stack:** Go, `golang.org/x/oauth2/google`, `tidwall/gjson`+`sjson`, React/TypeScript (dashboard)

**Spec:** `docs/superpowers/specs/2026-03-19-vertex-ai-backend-design.md`

---

## File Structure

### New files

| File | Responsibility |
|------|---------------|
| `internal/proxy/vertex_auth.go` | `VertexTokenManager` — register service accounts, get/refresh OAuth2 tokens |
| `internal/proxy/vertex_auth_test.go` | Unit tests for token manager with mock token source |
| `internal/proxy/vertex.go` | Body transform (`transformVertexBody`), URL construction (`vertexEndpointURL`), director function (`directorSetVertexUpstream`) |
| `internal/proxy/vertex_test.go` | Unit tests for body transform, URL construction, director |
| `internal/proxy/provider_vertex.go` | `VertexTransformer` — `ProviderTransformer` impl, context key/params |
| `internal/proxy/provider_vertex_test.go` | Unit tests for `SetUpstream` integration with mock token manager |

### Modified files

| File | Change |
|------|--------|
| `internal/types/upstream.go:11` | Add `ProviderVertex` constant |
| `internal/proxy/provider_transform.go:56-61` | Add `RegisterVertexTransformer()` |
| `internal/proxy/executor.go:197` | Add Vertex to model-rewrite exclusion |
| `internal/proxy/executor.go:242-244` | Add Vertex context injection |
| `internal/proxy/handler.go:44-48` | Add `ProviderVertex` to `HandleMessages` |
| `internal/proxy/handler.go:166-173` | Add `ProviderVertex` to `HandleCountTokens` |
| `internal/proxy/lb/health_checker.go:47-57` | Add `TokenFetcher` field |
| `internal/proxy/lb/health_checker.go:76-87` | Update `NewHealthChecker` signature |
| `internal/proxy/lb/health_checker.go:243-255` | Add `case "vertex"` probe |
| `internal/proxy/router_engine.go:83-107` | Wire `VertexTokenManager` into Router init |
| `internal/proxy/router_engine.go:406-415` | Clear+re-register tokens on Reload |
| `internal/admin/handle_upstreams.go:187-216` | Add Vertex test case |
| `dashboard/src/components/ui/textarea.tsx` | New: `Textarea` component |
| `dashboard/src/pages/admin/UpstreamsPage.tsx:286-318` | Add Vertex provider + textarea |

---

## Task 1: Provider Constant

**Files:**
- Modify: `internal/types/upstream.go:6-12`

- [ ] **Step 1: Add ProviderVertex constant**

In `internal/types/upstream.go`, add after line 11 (`ProviderClaudeCode`):

```go
const (
	ProviderAnthropic  = "anthropic"
	ProviderOpenAI     = "openai"
	ProviderGemini     = "gemini"
	ProviderBedrock    = "bedrock"
	ProviderClaudeCode = "claudecode"
	ProviderVertex     = "vertex"
)
```

- [ ] **Step 2: Verify build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: SUCCESS

- [ ] **Step 3: Commit**

```bash
git add internal/types/upstream.go
git commit -m "feat(vertex): add ProviderVertex constant"
```

---

## Task 2: VertexTokenManager

**Files:**
- Create: `internal/proxy/vertex_auth.go`
- Create: `internal/proxy/vertex_auth_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/proxy/vertex_auth_test.go`:

```go
package proxy

import (
	"fmt"
	"sync"
	"testing"

	"golang.org/x/oauth2"
)

// mockTokenSource returns a fixed token for testing.
type mockTokenSource struct {
	token *oauth2.Token
	err   error
	calls int
	mu    sync.Mutex
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.token, m.err
}

func TestVertexTokenManager_RegisterAndGetToken(t *testing.T) {
	tm := NewVertexTokenManager()

	// Register with a mock source (bypass real JSON parsing for unit test).
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "fake-token-123"},
	}
	tm.registerWithSource("upstream-1", mock)

	tok, err := tm.GetToken("upstream-1")
	if err != nil {
		t.Fatalf("GetToken() error = %v", err)
	}
	if tok != "fake-token-123" {
		t.Errorf("GetToken() = %q, want %q", tok, "fake-token-123")
	}
}

func TestVertexTokenManager_GetToken_UnknownUpstream(t *testing.T) {
	tm := NewVertexTokenManager()

	_, err := tm.GetToken("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown upstream, got nil")
	}
}

func TestVertexTokenManager_GetToken_SourceError(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{err: fmt.Errorf("auth failed")}
	tm.registerWithSource("upstream-1", mock)

	_, err := tm.GetToken("upstream-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestVertexTokenManager_Clear(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "tok"},
	}
	tm.registerWithSource("upstream-1", mock)

	tm.Clear()

	_, err := tm.GetToken("upstream-1")
	if err == nil {
		t.Fatal("expected error after Clear(), got nil")
	}
}

func TestVertexTokenManager_Deregister(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "tok"},
	}
	tm.registerWithSource("upstream-1", mock)

	tm.Deregister("upstream-1")

	_, err := tm.GetToken("upstream-1")
	if err == nil {
		t.Fatal("expected error after Deregister(), got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestVertexTokenManager -v`
Expected: FAIL — `NewVertexTokenManager` not defined

- [ ] **Step 3: Write the implementation**

Create `internal/proxy/vertex_auth.go`:

```go
package proxy

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const vertexOAuthScope = "https://www.googleapis.com/auth/cloud-platform"

// VertexTokenManager manages OAuth2 access tokens for Vertex AI upstreams.
// Each upstream has its own service account and independently cached token.
type VertexTokenManager struct {
	mu     sync.RWMutex
	tokens map[string]*vertexToken
}

type vertexToken struct {
	source oauth2.TokenSource
}

// NewVertexTokenManager creates a new token manager.
func NewVertexTokenManager() *VertexTokenManager {
	return &VertexTokenManager{
		tokens: make(map[string]*vertexToken),
	}
}

// Register parses a service account JSON key and creates a token source for
// the given upstream. The token source handles caching and automatic refresh.
func (m *VertexTokenManager) Register(upstreamID string, serviceAccountJSON []byte) error {
	creds, err := google.CredentialsFromJSON(context.Background(), serviceAccountJSON, vertexOAuthScope)
	if err != nil {
		return fmt.Errorf("parsing service account JSON for upstream %s: %w", upstreamID, err)
	}
	source := oauth2.ReuseTokenSource(nil, creds.TokenSource)
	m.mu.Lock()
	m.tokens[upstreamID] = &vertexToken{source: source}
	m.mu.Unlock()
	return nil
}

// registerWithSource is a test helper that registers an upstream with a custom
// token source, bypassing JSON key parsing.
func (m *VertexTokenManager) registerWithSource(upstreamID string, source oauth2.TokenSource) {
	m.mu.Lock()
	m.tokens[upstreamID] = &vertexToken{source: oauth2.ReuseTokenSource(nil, source)}
	m.mu.Unlock()
}

// GetToken returns a valid access token for the given upstream.
// The underlying ReuseTokenSource handles caching and refresh.
func (m *VertexTokenManager) GetToken(upstreamID string) (string, error) {
	m.mu.RLock()
	entry, ok := m.tokens[upstreamID]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("no vertex token source registered for upstream %s", upstreamID)
	}
	tok, err := entry.source.Token()
	if err != nil {
		return "", fmt.Errorf("getting token for upstream %s: %w", upstreamID, err)
	}
	return tok.AccessToken, nil
}

// Clear removes all registered token sources. Called by Router.Reload()
// before re-registering upstreams.
func (m *VertexTokenManager) Clear() {
	m.mu.Lock()
	m.tokens = make(map[string]*vertexToken)
	m.mu.Unlock()
}

// Deregister removes a single upstream's token source.
func (m *VertexTokenManager) Deregister(upstreamID string) {
	m.mu.Lock()
	delete(m.tokens, upstreamID)
	m.mu.Unlock()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run TestVertexTokenManager -v`
Expected: All 5 tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/vertex_auth.go internal/proxy/vertex_auth_test.go
git commit -m "feat(vertex): add VertexTokenManager for OAuth2 token lifecycle"
```

---

## Task 3: Body Transform & URL Construction

**Files:**
- Create: `internal/proxy/vertex.go`
- Create: `internal/proxy/vertex_test.go`

Reference: `internal/proxy/bedrock.go` and `internal/proxy/bedrock_test.go` for patterns.

- [ ] **Step 1: Write the failing tests**

Create `internal/proxy/vertex_test.go`:

```go
package proxy

import (
	"net/http"
	"strings"
	"testing"
)

func TestTransformVertexBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		betas     []string
		wantCheck func(t *testing.T, result string)
	}{
		{
			name: "sets anthropic_version and strips model/stream",
			body: `{"model":"claude-sonnet-4-20250514","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"Hi"}]}`,
			wantCheck: func(t *testing.T, result string) {
				if !strings.Contains(result, `"anthropic_version":"vertex-2023-10-16"`) {
					t.Errorf("expected anthropic_version, got %s", result)
				}
				if strings.Contains(result, `"model"`) {
					t.Errorf("model should be removed, got %s", result)
				}
				if strings.Contains(result, `"stream"`) {
					t.Errorf("stream should be removed, got %s", result)
				}
				if !strings.Contains(result, `"max_tokens"`) {
					t.Errorf("max_tokens should remain, got %s", result)
				}
			},
		},
		{
			name: "preserves existing anthropic_version",
			body: `{"model":"m","stream":false,"anthropic_version":"custom-ver"}`,
			wantCheck: func(t *testing.T, result string) {
				if !strings.Contains(result, `"anthropic_version":"custom-ver"`) {
					t.Errorf("expected custom version preserved, got %s", result)
				}
			},
		},
		{
			name:  "injects betas into body",
			body:  `{"model":"m","stream":false}`,
			betas: []string{"interleaved-thinking-2025-05-14"},
			wantCheck: func(t *testing.T, result string) {
				if !strings.Contains(result, `"anthropic_beta"`) {
					t.Errorf("expected anthropic_beta, got %s", result)
				}
				if !strings.Contains(result, "interleaved-thinking-2025-05-14") {
					t.Errorf("expected beta value, got %s", result)
				}
			},
		},
		{
			name: "no betas added when none provided",
			body: `{"model":"m","stream":false}`,
			wantCheck: func(t *testing.T, result string) {
				if strings.Contains(result, `"anthropic_beta"`) {
					t.Errorf("no anthropic_beta expected, got %s", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := transformVertexBody([]byte(tt.body), tt.betas)
			if err != nil {
				t.Fatalf("transformVertexBody() error = %v", err)
			}
			tt.wantCheck(t, string(result))
		})
	}
}

func TestVertexEndpointURL(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		model     string
		streaming bool
		wantURL   string
	}{
		{
			name:      "non-streaming rawPredict",
			baseURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models",
			model:     "claude-sonnet-4-20250514",
			streaming: false,
			wantURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-20250514:rawPredict",
		},
		{
			name:      "streaming streamRawPredict",
			baseURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models",
			model:     "claude-sonnet-4-20250514",
			streaming: true,
			wantURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-20250514:streamRawPredict",
		},
		{
			name:      "trailing slash in base URL",
			baseURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models/",
			model:     "claude-opus-4-20250514",
			streaming: false,
			wantURL:   "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-opus-4-20250514:rawPredict",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vertexEndpointURL(tt.baseURL, tt.model, tt.streaming)
			if got != tt.wantURL {
				t.Errorf("vertexEndpointURL() = %q, want %q", got, tt.wantURL)
			}
		})
	}
}

func TestDirectorSetVertexUpstream(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req.Header.Set("x-api-key", "user-key")
	req.Header.Set("anthropic-version", "2023-06-01")

	directorSetVertexUpstream(req,
		"https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models",
		"ya29.fake-access-token",
		"claude-sonnet-4-20250514",
		true,
	)

	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
	if req.URL.Host != "us-east5-aiplatform.googleapis.com" {
		t.Errorf("host = %s, want us-east5-aiplatform.googleapis.com", req.URL.Host)
	}
	wantPath := "/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-20250514:streamRawPredict"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
	if req.Header.Get("Authorization") != "Bearer ya29.fake-access-token" {
		t.Errorf("Authorization = %s, want Bearer ya29.fake-access-token", req.Header.Get("Authorization"))
	}
}

func TestDirectorSetVertexUpstream_NonStreaming(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)

	directorSetVertexUpstream(req,
		"https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models",
		"ya29.token",
		"claude-sonnet-4-20250514",
		false,
	)

	wantPath := "/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-20250514:rawPredict"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run "TestTransformVertexBody|TestVertexEndpointURL|TestDirectorSetVertexUpstream" -v`
Expected: FAIL — functions not defined

- [ ] **Step 3: Write the implementation**

Create `internal/proxy/vertex.go`:

```go
package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const vertexDefaultVersion = "vertex-2023-10-16"

// vertexSupportedBetas is the set of anthropic_beta flags that Vertex AI
// recognises. Uses the same set as Bedrock since both host Claude models.
var vertexSupportedBetas = map[string]bool{
	"computer-use-2025-01-24":          true,
	"token-efficient-tools-2025-02-19": true,
	"interleaved-thinking-2025-05-14":  true,
	"output-128k-2025-02-19":           true,
	"dev-full-thinking-2025-05-14":     true,
	"context-1m-2025-08-07":            true,
	"context-management-2025-06-27":    true,
	"effort-2025-11-24":                true,
	"tool-search-tool-2025-10-19":      true,
	"tool-examples-2025-10-29":         true,
}

// filterVertexBetas returns only the beta flags that Vertex AI supports.
func filterVertexBetas(betas []string) (supported, dropped []string) {
	for _, b := range betas {
		if vertexSupportedBetas[b] {
			supported = append(supported, b)
		} else {
			dropped = append(dropped, b)
		}
	}
	return
}

// transformVertexBody modifies the request body for Vertex AI format:
//   - Sets anthropic_version to "vertex-2023-10-16" if not present
//   - Moves anthropic-beta header values into body as anthropic_beta array
//   - Removes model and stream fields (encoded in the URL)
func transformVertexBody(body []byte, betas []string) ([]byte, error) {
	var err error

	if !gjson.GetBytes(body, "anthropic_version").Exists() {
		body, err = sjson.SetBytes(body, "anthropic_version", vertexDefaultVersion)
		if err != nil {
			return nil, fmt.Errorf("setting anthropic_version: %w", err)
		}
	}

	if len(betas) > 0 {
		body, err = sjson.SetBytes(body, "anthropic_beta", betas)
		if err != nil {
			return nil, fmt.Errorf("setting anthropic_beta: %w", err)
		}
	}

	body, _ = sjson.DeleteBytes(body, "model")
	body, _ = sjson.DeleteBytes(body, "stream")

	return body, nil
}

// vertexEndpointURL constructs the full Vertex AI endpoint URL.
// Format: {baseURL}/{model}:rawPredict or {baseURL}/{model}:streamRawPredict
func vertexEndpointURL(baseURL, model string, streaming bool) string {
	base := strings.TrimRight(baseURL, "/")
	method := "rawPredict"
	if streaming {
		method = "streamRawPredict"
	}
	return fmt.Sprintf("%s/%s:%s", base, model, method)
}

// directorSetVertexUpstream configures the outbound request for a Vertex AI upstream.
func directorSetVertexUpstream(req *http.Request, baseURL, accessToken, model string, streaming bool) {
	endpoint := vertexEndpointURL(baseURL, model, streaming)
	target, err := url.Parse(endpoint)
	if err != nil {
		// Fallback: just set the token.
		req.Header.Set("Authorization", "Bearer "+accessToken)
		return
	}

	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = target.Path
	req.URL.RawPath = target.RawPath
	req.Host = target.Host

	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Remove client headers that Vertex AI does not use.
	req.Header.Del("x-api-key")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run "TestTransformVertexBody|TestVertexEndpointURL|TestDirectorSetVertexUpstream" -v`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/vertex.go internal/proxy/vertex_test.go
git commit -m "feat(vertex): add body transform, URL construction, and director function"
```

---

## Task 4: VertexTransformer (ProviderTransformer implementation)

**Files:**
- Create: `internal/proxy/provider_vertex.go`
- Create: `internal/proxy/provider_vertex_test.go`
- Modify: `internal/proxy/provider_transform.go:54-61`

Reference: `internal/proxy/provider_bedrock.go` for context key pattern.

- [ ] **Step 1: Write the failing test**

Create `internal/proxy/provider_vertex_test.go`:

```go
package proxy

import (
	"net/http"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
	"golang.org/x/oauth2"
)

func TestVertexTransformer_SetUpstream(t *testing.T) {
	tm := NewVertexTokenManager()
	mock := &mockTokenSource{
		token: &oauth2.Token{AccessToken: "ya29.test-token"},
	}
	tm.registerWithSource("u1", mock)

	transformer := &VertexTransformer{tokenManager: tm}

	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req = withVertexParams(req, "claude-sonnet-4-20250514", true)

	upstream := &types.Upstream{
		ID:      "u1",
		BaseURL: "https://us-east5-aiplatform.googleapis.com/v1/projects/p/locations/us-east5/publishers/anthropic/models",
	}

	err := transformer.SetUpstream(req, upstream, "ignored-api-key")
	if err != nil {
		t.Fatalf("SetUpstream() error = %v", err)
	}

	if req.Header.Get("Authorization") != "Bearer ya29.test-token" {
		t.Errorf("Authorization = %q, want %q", req.Header.Get("Authorization"), "Bearer ya29.test-token")
	}
	wantPath := "/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-4-20250514:streamRawPredict"
	if req.URL.Path != wantPath {
		t.Errorf("path = %q, want %q", req.URL.Path, wantPath)
	}
}

func TestVertexTransformer_SetUpstream_TokenError(t *testing.T) {
	tm := NewVertexTokenManager()
	// No upstream registered — GetToken will fail.
	transformer := &VertexTransformer{tokenManager: tm}

	req := mustNewRequest(t, "POST", "http://localhost/v1/messages", nil)
	req = withVertexParams(req, "claude-sonnet-4-20250514", false)

	upstream := &types.Upstream{
		ID:      "unknown",
		BaseURL: "https://example.com/v1/projects/p/locations/r/publishers/anthropic/models",
	}

	err := transformer.SetUpstream(req, upstream, "")
	if err == nil {
		t.Fatal("expected error for unregistered upstream, got nil")
	}
}

func TestVertexTransformer_TransformBody(t *testing.T) {
	transformer := &VertexTransformer{}

	headers := http.Header{}
	headers.Set("anthropic-beta", "interleaved-thinking-2025-05-14,claude-code-20250219")

	body := []byte(`{"model":"claude-sonnet-4","stream":true,"max_tokens":1024}`)
	result, err := transformer.TransformBody(body, "claude-sonnet-4", true, headers)
	if err != nil {
		t.Fatalf("TransformBody() error = %v", err)
	}
	s := string(result)

	// model and stream should be stripped
	if contains(s, `"model"`) {
		t.Errorf("model should be removed: %s", s)
	}
	if contains(s, `"stream"`) {
		t.Errorf("stream should be removed: %s", s)
	}
	// anthropic_version should be set
	if !contains(s, `"anthropic_version"`) {
		t.Errorf("anthropic_version should be set: %s", s)
	}
	// supported beta should be in body, unsupported dropped
	if !contains(s, "interleaved-thinking-2025-05-14") {
		t.Errorf("supported beta should be in body: %s", s)
	}
	if contains(s, "claude-code-20250219") {
		t.Errorf("unsupported beta should be dropped: %s", s)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run "TestVertexTransformer" -v`
Expected: FAIL — `VertexTransformer` not defined

- [ ] **Step 3: Write the implementation**

Create `internal/proxy/provider_vertex.go`:

```go
package proxy

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelserver/modelserver/internal/types"
)

// vertexContextKey is used to pass Vertex-specific parameters through the
// request context from the Executor to SetUpstream.
type vertexContextKey struct{}

// vertexParams holds the resolved model and streaming flag that SetUpstream
// needs to construct the Vertex endpoint URL.
type vertexParams struct {
	Model    string
	IsStream bool
}

// withVertexParams returns a new request with Vertex routing parameters
// stored in its context.
func withVertexParams(r *http.Request, model string, isStream bool) *http.Request {
	ctx := context.WithValue(r.Context(), vertexContextKey{}, vertexParams{
		Model:    model,
		IsStream: isStream,
	})
	return r.WithContext(ctx)
}

// VertexTransformer handles Google Vertex AI request/response transformations.
// Vertex AI hosts Claude models via rawPredict/streamRawPredict endpoints,
// using the Anthropic wire format with a different auth mechanism and URL scheme.
type VertexTransformer struct {
	tokenManager *VertexTokenManager
}

var _ ProviderTransformer = (*VertexTransformer)(nil)

// TransformBody applies Vertex-specific body modifications:
//   - Sets anthropic_version to "vertex-2023-10-16" if not present
//   - Moves supported anthropic-beta header values into the body
//   - Removes model and stream fields (Vertex encodes these in the URL)
func (t *VertexTransformer) TransformBody(body []byte, _ string, _ bool, headers http.Header) ([]byte, error) {
	allBetas := splitBetaHeaders(headers.Values("anthropic-beta"))
	betas, _ := filterVertexBetas(allBetas)
	return transformVertexBody(body, betas)
}

// SetUpstream configures the outbound request for a Vertex AI upstream.
// It reads the resolved model and streaming flag from the request context
// (set by the Executor via withVertexParams), gets a valid OAuth2 access
// token from the token manager, and constructs the endpoint URL.
func (t *VertexTransformer) SetUpstream(r *http.Request, upstream *types.Upstream, _ string) error {
	params, _ := r.Context().Value(vertexContextKey{}).(vertexParams)

	accessToken, err := t.tokenManager.GetToken(upstream.ID)
	if err != nil {
		return err
	}

	directorSetVertexUpstream(r, upstream.BaseURL, accessToken, params.Model, params.IsStream)
	return nil
}

// WrapStream wraps the response body with the Anthropic SSE stream interceptor.
// Vertex AI's streamRawPredict returns standard Anthropic SSE format.
func (t *VertexTransformer) WrapStream(body io.ReadCloser, startTime time.Time, onComplete func(StreamMetrics)) io.ReadCloser {
	return newStreamInterceptor(body, startTime, func(model, msgID string, usage anthropic.Usage, ttft int64) {
		onComplete(StreamMetrics{
			Model:               model,
			MsgID:               msgID,
			InputTokens:         usage.InputTokens,
			OutputTokens:        usage.OutputTokens,
			CacheCreationTokens: usage.CacheCreationInputTokens,
			CacheReadTokens:     usage.CacheReadInputTokens,
			TTFTMs:              ttft,
		})
	})
}

// ParseResponse extracts metrics from a non-streaming Vertex AI response body.
// Vertex AI's rawPredict returns standard Anthropic JSON format.
func (t *VertexTransformer) ParseResponse(body []byte) (*ResponseMetrics, error) {
	model, msgID, usage, err := ParseNonStreamingResponse(body)
	if err != nil {
		return nil, err
	}
	return &ResponseMetrics{
		Model:               model,
		MsgID:               msgID,
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationInputTokens,
		CacheReadTokens:     usage.CacheReadInputTokens,
	}, nil
}
```

- [ ] **Step 4: Add RegisterVertexTransformer to provider_transform.go**

In `internal/proxy/provider_transform.go`, add at the end of the file (after line 71):

```go
// RegisterVertexTransformer registers the Vertex AI transformer with the given
// token manager. Called during server initialization after the token manager
// is created (cannot use init() since the transformer needs runtime state).
func RegisterVertexTransformer(tm *VertexTokenManager) {
	providerTransformers[types.ProviderVertex] = &VertexTransformer{tokenManager: tm}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /root/coding/modelserver && go test ./internal/proxy/ -run "TestVertexTransformer" -v`
Expected: All 3 tests PASS

- [ ] **Step 6: Verify full build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: SUCCESS

- [ ] **Step 7: Commit**

```bash
git add internal/proxy/provider_vertex.go internal/proxy/provider_vertex_test.go internal/proxy/provider_transform.go
git commit -m "feat(vertex): add VertexTransformer implementing ProviderTransformer"
```

---

## Task 5: Executor & Handler Integration

**Files:**
- Modify: `internal/proxy/executor.go:197,242-244`
- Modify: `internal/proxy/handler.go:44-48,166-173`

- [ ] **Step 1: Add Vertex to model-rewrite guard in executor.go**

In `internal/proxy/executor.go`, change line 197 from:

```go
			if actualModel != reqCtx.Model && upstream.Provider != types.ProviderBedrock {
```

to:

```go
			if actualModel != reqCtx.Model && upstream.Provider != types.ProviderBedrock && upstream.Provider != types.ProviderVertex {
```

- [ ] **Step 2: Add Vertex context injection in executor.go**

In `internal/proxy/executor.go`, after the Bedrock context injection block (after line 244), add:

```go
		// For Vertex, inject the resolved model and streaming flag into the
		// request context so SetUpstream can construct the correct URL path.
		if upstream.Provider == types.ProviderVertex {
			outReq = withVertexParams(outReq, actualModel, reqCtx.IsStream)
		}
```

- [ ] **Step 3: Add ProviderVertex to HandleMessages in handler.go**

In `internal/proxy/handler.go`, change `HandleMessages` (lines 44-48) from:

```go
	h.handleProxyRequest(w, r, []string{
		types.ProviderAnthropic,
		types.ProviderBedrock,
		types.ProviderClaudeCode,
	})
```

to:

```go
	h.handleProxyRequest(w, r, []string{
		types.ProviderAnthropic,
		types.ProviderBedrock,
		types.ProviderClaudeCode,
		types.ProviderVertex,
	})
```

- [ ] **Step 4: Add ProviderVertex to HandleCountTokens in handler.go**

In `internal/proxy/handler.go`, change the filter at lines 168-172 from:

```go
		if c.Upstream.Provider == types.ProviderAnthropic || c.Upstream.Provider == types.ProviderClaudeCode {
```

to:

```go
		if c.Upstream.Provider == types.ProviderAnthropic || c.Upstream.Provider == types.ProviderClaudeCode {
			// Note: Vertex is excluded from count_tokens because the
			// httputil.ReverseProxy Director cannot transform the request body
			// (strip model/stream, inject anthropic_version). If Vertex
			// count_tokens support is needed later, refactor to use the
			// Executor pipeline instead of ReverseProxy.
```

The `HandleCountTokens` Director does not need changes — Vertex is excluded from the provider filter above, so the Director will never receive a Vertex upstream.

- [ ] **Step 5: Verify build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: SUCCESS

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/executor.go internal/proxy/handler.go
git commit -m "feat(vertex): integrate Vertex into executor and handler routing"
```

---

## Task 6: Health Checker Integration

**Files:**
- Modify: `internal/proxy/lb/health_checker.go:47-57,76-87,243-255`
- Modify: `internal/proxy/router_engine.go:101`

- [ ] **Step 1: Add TokenFetcher type and field to HealthChecker**

In `internal/proxy/lb/health_checker.go`, add the type before the `HealthChecker` struct (before line 47):

```go
// TokenFetcher retrieves a valid access token for the given upstream.
// Used by health checker for providers that require dynamic token refresh (e.g., Vertex AI).
type TokenFetcher func(upstreamID string) (string, error)
```

Add the field to `HealthChecker` struct (after `wg` field at line 56):

```go
	tokenFetcher   TokenFetcher           // Optional: for Vertex AI token refresh
```

- [ ] **Step 2: Update NewHealthChecker to accept TokenFetcher**

Change `NewHealthChecker` (lines 76-87) from:

```go
func NewHealthChecker(cb *CircuitBreaker, metrics *UpstreamMetrics, logger *slog.Logger) *HealthChecker {
```

to:

```go
func NewHealthChecker(cb *CircuitBreaker, metrics *UpstreamMetrics, logger *slog.Logger, tokenFetcher TokenFetcher) *HealthChecker {
```

And add to the returned struct:

```go
		tokenFetcher:   tokenFetcher,
```

- [ ] **Step 3: Add Vertex probe builder**

In `internal/proxy/lb/health_checker.go`, add after `buildBedrockProbe` (after line 347):

```go
func (hc *HealthChecker) buildVertexProbe(entry *healthEntry) (*http.Request, error) {
	body := map[string]interface{}{
		"anthropic_version": "vertex-2023-10-16",
		"max_tokens":        1,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal probe body: %w", err)
	}

	// Construct the rawPredict URL: baseURL/{testModel}:rawPredict
	base := entry.baseURL
	if len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	url := fmt.Sprintf("%s/%s:rawPredict", base, entry.testModel)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Get access token via the token fetcher callback.
	if hc.tokenFetcher != nil {
		token, err := hc.tokenFetcher(entry.upstreamID)
		if err != nil {
			return nil, fmt.Errorf("get vertex token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
}
```

- [ ] **Step 4: Add "vertex" case to buildProbeRequest switch**

In `internal/proxy/lb/health_checker.go`, change `buildProbeRequest` (lines 243-255) to add after the "bedrock" case:

```go
	case "vertex":
		return hc.buildVertexProbe(entry)
```

The full switch becomes:

```go
func (hc *HealthChecker) buildProbeRequest(entry *healthEntry) (*http.Request, error) {
	switch entry.provider {
	case "anthropic":
		return hc.buildAnthropicProbe(entry)
	case "openai", "gemini":
		return hc.buildOpenAIProbe(entry)
	case "claudecode":
		return hc.buildClaudeCodeProbe(entry)
	case "bedrock":
		return hc.buildBedrockProbe(entry)
	case "vertex":
		return hc.buildVertexProbe(entry)
	default:
		return hc.buildOpenAIProbe(entry)
	}
}
```

- [ ] **Step 5: Update NewHealthChecker call site in router_engine.go**

In `internal/proxy/router_engine.go`, line 101 currently reads:

```go
	r.healthChecker = lb.NewHealthChecker(r.circuitBreaker, r.metrics, logger)
```

This will fail to compile after the signature change. For now, pass `nil` as the token fetcher — it will be wired in Task 7:

```go
	r.healthChecker = lb.NewHealthChecker(r.circuitBreaker, r.metrics, logger, nil)
```

- [ ] **Step 6: Verify build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: SUCCESS

- [ ] **Step 7: Commit**

```bash
git add internal/proxy/lb/health_checker.go internal/proxy/router_engine.go
git commit -m "feat(vertex): add TokenFetcher and Vertex health probe to HealthChecker"
```

---

## Task 7: Router Wiring (VertexTokenManager ↔ Router ↔ HealthChecker)

**Files:**
- Modify: `internal/proxy/router_engine.go:83-107,111-124,406-415`

- [ ] **Step 1: Add vertexTokenManager field to Router struct**

In `internal/proxy/router_engine.go`, find the Router struct and add:

```go
	vertexTokenManager *VertexTokenManager
```

- [ ] **Step 2: Update NewRouter to create and wire VertexTokenManager**

Change `NewRouter` to create the token manager, pass it to health checker, and register the transformer. The updated function:

```go
func NewRouter(
	upstreams []types.Upstream,
	groups []store.UpstreamGroupWithMembers,
	routes []types.Route,
	encKey []byte,
	logger *slog.Logger,
	sessionTTL time.Duration,
	_ *store.Store,
) *Router {
	r := &Router{
		sessionTTL: sessionTTL,
		logger:     logger,
	}

	// Create the Vertex AI token manager.
	r.vertexTokenManager = NewVertexTokenManager()

	// Register the Vertex transformer (needs token manager).
	RegisterVertexTransformer(r.vertexTokenManager)

	// Build shared infrastructure components.
	r.connTracker = lb.NewConnectionTracker()
	r.metrics = lb.NewUpstreamMetrics()
	r.circuitBreaker = lb.NewCircuitBreaker(5, 2, 30*time.Second)
	r.healthChecker = lb.NewHealthChecker(r.circuitBreaker, r.metrics, logger, r.vertexTokenManager.GetToken)

	// Build all maps from the configuration.
	r.buildMaps(upstreams, groups, routes, encKey)

	return r
}
```

- [ ] **Step 3: Register Vertex upstreams in buildMaps**

In `internal/proxy/router_engine.go`, in `buildMaps`, after the line `keys := decryptUpstreamKeys(upstreams, encKey, r.logger)` (line 124), add:

```go
	// Register Vertex AI service account keys with the token manager.
	if r.vertexTokenManager != nil {
		r.vertexTokenManager.Clear()
		for _, u := range upstreams {
			if u.Provider == types.ProviderVertex {
				if key, ok := keys[u.ID]; ok {
					if err := r.vertexTokenManager.Register(u.ID, []byte(key)); err != nil {
						r.logger.Error("failed to register vertex token source",
							"upstream_id", u.ID, "error", err)
					}
				}
			}
		}
	}
```

Note: `types` is already imported in this file (check with `go build`). Also need to import types if not already — it should already be imported via existing usage.

- [ ] **Step 4: Verify build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: SUCCESS

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/router_engine.go
git commit -m "feat(vertex): wire VertexTokenManager into Router and HealthChecker"
```

---

## Task 8: Admin Test Endpoints

**Files:**
- Modify: `internal/admin/handle_upstreams.go:187-216`

Note: `handle_channels.go` does not exist on main. If/when it is merged, the same Vertex test case should be added there.

- [ ] **Step 1: Add Vertex test case to handleTestUpstream**

In `internal/admin/handle_upstreams.go`, in the `handleTestUpstream` function, find the switch statement for building the request body (around line 187). Add a new case before `default:`:

```go
		case types.ProviderVertex:
			base := baseURL
			if len(base) > 0 && base[len(base)-1] == '/' {
				base = base[:len(base)-1]
			}
			endpoint = fmt.Sprintf("%s/%s:rawPredict", base, upstreamTestModel)
			reqBody, _ = json.Marshal(map[string]interface{}{
				"anthropic_version": "vertex-2023-10-16",
				"max_tokens":        10,
				"messages":          []map[string]string{{"role": "user", "content": "Hi"}},
			})
```

And in the auth header switch (around line 229), add before `default:`:

```go
		case types.ProviderVertex:
			creds, err := google.CredentialsFromJSON(r.Context(), apiKey, "https://www.googleapis.com/auth/cloud-platform")
			if err != nil {
				writeData(w, http.StatusOK, map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("failed to parse service account JSON: %v", err),
				})
				return
			}
			tok, err := creds.TokenSource.Token()
			if err != nil {
				writeData(w, http.StatusOK, map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("failed to get access token: %v", err),
				})
				return
			}
			req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
```

Add import for `"golang.org/x/oauth2/google"` at the top of the file.

- [ ] **Step 2: Verify build**

Run: `cd /root/coding/modelserver && go build ./...`
Expected: SUCCESS

- [ ] **Step 3: Commit**

```bash
git add internal/admin/handle_upstreams.go
git commit -m "feat(vertex): add Vertex test endpoint to admin upstream handler"
```

---

## Task 9: Dashboard UI Changes

**Files:**
- Create: `dashboard/src/components/ui/textarea.tsx`
- Modify: `dashboard/src/pages/admin/UpstreamsPage.tsx:286-318`

Note: `ChannelsPage.tsx` does not exist on main. If/when it is merged, the same Vertex UI changes should be added there.

- [ ] **Step 1: Create Textarea component**

Create `dashboard/src/components/ui/textarea.tsx`:

```tsx
import * as React from "react"
import { cn } from "@/lib/utils"

function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        "w-full min-w-0 rounded-lg border border-input bg-transparent px-2.5 py-1.5 text-base transition-colors outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50 md:text-sm dark:bg-input/30 dark:disabled:bg-input/80 resize-y",
        className
      )}
      {...props}
    />
  )
}

export { Textarea }
```

- [ ] **Step 2: Update UpstreamsPage.tsx**

In `dashboard/src/pages/admin/UpstreamsPage.tsx`:

Add import at top:
```tsx
import { Textarea } from "@/components/ui/textarea"
```

Add Vertex to provider dropdown (after line 291, the "claudecode" SelectItem):
```tsx
                  <SelectItem value="vertex">Google Vertex AI</SelectItem>
```

Replace the API Key input (lines 312-318) with conditional rendering:
```tsx
              <Label>{editingId ? "API Key (leave blank to keep current)" : "API Key"}</Label>
              {form.provider === "vertex" ? (
                <Textarea
                  value={form.api_key}
                  onChange={(e) => setForm((p) => ({ ...p, api_key: e.target.value }))}
                  placeholder="Paste service account JSON key here..."
                  rows={6}
                  className="font-mono text-xs"
                />
              ) : (
                <Input
                  type="password"
                  value={form.api_key}
                  onChange={(e) => setForm((p) => ({ ...p, api_key: e.target.value }))}
                  placeholder="sk-..."
                />
              )}
```

Update Base URL placeholder to be dynamic (lines 304-309):
```tsx
              <Input
                value={form.base_url}
                onChange={(e) => setForm((p) => ({ ...p, base_url: e.target.value }))}
                placeholder={form.provider === "vertex"
                  ? "https://REGION-aiplatform.googleapis.com/v1/projects/PROJECT/locations/REGION/publishers/anthropic/models"
                  : "https://api.anthropic.com"}
              />
```

- [ ] **Step 3: Verify frontend builds**

Run: `cd /root/coding/modelserver/dashboard && npm run build`
Expected: SUCCESS (or use `npx tsc --noEmit` for type-check only)

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/components/ui/textarea.tsx dashboard/src/pages/admin/UpstreamsPage.tsx
git commit -m "feat(vertex): add Vertex provider to dashboard with textarea for service account JSON"
```

---

## Task 10: Dependency Check & Final Verification

**Files:**
- Possibly modify: `go.mod`, `go.sum`

- [ ] **Step 1: Verify oauth2/google import resolves**

Run: `cd /root/coding/modelserver && go mod tidy`
Check that `golang.org/x/oauth2` is still in go.mod (it should be — already at v0.36.0).

- [ ] **Step 2: Run full test suite**

Run: `cd /root/coding/modelserver && go test ./...`
Expected: All tests PASS (including new Vertex tests)

- [ ] **Step 3: Run go vet**

Run: `cd /root/coding/modelserver && go vet ./...`
Expected: No issues

- [ ] **Step 4: Commit if go.mod/go.sum changed**

```bash
git add go.mod go.sum
git commit -m "chore: update go.mod/go.sum for vertex oauth2/google dependency"
```

(Skip this step if no changes to go.mod/go.sum.)

- [ ] **Step 5: Final verification summary**

Verify all these are true:
- `go build ./...` succeeds
- `go test ./...` passes
- `go vet ./...` clean
- Dashboard builds (if npm available)
- New files: `vertex_auth.go`, `vertex_auth_test.go`, `vertex.go`, `vertex_test.go`, `provider_vertex.go`, `provider_vertex_test.go`, `textarea.tsx`
- Modified files: `upstream.go`, `provider_transform.go`, `executor.go`, `handler.go`, `health_checker.go`, `router_engine.go`, `handle_upstreams.go`, `UpstreamsPage.tsx`
