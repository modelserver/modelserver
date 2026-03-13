package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

type contextKey string

const (
	ctxAPIKey       contextKey = "apikey"
	ctxProject      contextKey = "project"
	ctxPolicy       contextKey = "policy"
	ctxSubscription contextKey = "subscription"
)

// APIKeyFromContext returns the API key from the request context.
func APIKeyFromContext(ctx context.Context) *types.APIKey {
	if k, ok := ctx.Value(ctxAPIKey).(*types.APIKey); ok {
		return k
	}
	return nil
}

// ProjectFromContext returns the project from the request context.
func ProjectFromContext(ctx context.Context) *types.Project {
	if p, ok := ctx.Value(ctxProject).(*types.Project); ok {
		return p
	}
	return nil
}

// PolicyFromContext returns the rate limit policy from the request context.
func PolicyFromContext(ctx context.Context) *types.RateLimitPolicy {
	if p, ok := ctx.Value(ctxPolicy).(*types.RateLimitPolicy); ok {
		return p
	}
	return nil
}

// SubscriptionFromContext returns the active subscription from the request context.
func SubscriptionFromContext(ctx context.Context) *types.Subscription {
	if s, ok := ctx.Value(ctxSubscription).(*types.Subscription); ok {
		return s
	}
	return nil
}

// AuthMiddleware validates the API key and loads the associated project, policy, and subscription.
// If an encryption key is provided, it first validates the embedded HMAC checksum
// to reject malformed keys without hitting the database.
//
// Supported key format: "ms-" + 49 base62 chars (32 random + 4 checksum bytes).
func AuthMiddleware(st *store.Store, encKey []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := extractAPIKey(r)
			if rawKey == "" {
				writeProxyError(w, http.StatusUnauthorized, "missing api key")
				return
			}

			// Pre-DB validation: verify format and embedded HMAC checksum.
			if len(encKey) > 0 && strings.HasPrefix(rawKey, types.APIKeyPrefix) {
				keyBody := rawKey[len(types.APIKeyPrefix):]
				if len(keyBody) != crypto.APIKeyBodyLen || !isBase62(keyBody) {
					writeProxyError(w, http.StatusUnauthorized, "invalid api key")
					return
				}
				if !crypto.ValidateAPIKeyChecksum(encKey, keyBody) {
					writeProxyError(w, http.StatusUnauthorized, "invalid api key")
					return
				}
			}

			hash := sha256.Sum256([]byte(rawKey))
			keyHash := hex.EncodeToString(hash[:])

			apiKey, err := st.GetAPIKeyByHash(keyHash)
			if err != nil {
				writeProxyError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if apiKey == nil {
				writeProxyError(w, http.StatusUnauthorized, "invalid api key")
				return
			}

			if apiKey.Status != types.APIKeyStatusActive {
				writeProxyError(w, http.StatusUnauthorized, "api key is "+apiKey.Status)
				return
			}

			if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
				writeProxyError(w, http.StatusUnauthorized, "api key expired")
				return
			}

			project, err := st.GetProjectByID(apiKey.ProjectID)
			if err != nil || project == nil {
				writeProxyError(w, http.StatusInternalServerError, "project not found")
				return
			}
			if project.Status != types.ProjectStatusActive {
				writeProxyError(w, http.StatusForbidden, "project is suspended")
				return
			}

			// Resolve rate limit policy: subscription > project default.
			var policy *types.RateLimitPolicy
			var subscription *types.Subscription

			// Try loading active subscription for this project.
			subscription, _ = st.GetActiveSubscription(project.ID)
			if subscription != nil && subscription.PlanID != "" {
				plan, _ := st.GetPlanByID(subscription.PlanID)
				if plan != nil {
					policy = plan.ToPolicy(project.ID)
				}
			}

			if policy == nil {
				policy, _ = st.GetDefaultPolicy(project.ID)
			}

			// Check policy validity window.
			if policy != nil && !policy.IsActive() {
				policy = nil
			}

			go st.UpdateAPIKeyLastUsed(apiKey.ID)

			ctx := context.WithValue(r.Context(), ctxAPIKey, apiKey)
			ctx = context.WithValue(ctx, ctxProject, project)
			if policy != nil {
				ctx = context.WithValue(ctx, ctxPolicy, policy)
			}
			if subscription != nil {
				ctx = context.WithValue(ctx, ctxSubscription, subscription)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractAPIKey(r *http.Request) string {
	if key := r.Header.Get("x-api-key"); key != "" {
		return key
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

func isBase62(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}
