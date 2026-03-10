package proxy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// RateLimitMiddleware checks credit and classic rate limits before allowing requests through.
// Must be placed after AuthMiddleware so policy and project are available in context.
func RateLimitMiddleware(st *store.Store, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			policy := PolicyFromContext(r.Context())
			if policy == nil {
				// No policy means no limits to enforce.
				next.ServeHTTP(w, r)
				return
			}

			apiKey := APIKeyFromContext(r.Context())
			project := ProjectFromContext(r.Context())
			if apiKey == nil || project == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Check credit-based limits.
			if denial := checkCreditLimits(st, policy, project.ID, apiKey.ID); denial != nil {
				logger.Warn("rate limit exceeded",
					"project_id", project.ID,
					"api_key_id", apiKey.ID,
					"rule_window", denial.window,
					"used", denial.used,
					"limit", denial.limit,
				)
				writeRateLimitError(w, denial)
				return
			}

			// Check classic limits (RPM, RPD, TPM, TPD).
			if denial := checkClassicLimits(st, policy, project.ID, apiKey.ID); denial != nil {
				logger.Warn("classic rate limit exceeded",
					"project_id", project.ID,
					"api_key_id", apiKey.ID,
					"metric", denial.window,
					"used", denial.used,
					"limit", denial.limit,
				)
				writeRateLimitError(w, denial)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

type rateLimitDenial struct {
	window   string
	limit    int64
	used     float64
	retryAt  time.Time
	message  string
}

func checkCreditLimits(st *store.Store, policy *types.RateLimitPolicy, projectID, apiKeyID string) *rateLimitDenial {
	now := time.Now().UTC()
	for _, rule := range policy.CreditRules {
		windowStart := computeWindowStart(now, rule.Window, rule.WindowType)
		windowEnd := computeWindowEnd(windowStart, rule.Window, rule.WindowType)

		var used float64
		var err error
		if rule.EffectiveScope() == types.CreditScopeProject {
			used, err = st.SumCreditsInWindowByProject(projectID, windowStart)
		} else {
			used, err = st.SumCreditsInWindow(apiKeyID, windowStart)
		}
		if err != nil {
			continue // Don't block on query errors.
		}

		if used >= float64(rule.MaxCredits) {
			return &rateLimitDenial{
				window:  rule.Window,
				limit:   rule.MaxCredits,
				used:    used,
				retryAt: windowEnd,
				message: fmt.Sprintf("credit limit exceeded for %s window (%s)", rule.Window, rule.WindowType),
			}
		}
	}
	return nil
}

func checkClassicLimits(st *store.Store, policy *types.RateLimitPolicy, projectID, apiKeyID string) *rateLimitDenial {
	now := time.Now().UTC()
	for _, rule := range policy.ClassicRules {
		windowStart, windowEnd := classicWindow(now, rule.Metric)
		if windowStart.IsZero() {
			continue
		}

		var current int64
		var err error

		switch rule.Metric {
		case "rpm", "rpd":
			current, err = st.CountRequestsInWindowByProject(projectID, windowStart)
		case "tpm", "tpd":
			current, err = st.SumTokensInWindowByProject(projectID, windowStart)
		default:
			continue
		}
		if err != nil {
			continue
		}

		if current >= rule.Limit {
			return &rateLimitDenial{
				window:  rule.Metric,
				limit:   rule.Limit,
				used:    float64(current),
				retryAt: windowEnd,
				message: fmt.Sprintf("%s limit exceeded", rule.Metric),
			}
		}
	}
	return nil
}

// classicWindow returns the start and end of the window for a classic metric.
func classicWindow(now time.Time, metric string) (time.Time, time.Time) {
	switch metric {
	case "rpm":
		start := now.Add(-time.Minute)
		return start, now.Add(time.Minute)
	case "rpd":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 0, 1)
	case "tpm":
		start := now.Add(-time.Minute)
		return start, now.Add(time.Minute)
	case "tpd":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 0, 1)
	default:
		return time.Time{}, time.Time{}
	}
}

func writeRateLimitError(w http.ResponseWriter, denial *rateLimitDenial) {
	retryAfter := int(time.Until(denial.retryAt).Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(denial.limit, 10))
	w.Header().Set("X-RateLimit-Used", strconv.FormatFloat(denial.used, 'f', 2, 64))
	w.Header().Set("X-RateLimit-Reset", denial.retryAt.Format(time.RFC3339))
	w.WriteHeader(http.StatusTooManyRequests)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    "rate_limit_error",
			"message": denial.message,
		},
	})
}
