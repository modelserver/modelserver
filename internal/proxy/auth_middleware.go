package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/ratelimit"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// quotaCache caches per-user credit quota lookups (10s TTL).
// Uses -1 as sentinel for "no quota set" since 0 is a valid quota value.
var quotaCache = ratelimit.NewCreditCache(10 * time.Second)

type contextKey string

const (
	ctxAPIKey       contextKey = "apikey"
	ctxProject      contextKey = "project"
	ctxPolicy       contextKey = "policy"
	ctxSubscription contextKey = "subscription"
	ctxUserQuotaPct contextKey = "user_quota_pct"
	ctxOAuthGrantID contextKey = "oauth_grant_id"
)

// TokenIntrospectResult holds the result of a token introspection call.
type TokenIntrospectResult struct {
	// Active indicates whether the token is currently valid.
	Active bool
	// Sub is the subject (user) the token was issued to.
	Sub string
	// Ext contains arbitrary extension claims embedded in the token
	// (e.g. "project_id", "user_id").
	Ext map[string]interface{}
	// ClientID is the OAuth client that was issued this token.
	ClientID string
}

// TokenIntrospector is implemented by anything that can introspect an OAuth
// access token. Passing an implementation to AuthMiddleware enables the Hydra
// fallback path without creating an import cycle between the proxy and admin
// packages.
type TokenIntrospector interface {
	IntrospectToken(ctx context.Context, token string) (*TokenIntrospectResult, error)
}

// introspectCacheEntry holds a cached introspection result.
type introspectCacheEntry struct {
	result   *TokenIntrospectResult
	cachedAt time.Time
}

// introspectCache is a simple in-memory cache for token introspection results.
// Keys are SHA256 hashes of the raw token; values are introspectCacheEntry.
var introspectCache sync.Map

const introspectCacheTTL = 30 * time.Second

// getIntrospectCache returns a cached result if it exists and has not expired.
func getIntrospectCache(tokenHash string) (*TokenIntrospectResult, bool) {
	v, ok := introspectCache.Load(tokenHash)
	if !ok {
		return nil, false
	}
	entry := v.(introspectCacheEntry)
	if time.Since(entry.cachedAt) > introspectCacheTTL {
		introspectCache.Delete(tokenHash)
		return nil, false
	}
	return entry.result, true
}

// setIntrospectCache stores an introspection result in the cache.
func setIntrospectCache(tokenHash string, result *TokenIntrospectResult) {
	introspectCache.Store(tokenHash, introspectCacheEntry{
		result:   result,
		cachedAt: time.Now(),
	})
}

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

// UserQuotaPctFromContext returns the user's credit quota percentage from the context.
// Returns nil if no quota is set (user has full access).
func UserQuotaPctFromContext(ctx context.Context) *float64 {
	if p, ok := ctx.Value(ctxUserQuotaPct).(*float64); ok {
		return p
	}
	return nil
}

// OAuthGrantIDFromContext returns the OAuth grant ID from the request context.
// Returns empty string if the request was not authenticated via an OAuth access token.
func OAuthGrantIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(ctxOAuthGrantID).(string); ok {
		return id
	}
	return ""
}

