package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func handleLogin(st *store.Store, authCfg config.AuthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Email == "" || body.Password == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "email and password are required")
			return
		}

		user, err := st.GetUserByEmail(body.Email)
		if err != nil || user == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(body.Password)); err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
			return
		}

		if user.Status != "active" {
			writeError(w, http.StatusForbidden, "forbidden", "account is disabled")
			return
		}

		token, err := GenerateToken(user, authCfg.JWTSecret, authCfg.AccessTokenTTL)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to generate token")
			return
		}

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"access_token": token,
			"token_type":   "Bearer",
			"expires_in":   int(authCfg.AccessTokenTTL.Seconds()),
			"user":         user,
		})
	}
}

func handleGetMe() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if user == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
			return
		}
		writeData(w, http.StatusOK, user)
	}
}

func handleListUsers(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := parsePagination(r)
		users, total, err := st.ListUsers(p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list users")
			return
		}
		writeList(w, users, total, p.Page, p.Limit())
	}
}

func handleGetUser(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, err := st.GetUserByID(chi.URLParam(r, "userID"))
		if err != nil || user == nil {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		writeData(w, http.StatusOK, user)
	}
}

func handleUpdateUser(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := chi.URLParam(r, "userID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		// Only allow safe fields.
		updates := make(map[string]interface{})
		for _, field := range []string{"name", "status", "is_superadmin", "max_projects"} {
			if v, ok := body[field]; ok {
				updates[field] = v
			}
		}
		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "no valid fields to update")
			return
		}

		if err := st.UpdateUser(userID, updates); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update user")
			return
		}

		user, _ := st.GetUserByID(userID)
		writeData(w, http.StatusOK, user)
	}
}
