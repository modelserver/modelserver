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

// buildRejectedRequestRow assembles a *types.Request suitable for
// fire-and-forget persistence on 4xx pre-handler rejections. It captures
// every field that's knowable from the *http.Request + context at the
// rejection point — UA, kind, streaming, oauth grant, trace id, client
// ip, model, (Provider stays empty — set by the executor at completion time
// on the success path) — so the row matches the shape of successful rows
// (handler.go CreateRequest) except for the rejection-specific
// status / error_message / extra_usage_reason fields.
//
// Returns nil when Project or APIKey is missing from context. That's
// the same skip-on-missing-attribution policy the original
// emitGuardRejection used: 5xx infra paths that bypass auth would
// otherwise produce orphan rows.
func buildRejectedRequestRow(
	r *http.Request,
	status string,
	errMsg string,
	extraUsageReason string,
) *types.Request {
	if r == nil {
		return nil
	}
	project := ProjectFromContext(r.Context())
	apiKey := APIKeyFromContext(r.Context())
	if project == nil || apiKey == nil {
		return nil
	}

	model := ""
	if m := ModelFromContext(r.Context()); m != nil {
		model = m.Name
	}
	if model == "" {
		// Fall back to body shape — covers the case where ResolveModel
		// ran but the catalog had no entry (the success path's pending
		// row also stores whatever the client sent).
		model = peekModel(r)
	}

	metadata := map[string]string{}
	if ua := r.Header.Get("User-Agent"); ua != "" {
		metadata["user_agent"] = ua
	}
	kind := requestKindFromRequest(r)
	// Anthropic-specific headers — captured on the success path by
	// HandleMessages and HandleCountTokens (handler.go). Mirror the same
	// scoping: only the two anthropic surfaces, never Gemini / OpenAI
	// paths that don't read these headers.
	if kind == types.KindAnthropicMessages || kind == types.KindAnthropicCountTokens {
		if v := r.Header.Get("Anthropic-Beta"); v != "" {
			metadata["anthropic_beta"] = v
		}
		if v := r.Header.Get("Anthropic-Version"); v != "" {
			metadata["anthropic_version"] = v
		}
	}

	return &types.Request{
		ProjectID:        project.ID,
		APIKeyID:         apiKey.ID,
		OAuthGrantID:     OAuthGrantIDFromContext(r.Context()),
		CreatedBy:        apiKey.CreatedBy,
		TraceID:          TraceIDFromContext(r.Context()),
		// Provider is intentionally left empty: rejection happens before
		// upstream selection, so we don't know which upstream this would
		// have hit. The success-path pending row (handler.go CreateRequest)
		// also leaves Provider empty — it's filled at CompleteRequest time
		// from the chosen upstream. See spec §"row comparison".
		RequestKind:      kind,
		Model:            model,
		Streaming:        peekStreaming(r),
		Status:           status,
		ClientIP:         r.RemoteAddr,
		ErrorMessage:     errMsg,
		ExtraUsageReason: extraUsageReason,
		Metadata:         metadata,
	}
}
