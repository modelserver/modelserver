// Package proxy — rejected_request.go: shared helpers for persisting a
// requests row when the middleware chain rejects a request before any
// handler runs. Today this fires from RateLimitMiddleware (classic 4xx)
// and ExtraUsageGuardMiddleware (extra-usage 429s). The goal is row
// shape parity with the success path's CreateRequest — same metadata
// (UA), request_kind, streaming, oauth_grant_id, provider — so dashboards
// pivoting on those columns include the rejected traffic.
package proxy

import (
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
