package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/types"
)

const (
	ctxTraceID     contextKey = "trace_id"
	ctxThreadID    contextKey = "thread_id"
	ctxTraceSource contextKey = "trace_source"

	codexTraceHeader = "Session_id"
)

// claudeUserIDPattern matches the Claude Code user_id format:
// user_<64 hex chars>_account__session_<uuid>
var claudeUserIDPattern = regexp.MustCompile(
	`(?i)^user_[0-9a-f]{64}_account__session_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

// TraceIDFromContext returns the trace ID from the request context.
func TraceIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(ctxTraceID).(string); ok {
		return id
	}
	return ""
}

// ThreadIDFromContext returns the thread ID from the request context.
func ThreadIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(ctxThreadID).(string); ok {
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

// TraceMiddleware extracts trace and thread IDs from multiple sources.
// If no trace ID is found from any source, no trace context is set (no auto-generation).
func TraceMiddleware(traceCfg config.TraceConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			traceID, source := extractTraceID(r, traceCfg)

			// Enforce session for POST endpoints that create completions.
			if traceCfg.RequireSession && traceID == "" {
				if r.Method == http.MethodPost && isCompletionEndpoint(r.URL.Path) {
					writeProxyError(w, http.StatusBadRequest,
						"please use a coding agent such as Claude Code, OpenCode, Codex, or Gemini CLI")
					return
				}
			}

			threadID := r.Header.Get(traceCfg.ThreadHeader)

			ctx := r.Context()
			if traceID != "" {
				ctx = context.WithValue(ctx, ctxTraceID, traceID)
				ctx = context.WithValue(ctx, ctxTraceSource, source)
			}
			if threadID != "" {
				ctx = context.WithValue(ctx, ctxThreadID, threadID)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
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

	// 4. Codex extraction from Session_id header
	if cfg.CodexTraceEnabled {
		if id := strings.TrimSpace(r.Header.Get(codexTraceHeader)); id != "" {
			return id, types.TraceSourceCodex
		}
	}

	// 5. Body field extraction via gjson paths
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

// extractClaudeTraceID parses the Claude Code user ID format to extract the
// session UUID which is used as the trace ID.
func extractClaudeTraceID(userID string) string {
	if !claudeUserIDPattern.MatchString(userID) {
		return ""
	}

	traceID := userID
	if idx := strings.LastIndex(traceID, "__"); idx >= 0 && idx+2 < len(traceID) {
		traceID = traceID[idx+2:]
	}
	if idx := strings.LastIndex(traceID, "_"); idx >= 0 && idx+1 < len(traceID) {
		traceID = traceID[idx+1:]
	}

	return strings.TrimSpace(traceID)
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
