package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

type contextKey string

const (
	ctxAPIKey  contextKey = "apikey"
	ctxProject contextKey = "project"
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

// AuthMiddleware validates the API key and loads the associated project.
func AuthMiddleware(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := extractAPIKey(r)
			if rawKey == "" {
				writeProxyError(w, http.StatusUnauthorized, "missing api key")
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

			go st.UpdateAPIKeyLastUsed(apiKey.ID)

			ctx := context.WithValue(r.Context(), ctxAPIKey, apiKey)
			ctx = context.WithValue(ctx, ctxProject, project)
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
