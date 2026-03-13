package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/modelserver/modelserver/internal/ratelimit"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// RateLimitMiddleware checks rate limits before allowing requests through.
// Uses the composite rate limiter with in-memory counters + DB credit checks.
// Rejected requests are logged to the request store with status "rate_limited".
func RateLimitMiddleware(limiter ratelimit.RateLimiter, st *store.Store, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			policy := PolicyFromContext(r.Context())
			if policy == nil {
				next.ServeHTTP(w, r)
				return
			}

			apiKey := APIKeyFromContext(r.Context())
			project := ProjectFromContext(r.Context())
			if apiKey == nil || project == nil {
				next.ServeHTTP(w, r)
				return
			}

			allowed, retryAfter, err := limiter.PreCheck(r.Context(), project.ID, apiKey.ID, "", policy)
			if err != nil {
				logger.Error("rate limit check error", "error", err)
				next.ServeHTTP(w, r) // Fail open.
				return
			}

			if !allowed {
				logger.Warn("rate limit exceeded",
					"project_id", project.ID,
					"api_key_id", apiKey.ID,
				)

				// Log the rejected request.
				model := peekModel(r)
				traceID := TraceIDFromContext(r.Context())
				clientIP := r.RemoteAddr
				errMsg := fmt.Sprintf("rate limit exceeded, retry after %ds", int(retryAfter.Seconds()))

				req := &types.Request{
					ProjectID:    project.ID,
					APIKeyID:     apiKey.ID,
					TraceID:      traceID,
					Provider:     "",
					Model:        model,
					Status:       types.RequestStatusRateLimited,
					ClientIP:     clientIP,
					ErrorMessage: errMsg,
				}
				go st.CreateRequest(req)

				writeRateLimitError(w, retryAfter)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// peekModel reads the model field from the JSON request body without consuming it.
func peekModel(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var shape struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &shape)
	return shape.Model
}

func writeRateLimitError(w http.ResponseWriter, retryAfter time.Duration) {
	retrySeconds := int(retryAfter.Seconds())
	if retrySeconds < 1 {
		retrySeconds = 1
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds))
	w.WriteHeader(http.StatusTooManyRequests)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    "rate_limit_error",
			"message": fmt.Sprintf("rate limit exceeded, retry after %ds", retrySeconds),
		},
	})
}
