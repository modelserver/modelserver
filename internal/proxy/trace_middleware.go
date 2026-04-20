package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

const (
	ctxTraceID     contextKey = "trace_id"
	ctxTraceSource contextKey = "trace_source"
	ctxClientKind  contextKey = "client_kind"

	openCodeTraceHeader = "X-Opencode-Session"
	codexTraceHeader    = "Session_id"
)

// claudeUserIDLegacyPattern matches the legacy Claude Code user_id string format:
// user_<64 hex chars>_account_<uuid>_session_<uuid>
// Also matches the variant without account UUID:
// user_<64 hex chars>_account__session_<uuid>
var claudeUserIDLegacyPattern = regexp.MustCompile(
	`(?i)^user_[0-9a-f]{64}_account_(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})?_session_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`,
)

// TraceIDFromContext returns the trace ID from the request context.
func TraceIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(ctxTraceID).(string); ok {
		return id
	}
	return ""
}

// TraceSourceFromContext returns the trace source from the request context.
func TraceSourceFromContext(ctx context.Context) string {
	if s, ok := ctx.Value(ctxTraceSource).(string); ok {
		return s
	}
	return ""
}

// ClientKindFromContext returns the client-kind classification derived by the
// trace middleware. Unlike TraceSource, this does not prefer X-Trace-Id
// headers — it identifies the upstream client so subscription-eligibility
// checks can decide whether to route to extra usage.
func ClientKindFromContext(ctx context.Context) string {
	if k, ok := ctx.Value(ctxClientKind).(string); ok {
		return k
	}
	return types.ClientKindUnknown
}

