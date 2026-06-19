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
	ctxTraceID              contextKey = "trace_id"
	ctxTraceSource          contextKey = "trace_source"
	ctxClientKind           contextKey = "client_kind"
	ctxClaudeAgentSDKSource contextKey = "claude_agent_sdk_source"
)

const (
	openCodeTraceHeader = "X-Opencode-Session"
	// codexTraceHeader is the hyphenated form emitted by codex CLI ≥0.135.0
	// (openai/codex#22193 dropped the underscored alias). codexTraceHeaderLegacy
	// is kept so requests from older codex CLIs (≤0.124.x) and from clients
	// that still piggyback on the legacy spelling continue to correlate.
	codexTraceHeader       = "Session-Id"
	codexTraceHeaderLegacy = "Session_id"
)

// codexSessionIDFromRequest returns the codex CLI session id from whichever
// spelling the client used, preferring the modern hyphenated form.
func codexSessionIDFromRequest(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get(codexTraceHeader)); id != "" {
		return id
	}
	return strings.TrimSpace(r.Header.Get(codexTraceHeaderLegacy))
}

// claudeUserIDLegacyPattern matches the legacy Claude Code user_id string format:
// user_<64 hex chars>_account_<uuid>_session_<uuid>
// Also matches the variant without account UUID:
// user_<64 hex chars>_account__session_<uuid>
var claudeUserIDLegacyPattern = regexp.MustCompile(
	`(?i)^user_[0-9a-f]{64}_account_(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})?_session_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`,
)

// claudeAgentSDKSystemPrompts is the set of system prompts that Anthropic's
// /v1/messages API recognises as first-party Claude Code traffic when
// presented with an OAuth subscriber token. Background hooks shipped with
// the CC CLI (e.g. the security-guidance plugin's Python hooks calling
// urllib.request.urlopen with UA "Python-urllib/3.x") emit a minimal
// request body — no metadata.user_id, no CCH header — using one of these
// prompts. Matching them here mirrors Anthropic's own gate so plugin
// traffic consumes subscription budget alongside the user's main session,
// rather than being mis-classified as a third-party client and 429'd by
// ExtraUsageGuardMiddleware.
//
// Must be byte-exact. "appending text fails the check" — see
// claude-plugins-official/security-guidance/hooks/llm.py:133-137.
var claudeAgentSDKSystemPrompts = map[string]struct{}{
	"You are a Claude agent, built on Anthropic's Claude Agent SDK.": {},
}

