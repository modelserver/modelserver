package proxy

import (
	"context"
	"net/http"

	"github.com/modelserver/modelserver/internal/metrics"
	"github.com/modelserver/modelserver/internal/types"
)

const ctxSubscriptionEligibility contextKey = "subscription_eligibility"

// SubscriptionEligibility captures whether the current request is allowed to
// consume the project's subscription budget, and if not, why. Produced by
// SubscriptionEligibilityMiddleware; consumed by RateLimitMiddleware to
// decide between credit-aware PreCheck and classic-only bypass.
type SubscriptionEligibility struct {
	Eligible bool
	// Reason is one of types.ExtraUsageReasonClientRestriction (when
	// !Eligible) or "" (when Eligible). Other reasons (rate_limited) are
	// decided later by RateLimitMiddleware.
	Reason string
}

// SubscriptionEligibilityFromContext reads the eligibility decision. The
// default (no MW in chain) is "eligible" so downstream code behaves exactly
// as before when the feature is disabled.
func SubscriptionEligibilityFromContext(ctx context.Context) SubscriptionEligibility {
	if e, ok := ctx.Value(ctxSubscriptionEligibility).(SubscriptionEligibility); ok {
		return e
	}
	return SubscriptionEligibility{Eligible: true}
}

// SubscriptionEligibilityMiddleware enforces the publisher/client-kind
// policy: anthropic-publisher models are only eligible for subscription
// consumption when the client is Claude Code. Non-anthropic publishers are
// always eligible (today). The middleware never short-circuits the request —
// it only annotates the context. When the resolved model is missing we
// record a metric and default to "eligible" so traffic is never dropped due
// to data holes.
func SubscriptionEligibilityMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			m := ModelFromContext(r.Context())
			kind := ClientKindFromContext(r.Context())

			eligible := true
			reason := ""
			switch {
			case m == nil:
				// Pre-resolve stage (GET /models etc.) — no decision needed.
			case m.Publisher == "":
				// Data hole: treat as eligible but surface to ops via metric.
				metrics.IncExtraUsageMissingPublisher(m.Name)
			case m.Publisher == types.PublisherAnthropic && kind != types.ClientKindClaudeCode:
				eligible = false
				reason = types.ExtraUsageReasonClientRestriction
			}

			ctx := context.WithValue(r.Context(), ctxSubscriptionEligibility,
				SubscriptionEligibility{Eligible: eligible, Reason: reason})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