// TraceMiddleware extracts trace IDs from multiple sources and, when one is
// found, ensures a corresponding row exists in the traces table before any
// downstream middleware runs. That matters for the FK on requests.trace_id:
// downstream middlewares (rate-limit rejection, extra-usage guard) may need
// to write request rows that reference this trace_id, and those INSERTs would
// fail silently if the trace row didn't exist yet.
//
// st and logger may be nil in tests that only exercise trace extraction.
// If no trace ID is found from any source, no trace context is set — except for
// OpenClaw, which does not send a session ID and uses the API key ID as trace ID.
func TraceMiddleware(traceCfg config.TraceConfig, st *store.Store, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			traceID, source := extractTraceID(r, traceCfg)

			// Enforce session for POST endpoints that create completions.
			if traceCfg.RequireSession && traceID == "" {
				if r.Method == http.MethodPost && isCompletionEndpoint(r.URL.Path) {
					writeProxyError(w, http.StatusBadRequest,
						"please use a coding agent such as Claude Code, OpenCode, Codex, OpenClaw, or Gemini CLI")
					return
				}
			}

			ctx := r.Context()
			if traceID != "" {
				ctx = context.WithValue(ctx, ctxTraceID, traceID)
				ctx = context.WithValue(ctx, ctxTraceSource, source)

				// Ensure the trace row exists so downstream writes to
				// requests.trace_id don't violate the FK. Only for POST
				// (completion endpoints that write request rows); GET
				// endpoints like /v1/models never insert request records.
				if st != nil && r.Method == http.MethodPost {
					if project := ProjectFromContext(ctx); project != nil {
						if err := st.EnsureTrace(project.ID, traceID, source); err != nil && logger != nil {
							logger.Warn("failed to ensure trace", "error", err, "trace_id", traceID)
						}
					}
				}
			}

			// Independent client-kind detection (decoupled from TraceSource
			// precedence — a Claude Code request carrying X-Trace-Id must
			// still be classified as claude-code for subscription-eligibility).
			ctx = context.WithValue(ctx, ctxClientKind, deriveClientKind(r, traceCfg))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// deriveClientKind classifies the upstream client independent of trace-id
// extraction. Spec §3.2 requires this to be decoupled from TraceSource
// precedence AND from the trace_*_enabled config flags — those flags turn
// off trace-id ingestion, not client classification. If we gated client
// detection on them, disabling claude_code_trace_enabled would silently
// force every Claude Code request into the client-restriction extra-usage
// branch, which is a major surprise for operators.
func deriveClientKind(r *http.Request, cfg config.TraceConfig) string {
	_ = cfg // kept for future per-client flags; today all checks run unconditionally.
	if id, err := tryExtractClaudeCodeTraceID(r); err == nil && id != "" {
		return types.ClientKindClaudeCode
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	if strings.Contains(ua, "opencode/") ||
		strings.TrimSpace(r.Header.Get(openCodeTraceHeader)) != "" {
		return types.ClientKindOpenCode
	}
	if isOpenClawRequest(r) {
		return types.ClientKindOpenClaw
	}
	if strings.TrimSpace(r.Header.Get(codexTraceHeader)) != "" {
		return types.ClientKindCodex
	}
	return types.ClientKindUnknown
}

// extractTraceID tries each source in priority order and returns the trace ID
// and its source. Returns ("", "") if no trace ID is found.
func extractTraceID(r *http.Request, cfg config.TraceConfig) (string, string) {
	// 1. Primary header
	if id := r.Header.Get(cfg.TraceHeader); id != "" {
		return id, types.TraceSourceHeader
	}

	// 2. Extra headers
	for _, h := range cfg.ExtraTraceHeaders {
		if id := r.Header.Get(h); id != "" {
			return id, types.TraceSourceHeader
		}
	}

	// 3. Claude Code extraction from metadata.user_id
	if cfg.ClaudeCodeTraceEnabled {
		if id, err := tryExtractClaudeCodeTraceID(r); err == nil && id != "" {
			return id, types.TraceSourceClaudeCode
		}
	}

	// 4. OpenCode extraction from X-Opencode-Session header
	if id := strings.TrimSpace(r.Header.Get(openCodeTraceHeader)); id != "" {
		return id, types.TraceSourceOpenCode
	}

	// 5. Codex/OpenCode extraction from Session_id header.
	// OpenCode's codex plugin also sends Session_id, but includes "opencode/"
	// in the User-Agent. When requests pass through the OpenCode console proxy,
	// X-Opencode-Session is stripped while Session_id survives, so we
	// disambiguate here.
	if cfg.CodexTraceEnabled {
		if id := strings.TrimSpace(r.Header.Get(codexTraceHeader)); id != "" {
			if strings.Contains(strings.ToLower(r.Header.Get("User-Agent")), "opencode/") {
				return id, types.TraceSourceOpenCode
			}
			return id, types.TraceSourceCodex
		}
	}

	// 6. OpenClaw detection via User-Agent or originator header.
	// OpenClaw does not send a session ID, so we use the API key ID
	// (set by AuthMiddleware which runs before TraceMiddleware) to group
	// all OpenClaw requests from the same key into one trace.
	if cfg.OpenClawTraceEnabled {
		if isOpenClawRequest(r) {
			if apiKey := APIKeyFromContext(r.Context()); apiKey != nil {
				return apiKey.ID, types.TraceSourceOpenClaw
			}
		}
	}

	// 7. Body field extraction via gjson paths
	if len(cfg.ExtraTraceBodyFields) > 0 {
		if id, err := tryExtractTraceIDFromBody(r, cfg.ExtraTraceBodyFields); err == nil && id != "" {
			return id, types.TraceSourceBody
		}
	}

	return "", ""
}

// tryExtractClaudeCodeTraceID reads the request body of POST /v1/messages
// requests and extracts the session UUID from the metadata.user_id field.
func tryExtractClaudeCodeTraceID(r *http.Request) (string, error) {
	if r.Method != http.MethodPost {
		return "", nil
	}
	path := r.URL.Path
	if path != "/v1/messages" {
		return "", nil
	}

	body, err := readAndRestoreBody(r)
	if err != nil || len(body) == 0 {
		return "", err
	}

	userID := gjson.GetBytes(body, "metadata.user_id").String()
	if userID == "" {
		return "", nil
	}

	return extractClaudeTraceID(userID), nil
}

// extractClaudeTraceID parses the Claude Code metadata.user_id to extract the
// session ID used as the trace ID.
//
// Current format (v2.1+): JSON string, e.g.
//
//	{"device_id":"<hex>","account_uuid":"<uuid>","session_id":"<uuid>"}
//
// Legacy format: plain string, e.g.
//
//	user_<64hex>_account_<uuid>_session_<uuid>
//	user_<64hex>_account__session_<uuid>
func extractClaudeTraceID(userID string) string {
	// Try JSON format first (current Claude Code ≥ v2.1).
	sessionID := gjson.Get(userID, "session_id").String()
	if sessionID != "" {
		return strings.TrimSpace(sessionID)
	}

	// Fall back to legacy string format.
	m := claudeUserIDLegacyPattern.FindStringSubmatch(userID)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// tryExtractTraceIDFromBody reads the request body and checks configured
// gjson paths for a trace ID value.
func tryExtractTraceIDFromBody(r *http.Request, fields []string) (string, error) {
	body, err := readAndRestoreBody(r)
	if err != nil || len(body) == 0 {
		return "", err
	}

	for _, field := range fields {
		result := gjson.GetBytes(body, field)
		if result.Exists() && result.String() != "" {
			return result.String(), nil
		}
	}

	return "", nil
}

// isOpenClawRequest returns true if the request originates from OpenClaw.
// Detection signals (either one is sufficient):
//   - User-Agent header containing "openclaw/"
//   - originator header equal to "openclaw" (sent by OpenClaw for OpenAI requests)
func isOpenClawRequest(r *http.Request) bool {
	if strings.Contains(strings.ToLower(r.Header.Get("User-Agent")), "openclaw/") {
		return true
	}
	return strings.EqualFold(r.Header.Get("originator"), "openclaw")
}

// isCompletionEndpoint returns true for endpoints that create completions.
func isCompletionEndpoint(path string) bool {
	return path == "/v1/messages" || path == "/v1/responses"
}

// readAndRestoreBody reads the entire request body and restores it so
// downstream handlers can read it again.
func readAndRestoreBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	return body, nil
}
