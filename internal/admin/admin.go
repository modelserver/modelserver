package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/modelserver/modelserver/internal/auth"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

type adminCtxKey string

const (
	ctxUser   adminCtxKey = "admin_user"
	ctxMember adminCtxKey = "admin_member"
)

// UserFromContext returns the authenticated user from the request context.
func UserFromContext(ctx context.Context) *types.User {
	if u, ok := ctx.Value(ctxUser).(*types.User); ok {
		return u
	}
	return nil
}

// MemberFromContext returns the project member from the request context.
// Only available inside project-scoped routes (after projectAccessMiddleware).
func MemberFromContext(ctx context.Context) *types.ProjectMember {
	if m, ok := ctx.Value(ctxMember).(*types.ProjectMember); ok {
		return m
	}
	return nil
}

// JWTAuthMiddleware validates a JWT bearer token and loads the user into context.
func JWTAuthMiddleware(jwtMgr *auth.JWTManager, st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := extractBearer(r)
			if tokenStr == "" {
				writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid authorization header")
				return
			}

			claims, err := jwtMgr.ValidateToken(tokenStr)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
				return
			}
			if claims.TokenType != "access" {
				writeError(w, http.StatusUnauthorized, "unauthorized", "expected access token")
				return
			}

			user, err := st.GetUserByID(claims.UserID)
			if err != nil || user == nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "user not found")
				return
			}
			if user.Status != types.UserStatusActive {
				writeError(w, http.StatusForbidden, "forbidden", "user account is disabled")
				return
			}

			ctx := context.WithValue(r.Context(), ctxUser, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireSuperadmin rejects requests from non-superadmin users.
func RequireSuperadmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if user == nil || !user.IsSuperadmin {
			writeError(w, http.StatusForbidden, "forbidden", "superadmin access required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// --- Shared response helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, types.ErrorResponse{
		Error: types.ErrorDetail{Code: code, Message: message},
	})
}

func writeData(w http.ResponseWriter, status int, data interface{}) {
	writeJSON(w, status, map[string]interface{}{"data": data})
}

func writeList(w http.ResponseWriter, data interface{}, total, page, perPage int) {
	totalPages := total / perPage
	if total%perPage != 0 {
		totalPages++
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": data,
		"meta": types.Meta{Total: total, Page: page, PerPage: perPage, TotalPages: totalPages},
	})
}

func parsePagination(r *http.Request) types.PaginationParams {
	p := types.DefaultPagination()
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.Page = n
		}
	}
	if v := r.URL.Query().Get("per_page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			p.PerPage = n
		}
	}
	if v := r.URL.Query().Get("sort"); v != "" {
		p.Sort = v
	}
	if v := r.URL.Query().Get("order"); v != "" {
		p.Order = v
	}
	return p
}

func decodeBody(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
