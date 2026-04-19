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

// RateLimitMiddleware performs rate-limit pre-checks and decides whether to
// route the request to subscription, extra-usage (rate_limited /
// client_restriction), or a hard-limit rejection. When the subscription
// eligibility middleware decided the client cannot consume subscription, we
// still run classic-only checks so upstream providers are protected against
// bursts.
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

			elig := SubscriptionEligibilityFromContext(r.Context())

			// Client-restriction branch: classic-only bypass + intent set.
			if !elig.Eligible {
				res, err := limiter.PreCheckClassicOnly(r.Context(), project.ID, apiKey.ID, "", policy)
				if err != nil {
					logger.Error("classic-only precheck error", "error", err)
					next.ServeHTTP(w, r) // fail open on limiter infra error
					return
				}
				if !res.Allowed {
					logRateLimitRejection(st, r, project, apiKey, res.RetryAfter)
					writeRateLimitError(w, res.RetryAfter)
					return
				}
				ctx := withExtraUsageIntent(r.Context(), elig.Reason)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Normal branch: full pre-check.
			res, err := limiter.PreCheck(r.Context(), project.ID, apiKey.ID, "", policy)
			if err != nil {
				logger.Error("rate limit check error", "error", err)
				next.ServeHTTP(w, r) // fail open
				return
			}

			if res.Allowed {
				// Per-user credit quota check (unchanged behaviour).
				if quotaPct := UserQuotaPctFromContext(r.Context()); quotaPct != nil {
					uAllowed, uRetryAfter, uErr := limiter.CheckUserQuota(r.Context(), project.ID, apiKey.CreatedBy, *quotaPct, policy)
					if uErr != nil {
						logger.Error("user quota check error", "error", uErr)
					} else if !uAllowed {
						logRateLimitRejectionMsg(st, r, project, apiKey, fmt.Sprintf("user quota exceeded, retry after %ds", int(uRetryAfter.Seconds())))
						writeRateLimitError(w, uRetryAfter)
						return
					}
				}
				next.ServeHTTP(w, r)
				return
			}

			// Credit-only hit → candidate for extra-usage.
			if res.LimitType == ratelimit.LimitTypeCredit {
				ctx := withExtraUsageIntent(r.Context(), types.ExtraUsageReasonRateLimited)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Classic hit (or unknown) → hard 429.
			logRateLimitRejection(st, r, project, apiKey, res.RetryAfter)
			writeRateLimitError(w, res.RetryAfter)
		})
	}
}

func logRateLimitRejection(st *store.Store, r *http.Request, project *types.Project, apiKey *types.APIKey, retryAfter time.Duration) {
	msg := fmt.Sprintf("rate limit exceeded, retry after %ds", int(retryAfter.Seconds()))
	logRateLimitRejectionMsg(st, r, project, apiKey, msg)
}

func logRateLimitRejectionMsg(st *store.Store, r *http.Request, project *types.Project, apiKey *types.APIKey, msg string) {
	model := peekModel(r)
	traceID := TraceIDFromContext(r.Context())
	req := &types.Request{
		ProjectID:    project.ID,
		APIKeyID:     apiKey.ID,
		CreatedBy:    apiKey.CreatedBy,
		TraceID:      traceID,
		Provider:     "",
		Model:        model,
		Status:       types.RequestStatusRateLimited,
		ClientIP:     r.RemoteAddr,
		ErrorMessage: msg,
	}
	go st.CreateRequest(req)
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
	// Preserved from pre-extra-usage behaviour: classic rate-limit rejections
	// use 400 so existing Anthropic SDKs surface them as APIError with the
	// canonical message. Extra-usage guard rejections (see writeExtraUsageRejected)
	// use 429 per spec §5.4.
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    "rate_limit_error",
			"message": fmt.Sprintf("rate limit exceeded, retry after %ds", retrySeconds),
		},
	})
}
