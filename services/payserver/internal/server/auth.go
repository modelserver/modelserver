package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelserver/modelserver/services/payserver/internal/store"
	"github.com/modelserver/modelserver/services/payserver/internal/tenant"
)

type ctxKey int

const ctxKeyTenant ctxKey = iota

// TenantFromContext returns the authenticated tenant. Panics if invoked
// from a handler that wasn't wrapped by tenantAuthMiddleware — that's a
// programmer error, not a runtime one.
func TenantFromContext(ctx context.Context) *tenant.Tenant {
	t, ok := ctx.Value(ctxKeyTenant).(*tenant.Tenant)
	if !ok {
		panic("TenantFromContext called from a non-authenticated handler")
	}
	return t
}

// dummyBcryptHash is a fixed bcrypt hash used to maintain ~constant-time
// latency when the tenant id doesn't exist (otherwise the absence of a
// VerifySecret call would leak existence via timing).
const dummyBcryptHash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

func tenantAuthMiddleware(st *store.Store, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			// RFC 7235 requires case-insensitive matching of the auth scheme;
			// some clients (curl --user, browser auth, language HTTP libs)
			// send "bearer" lowercased.
			const scheme = "Bearer "
			if len(auth) < len(scheme) || !strings.EqualFold(auth[:len(scheme)], scheme) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing bearer token"})
				return
			}
			token := auth[len(scheme):]
			id, secret, ok := strings.Cut(token, ":")
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "malformed token; expected <tenant_id>:<secret>"})
				return
			}
			t, err := st.GetTenantByID(id)
			if err != nil {
				logger.Error("auth: get tenant", "tenant_id", id, "error", err)
				_ = tenant.VerifySecret(dummyBcryptHash, secret)
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
				return
			}
			if t == nil || !t.IsActive {
				_ = tenant.VerifySecret(dummyBcryptHash, secret)
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
				return
			}
			if !tenant.VerifySecret(t.SecretHash, secret) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyTenant, t)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
