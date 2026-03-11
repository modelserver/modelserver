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
// Supported key formats:
//   - New (base64url): "ms-" + 48 base64url chars (32 random + 4 checksum bytes)
//   - Legacy (hex):    "ms-" + 72 hex chars (64 random + 8 checksum hex)
//   - Old legacy:      "ms-" + 64 hex chars (no checksum, falls through to DB)
func AuthMiddleware(st *store.Store, encKey []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := extractAPIKey(r)
			if rawKey == "" {
				writeProxyError(w, http.StatusUnauthorized, "missing api key")
				return
			}

			// Pre-DB validation: verify embedded HMAC checksum to reject brute-force attempts.
			if len(encKey) > 0 && strings.HasPrefix(rawKey, types.APIKeyPrefix) {
				keyBody := rawKey[len(types.APIKeyPrefix):]
				bodyLen := len(keyBody)

				switch {
				case bodyLen == 48:
					// New format (base64url): 36 raw bytes → 48 base64url chars.
					if !crypto.ValidateAPIKeyChecksum(encKey, keyBody) {
						writeProxyError(w, http.StatusUnauthorized, "invalid api key")
						return
					}
				case bodyLen == 72:
					// Legacy hex format with checksum: 64 random hex + 8 checksum hex.
					if !crypto.ValidateAPIKeyChecksumHex(encKey, keyBody) {
						writeProxyError(w, http.StatusUnauthorized, "invalid api key")
						return
					}
				case bodyLen == 64:
					// Old legacy hex format without checksum — skip pre-check, fall through to DB.
				default:
					// Invalid length — reject immediately.
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

			// Resolve rate limit policy: key-specific > subscription > project default.
			var policy *types.RateLimitPolicy
			var subscription *types.Subscription

			if apiKey.RateLimitPolicyID != "" {
				policy, _ = st.GetPolicyByID(apiKey.RateLimitPolicyID)
			}

			if policy == nil {
				// Try loading active subscription for this project.
				subscription, _ = st.GetActiveSubscription(project.ID)
				if subscription != nil && subscription.PlanID != "" {
					plan, _ := st.GetPlanByID(subscription.PlanID)
					if plan != nil {
						policy = plan.ToPolicy(project.ID)
					}
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
