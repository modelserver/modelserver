// Package proxy — rejected_request.go: shared helpers for persisting a
// requests row when the middleware chain rejects a request before any
// handler runs. Today this fires from RateLimitMiddleware (classic 4xx)
// and ExtraUsageGuardMiddleware (extra-usage 429s). The goal is row
// shape parity with the success path's CreateRequest — same metadata
// (UA), request_kind, streaming, oauth_grant_id, provider — so dashboards
// pivoting on those columns include the rejected traffic.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/modelserver/modelserver/internal/types"
)

// requestKindFromRequest maps the incoming path + method to a
// types.Kind* constant. Mirrors the per-handler constants chosen in
// handler.go (search for `RequestKind: types.Kind`). Returns "" if
// the path is outside the proxy's POST request surface (admin,
// health, GETs on /v1/models or /v1/usage, etc.); the caller treats
// "" the same as the production success path treats an unrouted
// pre-handler rejection: the column stays empty.
//
// The mapping is intentionally a switch rather than a re-run of the
// chi router: chi is not exposed at this point in the middleware
// chain and a small switch is easier to keep in sync with router.go
// than a router clone.
func requestKindFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if r.Method != http.MethodPost {
		return ""
	}
	switch r.URL.Path {
	case "/v1/messages":
		return types.KindAnthropicMessages
	case "/v1/messages/count_tokens":
		return types.KindAnthropicCountTokens
	case "/v1/responses":
		return types.KindOpenAIResponses
	case "/v1/responses/compact":
		return types.KindOpenAIResponsesCompact
	case "/v1/chat/completions":
		return types.KindOpenAIChatCompletions
	case "/v1/images/generations":
		return types.KindOpenAIImagesGenerations
	case "/v1/images/edits":
		return types.KindOpenAIImagesEdits
	}
	// Gemini native: /v1beta/models/{model}:{method}. The handler in
	// router.go binds POST /v1beta/models/* unconditionally and lets
	// HandleGemini classify by suffix; we do the same here.
	if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
		return types.KindGoogleGenerateContent
	}
	return ""
}

// streamingBodyPaths lists the proxy surfaces whose streaming flag lives
// in the JSON request body as {"stream": bool}. Other surfaces either
// signal streaming via the path (Gemini :stream*) or have no streaming
// variant at all (count_tokens, images/*).
var streamingBodyPaths = map[string]bool{
	"/v1/messages":          true,
	"/v1/responses":         true,
	"/v1/responses/compact": true,
	"/v1/chat/completions":  true,
}

// peekStreaming reports whether the incoming request is streaming
// without consuming the body. Three sources of truth:
//
//   - Gemini native paths whose suffix is :stream<Anything> → true.
//   - JSON POSTs to streamingBodyPaths → parse {"stream": bool}.
//   - Everything else → false.
//
// Body reads are restored so downstream middleware/handlers still see
// the original payload. Errors (bad JSON, IO failures) return false;
// the success path's metadata population has the same best-effort style.
func peekStreaming(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
		// Gemini native: any `:stream*` suffix means streaming.
		if i := strings.LastIndex(r.URL.Path, ":"); i >= 0 {
			suffix := r.URL.Path[i+1:]
			if strings.HasPrefix(suffix, "stream") {
				return true
			}
		}
		return false
	}
	if !streamingBodyPaths[r.URL.Path] {
		return false
	}
	if r.Body == nil {
		return false
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var shape struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &shape)
	return shape.Stream
}
