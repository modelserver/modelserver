package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

type adminCtxKey string

const ctxUser adminCtxKey = "admin_user"

// Claims defines the JWT payload.
type Claims struct {
	UserID       string `json:"uid"`
	IsSuperadmin bool   `json:"sa"`
	jwt.RegisteredClaims
}

// UserFromContext returns the authenticated user from the request context.
func UserFromContext(ctx context.Context) *types.User {
	if u, ok := ctx.Value(ctxUser).(*types.User); ok {
		return u
	}
	return nil
}

// AuthMiddleware validates a JWT bearer token and loads the user into context.
func AuthMiddleware(st *store.Store, jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid authorization header")
				return
			}

			claims := &Claims{}
			parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
				return []byte(jwtSecret), nil
			})
			if err != nil || !parsed.Valid {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
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

// GenerateToken creates a signed JWT for a user.
func GenerateToken(user *types.User, secret string, ttl time.Duration) (string, error) {
	claims := Claims{
		UserID:       user.ID,
		IsSuperadmin: user.IsSuperadmin,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   user.ID,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, &claims)
	return token.SignedString([]byte(secret))
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"data": data,
		"meta": types.Meta{Total: total, Page: page, PerPage: perPage},
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