// ClaudeAgentSDK source labels written to requests.metadata when an
// SDK-shaped request is admitted as ClaudeCode. The "plugin:*" labels are
// best-effort attributions based on multi-signal fingerprints; the generic
// "claude-agent-sdk" label is the fallback when the system prompt matches
// but no narrower signature does.
const (
	// ClaudeAgentSDKSourceSecurityGuidance is the fingerprint triad of the
	// security-guidance plugin's Python review hook: SDK system prompt +
	// Python-urllib UA + structured-outputs json_schema output_config. The
	// hook lives at hooks/llm.py in the plugin and is the dominant emitter
	// of Python-urllib traffic on a normal CC install.
	ClaudeAgentSDKSourceSecurityGuidance = "plugin:security-guidance"
	// ClaudeAgentSDKSourceGeneric tags a request whose system prompt is in
	// the Agent SDK allowlist but whose other signals don't match any
	// known plugin signature — e.g. a user's own Agent SDK script.
	ClaudeAgentSDKSourceGeneric = "claude-agent-sdk"
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
			kind, sdkSource := deriveClientKind(r, traceCfg)
			ctx = context.WithValue(ctx, ctxClientKind, kind)
			if sdkSource != "" {
				ctx = context.WithValue(ctx, ctxClaudeAgentSDKSource, sdkSource)
			}
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
// deriveClientKind also returns the Claude Agent SDK source label when one
// matched (e.g. "plugin:security-guidance"), so the caller can stash it on
// the context without re-parsing the body. The label is "" for non-SDK
// classifications.
func deriveClientKind(r *http.Request, cfg config.TraceConfig) (kind, sdkSource string) {
	_ = cfg // kept for future per-client flags; today all checks run unconditionally.
	if id, err := tryExtractClaudeCodeTraceID(r); err == nil && id != "" {
		return types.ClientKindClaudeCode, ""
	}
	// Claude Agent SDK / first-party CC plugin probe: requests built by
	// hooks like security-guidance never carry metadata.user_id, but their
	// system prompt is the SDK-attested string Anthropic uses as its own
	// OAuth gate. Treat them as Claude Code so they consume subscription,
	// and remember which plugin family matched so the request row can carry
	// that attribution.
	if src := classifyClaudeAgentSDK(r); src != "" {
		return types.ClientKindClaudeCode, src
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	if strings.Contains(ua, "opencode/") ||
		strings.TrimSpace(r.Header.Get(openCodeTraceHeader)) != "" {
		return types.ClientKindOpenCode, ""
	}
	// Claude Desktop (Anthropic's Electron app) ships a Chromium-style UA
	// containing both "Claude/<version>" (the product segment) and
	// "Electron/<version>" (the runtime segment). The CLI's UA is the
	// unrelated "claude-cli/<version> (external, cli)" string set in
	// normalize_identity.go, so requiring both substrings keeps CLI traffic
	// out of this branch even if its UA is ever shortened to "claude/".
	if strings.Contains(ua, "claude/") && strings.Contains(ua, "electron/") {
		return types.ClientKindClaudeDesktop, ""
	}
	if isOpenClawRequest(r) {
		return types.ClientKindOpenClaw, ""
	}
	if codexSessionIDFromRequest(r) != "" {
		return types.ClientKindCodex, ""
	}
	return types.ClientKindUnknown, ""
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

	// 5. Codex/OpenCode extraction from the codex session-id header (both the
	// modern hyphenated form sent by codex ≥0.135.0 and the underscored
	// legacy alias). OpenCode's codex plugin also sends this header but
	// includes "opencode/" in the User-Agent — when requests pass through
	// the OpenCode console proxy, X-Opencode-Session is stripped while the
	// codex session-id header survives, so we disambiguate here.
	if cfg.CodexTraceEnabled {
		if id := codexSessionIDFromRequest(r); id != "" {
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

// classifyClaudeAgentSDK inspects a POST /v1/messages request for the
// fingerprint of a first-party Claude Code plugin / Agent SDK caller.
// Returns one of:
//
//   - "" — not an SDK request
//   - ClaudeAgentSDKSourceSecurityGuidance — strong triad signature for the
//     bundled security-guidance Python hook (SDK system prompt + Python-urllib
//     UA + structured-outputs json_schema output_config)
//   - ClaudeAgentSDKSourceGeneric — SDK system prompt matched but the
//     narrower plugin signature did not (e.g. a user-authored Agent SDK
//     script, or a non-Python SDK consumer)
//
// Gated to POST /v1/messages — every other endpoint short-circuits without
// touching the body. Body access uses readAndRestoreBody so downstream
// handlers still see the full payload.
//
// The Agent SDK plugin emits "system" as a plain string. The CC CLI emits
// "system" as an array of {type:"text",text:"..."} blocks; we walk that
// shape too so a future SDK revision that wraps the prompt in an array
// stays classified correctly without a code change.
func classifyClaudeAgentSDK(r *http.Request) string {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		return ""
	}
	body, err := readAndRestoreBody(r)
	if err != nil || len(body) == 0 {
		return ""
	}
	if !systemPromptInAgentSDKAllowlist(body) {
		return ""
	}

	// Plugin-specific narrowing: the security-guidance hook is identifiable
	// by the combination of Python-urllib UA and the structured-outputs
	// output_config.format.type=json_schema body shape it always emits.
	// Either signal alone is too weak; together they are unique on the
	// public CC plugin surface today.
	ua := r.Header.Get("User-Agent")
	hasPythonUrllibUA := strings.HasPrefix(ua, "Python-urllib/")
	hasJSONSchemaOutput := gjson.GetBytes(body, "output_config.format.type").Str == "json_schema"
	if hasPythonUrllibUA && hasJSONSchemaOutput {
		return ClaudeAgentSDKSourceSecurityGuidance
	}
	return ClaudeAgentSDKSourceGeneric
}

// systemPromptInAgentSDKAllowlist returns true when the body's top-level
// "system" field — whether a bare string or an array of {type:"text",
// text:"..."} blocks — exactly matches an entry in
// claudeAgentSDKSystemPrompts.
func systemPromptInAgentSDKAllowlist(body []byte) bool {
	sys := gjson.GetBytes(body, "system")
	switch {
	case sys.Type == gjson.String:
		_, ok := claudeAgentSDKSystemPrompts[sys.Str]
		return ok
	case sys.IsArray():
		for _, item := range sys.Array() {
			t := item.Get("text")
			if t.Exists() && t.Type == gjson.String {
				if _, ok := claudeAgentSDKSystemPrompts[t.Str]; ok {
					return true
				}
			}
		}
	}
	return false
}

// ClaudeAgentSDKSourceFromContext returns the source label written by
// TraceMiddleware when a request was admitted as ClaudeCode via the Agent
// SDK system-prompt allowlist. Returns "" for non-SDK requests.
func ClaudeAgentSDKSourceFromContext(ctx context.Context) string {
	if s, ok := ctx.Value(ctxClaudeAgentSDKSource).(string); ok {
		return s
	}
	return ""
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
