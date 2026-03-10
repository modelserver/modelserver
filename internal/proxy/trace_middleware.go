package proxy

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/modelserver/modelserver/internal/config"
)

const (
	ctxTraceID  contextKey = "trace_id"
	ctxThreadID contextKey = "thread_id"
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

// TraceMiddleware extracts or generates trace and thread IDs.
func TraceMiddleware(traceCfg config.TraceConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			traceID := r.Header.Get(traceCfg.TraceHeader)
			if traceID == "" {
				traceID = uuid.New().String()
			}

			threadID := r.Header.Get(traceCfg.ThreadHeader)

			ctx := context.WithValue(r.Context(), ctxTraceID, traceID)
			ctx = context.WithValue(ctx, ctxThreadID, threadID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