// AuthMiddleware validates the API key and loads the associated project, policy, and subscription.
// If an encryption key is provided, it first validates the embedded HMAC checksum
// to reject malformed keys without hitting the database.
//
// Supported key format: "ms-" + 49 base62 chars (32 random + 4 checksum bytes).
//
// If introspector is non-nil and the token fails API key validation, it falls back
// to OAuth token introspection (e.g. via Hydra). On success the token's project_id
// and user_id extension claims are used to load the project and build an equivalent
// auth context.
func AuthMiddleware(st *store.Store, encKey []byte, introspector TokenIntrospector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := extractAPIKey(r)
			if rawKey == "" {
				writeProxyError(w, http.StatusUnauthorized, "missing api key")
				return
			}

			// Pre-DB validation: verify format and embedded HMAC checksum.
			isValidAPIKeyFormat := true
			if len(encKey) > 0 && strings.HasPrefix(rawKey, types.APIKeyPrefix) {
				keyBody := rawKey[len(types.APIKeyPrefix):]
				if len(keyBody) != crypto.APIKeyBodyLen || !isBase62(keyBody) {
					isValidAPIKeyFormat = false
				} else if !crypto.ValidateAPIKeyChecksum(encKey, keyBody) {
					isValidAPIKeyFormat = false
				}
			}

			if !isValidAPIKeyFormat {
				// HMAC check failed — try token introspection as fallback.
				if introspector == nil {
					writeProxyError(w, http.StatusUnauthorized, "invalid api key")
					return
				}
				handleTokenIntrospectionAuth(w, r, next, st, rawKey, introspector)
				return
			}

			// Token doesn't look like an API key at all — skip DB lookup, try introspection directly.
			if !strings.HasPrefix(rawKey, types.APIKeyPrefix) {
				if introspector != nil {
					handleTokenIntrospectionAuth(w, r, next, st, rawKey, introspector)
					return
				}
				writeProxyError(w, http.StatusUnauthorized, "invalid api key")
				return
			}

			hash := sha256.Sum256([]byte(rawKey))
			keyHash := hex.EncodeToString(hash[:])

			apiKey, err := st.GetAPIKeyByHash(keyHash)
			if err != nil {
				writeProxyError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if apiKey == nil {
				// Hash not found in DB — try token introspection as fallback.
				if introspector == nil {
					writeProxyError(w, http.StatusUnauthorized, "invalid api key")
					return
				}
				handleTokenIntrospectionAuth(w, r, next, st, rawKey, introspector)
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
					policy = plan.ToPolicy(project.ID, &subscription.StartsAt)
				}
			}

			if policy == nil {
				policy, _ = st.GetDefaultPolicy(project.ID)
			}

			// Check policy validity window.
			if policy != nil && !policy.IsActive() {
				policy = nil
			}

			// Load per-user credit quota (cached 10s).
			var userQuotaPct *float64
			quotaCacheKey := project.ID + ":" + apiKey.CreatedBy
			if cached, ok := quotaCache.Get(quotaCacheKey); ok {
				if cached >= 0 { // -1 sentinel = no quota
					v := cached
					userQuotaPct = &v
				}
			} else {
				member, memberErr := st.GetProjectMember(project.ID, apiKey.CreatedBy)
				if memberErr != nil {
					// Fail open: proceed without quota enforcement.
				} else if member != nil && member.CreditQuotaPct != nil {
					userQuotaPct = member.CreditQuotaPct
					quotaCache.Set(quotaCacheKey, *member.CreditQuotaPct)
				} else {
					quotaCache.Set(quotaCacheKey, -1) // sentinel: no quota
				}
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
			if userQuotaPct != nil {
				ctx = context.WithValue(ctx, ctxUserQuotaPct, userQuotaPct)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// handleTokenIntrospectionAuth authenticates a request via token introspection.
// It writes an error response and returns if authentication fails.
func handleTokenIntrospectionAuth(w http.ResponseWriter, r *http.Request, next http.Handler, st *store.Store, rawToken string, introspector TokenIntrospector) {
	// Use a SHA256 hash of the token as the cache key to avoid storing raw tokens.
	h := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(h[:])

	var result *TokenIntrospectResult

	if cached, ok := getIntrospectCache(tokenHash); ok {
		result = cached
	} else {
		var err error
		result, err = introspector.IntrospectToken(r.Context(), rawToken)
		if err != nil {
			writeProxyError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		setIntrospectCache(tokenHash, result)
	}

	if !result.Active {
		writeProxyError(w, http.StatusUnauthorized, "token is not active")
		return
	}

	// Extract project_id and user_id from the token's extension claims.
	projectID, _ := result.Ext["project_id"].(string)
	userID, _ := result.Ext["user_id"].(string)

	if projectID == "" {
		writeProxyError(w, http.StatusUnauthorized, "token missing project_id claim")
		return
	}

	project, err := st.GetProjectByID(projectID)
	if err != nil || project == nil {
		writeProxyError(w, http.StatusUnauthorized, "project not found")
		return
	}
	if project.Status != types.ProjectStatusActive {
		writeProxyError(w, http.StatusForbidden, "project is suspended")
		return
	}

	// Build a synthetic APIKey so the rest of the pipeline (handler, rate limit,
	// usage tracking) can operate without special-casing the Hydra path.
	syntheticKey := &types.APIKey{
		ID:        "", // empty → NULL in requests table (no real API key for token auth)
		ProjectID: project.ID,
		CreatedBy: userID,
		Name:      "hydra-token",
		Status:    types.APIKeyStatusActive,
	}

	// Resolve rate limit policy: subscription > project default.
	var policy *types.RateLimitPolicy
	var subscription *types.Subscription

	subscription, _ = st.GetActiveSubscription(project.ID)
	if subscription != nil && subscription.PlanID != "" {
		plan, _ := st.GetPlanByID(subscription.PlanID)
		if plan != nil {
			policy = plan.ToPolicy(project.ID, &subscription.StartsAt)
		}
	}
	if policy == nil {
		policy, _ = st.GetDefaultPolicy(project.ID)
	}
	if policy != nil && !policy.IsActive() {
		policy = nil
	}

	// Load per-user credit quota (cached 10s).
	var userQuotaPct *float64
	if userID != "" {
		quotaCacheKey := project.ID + ":" + userID
		if cached, ok := quotaCache.Get(quotaCacheKey); ok {
			if cached >= 0 {
				v := cached
				userQuotaPct = &v
			}
		} else {
			member, memberErr := st.GetProjectMember(project.ID, userID)
			if memberErr != nil {
				// Fail open.
			} else if member != nil && member.CreditQuotaPct != nil {
				userQuotaPct = member.CreditQuotaPct
				quotaCache.Set(quotaCacheKey, *member.CreditQuotaPct)
			} else {
				quotaCache.Set(quotaCacheKey, -1)
			}
		}
	}

	// Look up the OAuth grant so we can record oauth_grant_id on the request row.
	var oauthGrantID string
	if result.ClientID != "" {
		grant, err := st.GetOAuthGrantByProjectUserClient(project.ID, userID, result.ClientID)
		if err == nil && grant != nil {
			oauthGrantID = grant.ID
		}
	}

	ctx := context.WithValue(r.Context(), ctxAPIKey, syntheticKey)
	ctx = context.WithValue(ctx, ctxProject, project)
	if policy != nil {
		ctx = context.WithValue(ctx, ctxPolicy, policy)
	}
	if subscription != nil {
		ctx = context.WithValue(ctx, ctxSubscription, subscription)
	}
	if userQuotaPct != nil {
		ctx = context.WithValue(ctx, ctxUserQuotaPct, userQuotaPct)
	}
	if oauthGrantID != "" {
		ctx = context.WithValue(ctx, ctxOAuthGrantID, oauthGrantID)
	}
	next.ServeHTTP(w, r.WithContext(ctx))
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
