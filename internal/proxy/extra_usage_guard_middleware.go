package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/metrics"
	"github.com/modelserver/modelserver/internal/store"
)

const (
	ctxExtraUsageIntent  contextKey = "extra_usage_intent"
	ctxExtraUsageContext contextKey = "extra_usage_context"
)

// ExtraUsageIntent marks a request that would otherwise have been blocked as
// a candidate for extra-usage fulfilment. Set by RateLimitMiddleware and
// consumed by ExtraUsageGuardMiddleware.
type ExtraUsageIntent struct {
	// Reason is "rate_limited" (credit window depleted) or
	// "client_restriction" (publisher/kind mismatch).
	Reason string
}

// ExtraUsageContext is written by the guard after all checks pass. The
// executor reads this to trigger post-request settlement.
type ExtraUsageContext struct {
	Reason            string
	BalanceFenAtEntry int64
	MonthlyLimitFen   int64
	MonthlySpentFen   int64
}

// withExtraUsageIntent tags the context with an intent reason. Safe to call
// with an empty reason — in that case no tag is attached.
func withExtraUsageIntent(ctx context.Context, reason string) context.Context {
	if reason == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxExtraUsageIntent, ExtraUsageIntent{Reason: reason})
}

// extraUsageIntentFromContext returns the intent and whether one is present.
func extraUsageIntentFromContext(ctx context.Context) (ExtraUsageIntent, bool) {
	i, ok := ctx.Value(ctxExtraUsageIntent).(ExtraUsageIntent)
	return i, ok
}

// withExtraUsageContext attaches the guard-approved context that the
// executor's settle hook reads to trigger billing.
func withExtraUsageContext(ctx context.Context, c ExtraUsageContext) context.Context {
	return context.WithValue(ctx, ctxExtraUsageContext, c)
}

// ExtraUsageContextFromContext returns the guard-approved settlement context
// and whether the request has been routed through extra usage.
func ExtraUsageContextFromContext(ctx context.Context) (ExtraUsageContext, bool) {
	c, ok := ctx.Value(ctxExtraUsageContext).(ExtraUsageContext)
	return c, ok
}

// ExtraUsageGuardMiddleware checks the global circuit breaker and per-project
// settings (enabled / balance / monthly limit) when an extra-usage intent was
// set upstream. It either approves (attaching ExtraUsageContext for the
// executor) or rejects with HTTP 429 + descriptive headers/body.
//
// When no intent is present the middleware is a no-op.
func ExtraUsageGuardMiddleware(cfg config.ExtraUsageConfig, st *store.Store, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			intent, has := extraUsageIntentFromContext(r.Context())
			if !has {
				next.ServeHTTP(w, r)
				return
			}

			if !cfg.Enabled {
				writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
					Enabled: false,
					Message: "extra usage temporarily disabled",
				})
				recordExtraUsageResult(intent.Reason, "rejected")
				return
			}

			project := ProjectFromContext(r.Context())
			if project == nil {
				// Auth should have populated this; fail safe.
				writeExtraUsageRejected(w, http.StatusInternalServerError, intent.Reason, guardStateRejected{
					Message: "missing project context",
				})
				return
			}

			settings, err := st.GetExtraUsageSettings(project.ID)
			if err != nil {
				logger.Error("extra_usage settings lookup failed", "error", err, "project_id", project.ID)
				writeExtraUsageRejected(w, http.StatusInternalServerError, intent.Reason, guardStateRejected{
					Message: "extra usage lookup failed",
				})
				return
			}

			if settings == nil || !settings.Enabled {
				writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
					Enabled: false,
					Message: rejectedMessage(intent.Reason, "not_enabled"),
				})
				recordExtraUsageResult(intent.Reason, "rejected")
				return
			}
			if settings.BalanceFen <= 0 {
				writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
					Enabled:    true,
					BalanceFen: settings.BalanceFen,
					Message:    rejectedMessage(intent.Reason, "balance_depleted"),
				})
				recordExtraUsageResult(intent.Reason, "rejected")
				return
			}

			var monthlySpent int64
			if settings.MonthlyLimitFen > 0 {
				spent, err := st.GetMonthlyExtraSpendFen(project.ID)
				if err != nil {
					logger.Error("extra_usage monthly spend query failed", "error", err, "project_id", project.ID)
					writeExtraUsageRejected(w, http.StatusInternalServerError, intent.Reason, guardStateRejected{
						Message: "extra usage monthly check failed",
					})
					return
				}
				if spent >= settings.MonthlyLimitFen {
					writeExtraUsageRejected(w, http.StatusTooManyRequests, intent.Reason, guardStateRejected{
						Enabled:    true,
						BalanceFen: settings.BalanceFen,
						Message:    rejectedMessage(intent.Reason, "monthly_limit"),
					})
					recordExtraUsageResult(intent.Reason, "rejected")
					return
				}
				monthlySpent = spent
			}

			ctx := withExtraUsageContext(r.Context(), ExtraUsageContext{
				Reason:            intent.Reason,
				BalanceFenAtEntry: settings.BalanceFen,
				MonthlyLimitFen:   settings.MonthlyLimitFen,
				MonthlySpentFen:   monthlySpent,
			})
			recordExtraUsageResult(intent.Reason, "allowed")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type guardStateRejected struct {
	Enabled    bool
	BalanceFen int64
	Message    string
}

// writeExtraUsageRejected renders a 429 (typically) response with descriptive
// extra-usage headers and a JSON body. The envelope shape is the same as
// writeRateLimitError so client SDKs parsing 429 responses keep working.
func writeExtraUsageRejected(w http.ResponseWriter, status int, reason string, st guardStateRejected) {
	w.Header().Set("X-Extra-Usage-Required", "true")
	w.Header().Set("X-Extra-Usage-Reason", reason)
	w.Header().Set("X-Extra-Usage-Enabled", strconv.FormatBool(st.Enabled))
	w.Header().Set("X-Extra-Usage-Balance-Fen", strconv.FormatInt(st.BalanceFen, 10))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    "rate_limit_error",
			"message": st.Message,
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

// rejectedMessage returns the user-facing message mapped from the (reason,
// sub-reason) pair. Keeps §5.4 phrasing in one place so future edits don't
// drift per-handler.
func rejectedMessage(reason, subReason string) string {
	switch reason {
	case "client_restriction":
		switch subReason {
		case "not_enabled":
			return "this client cannot use subscription for anthropic models; enable extra usage"
		case "balance_depleted":
			return "extra usage balance depleted for this client restriction"
		case "monthly_limit":
			return "extra usage monthly limit reached for this client restriction"
		}
	case "rate_limited":
		switch subReason {
		case "not_enabled":
			return "rate limit reached; enable extra usage to continue"
		case "balance_depleted":
			return "rate limit reached; extra usage balance depleted"
		case "monthly_limit":
			return "rate limit reached; extra usage monthly limit reached"
		}
	}
	return fmt.Sprintf("extra usage unavailable: %s", subReason)
}

// recordExtraUsageResult bumps the Prometheus counter for guard decisions.
func recordExtraUsageResult(reason, result string) {
	metrics.IncExtraUsageRequest(reason, result)
}
