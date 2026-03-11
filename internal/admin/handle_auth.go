package admin

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/auth"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
	"golang.org/x/crypto/bcrypt"
)

func handleLogin(st *store.Store, jwtMgr *auth.JWTManager, authCfg config.AuthConfig) http.HandlerFunc {
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
		if err != nil || user == nil || user.PasswordHash == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(body.Password)); err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid credentials")
			return
		}

		if user.Status != types.UserStatusActive {
			writeError(w, http.StatusForbidden, "forbidden", "account is disabled")
			return
		}

		issueTokens(w, jwtMgr, user)
	}
}

func handleRegister(st *store.Store, jwtMgr *auth.JWTManager, authCfg config.AuthConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authCfg.AllowRegistration {
			writeError(w, http.StatusForbidden, "forbidden", "registration is disabled")
			return
		}

		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			Name     string `json:"name"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Email == "" || body.Password == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "email and password are required")
			return
		}

		existing, _ := st.GetUserByEmail(body.Email)
		if existing != nil {
			writeError(w, http.StatusConflict, "conflict", "email already registered")
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to hash password")
			return
		}

		// First registered user becomes superadmin.
		isFirst := false
		if exists, err := st.UserExists(); err == nil && !exists {
			isFirst = true
		}

		user := &types.User{
			Email:        body.Email,
			PasswordHash: string(hash),
			Name:         body.Name,
			IsSuperadmin: isFirst,
			MaxProjects:  5,
			Status:       types.UserStatusActive,
		}
		if isFirst {
			user.MaxProjects = 100
		}
		if err := st.CreateUser(user); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create user")
			return
		}

		// Auto-create default project for new user.
		project := &types.Project{
			Name:      "Default Project",
			Slug:      "default-" + user.ID[:8],
			CreatedBy: user.ID,
			Status:    types.ProjectStatusActive,
		}
		_ = st.CreateProject(project)

		issueTokens(w, jwtMgr, user)
	}
}

func handleRefresh(st *store.Store, jwtMgr *auth.JWTManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		if err := decodeBody(r, &body); err != nil || body.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "refresh_token is required")
			return
		}

		claims, err := jwtMgr.ValidateToken(body.RefreshToken)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid refresh token")
			return
		}
		if claims.TokenType != "refresh" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "expected refresh token")
			return
		}

		user, err := st.GetUserByID(claims.UserID)
		if err != nil || user == nil || user.Status != types.UserStatusActive {
			writeError(w, http.StatusUnauthorized, "unauthorized", "user not found or disabled")
			return
		}

		issueTokens(w, jwtMgr, user)
	}
}

func handleInitialize(st *store.Store, jwtMgr *auth.JWTManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		exists, err := st.UserExists()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if exists {
			writeError(w, http.StatusConflict, "conflict", "system already initialized")
			return
		}

		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			Name     string `json:"name"`
		}
		if err := decodeBody(r, &body); err != nil || body.Email == "" || body.Password == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "email and password are required")
			return
		}

		hash, _ := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		user := &types.User{
			Email:        body.Email,
			PasswordHash: string(hash),
			Name:         body.Name,
			IsSuperadmin: true,
			MaxProjects:  100,
			Status:       types.UserStatusActive,
		}
		if err := st.CreateUser(user); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create user")
			return
		}

		// Auto-create default project for superadmin.
		project := &types.Project{
			Name:      "Default Project",
			Slug:      "default-" + user.ID[:8],
			CreatedBy: user.ID,
			Status:    types.ProjectStatusActive,
		}
		_ = st.CreateProject(project)

		issueTokens(w, jwtMgr, user)
	}
}

func handleChangePassword(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if user == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
			return
		}

		var body struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := decodeBody(r, &body); err != nil || body.NewPassword == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "new_password is required")
			return
		}

		if user.PasswordHash != "" {
			if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(body.CurrentPassword)); err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "incorrect current password")
				return
			}
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(body.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to hash password")
			return
		}

		if err := st.UpdateUser(user.ID, map[string]interface{}{"password_hash": string(hash)}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update password")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleOAuthCallback(st *store.Store, jwtMgr *auth.JWTManager, cfg *config.Config, provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		if err := decodeBody(r, &body); err != nil || body.Code == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "code is required")
			return
		}

		var info *auth.OAuthUserInfo
		var err error
		ctx := context.Background()

		switch provider {
		case "github":
			if cfg.Auth.OAuth.GitHub.ClientID == "" {
				writeError(w, http.StatusNotImplemented, "not_configured", "GitHub OAuth not configured")
				return
			}
			gh := auth.NewGitHubOAuth(cfg.Auth.OAuth.GitHub.ClientID, cfg.Auth.OAuth.GitHub.ClientSecret, "")
			info, err = gh.ExchangeAndGetUser(ctx, body.Code)
		case "google":
			if cfg.Auth.OAuth.Google.ClientID == "" {
				writeError(w, http.StatusNotImplemented, "not_configured", "Google OAuth not configured")
				return
			}
			g := auth.NewGoogleOAuth(cfg.Auth.OAuth.Google.ClientID, cfg.Auth.OAuth.Google.ClientSecret, "")
			info, err = g.ExchangeAndGetUser(ctx, body.Code)
		case "oidc":
			if cfg.Auth.OAuth.OIDC.IssuerURL == "" {
				writeError(w, http.StatusNotImplemented, "not_configured", "OIDC not configured")
				return
			}
			oidcProvider, oidcErr := auth.NewOIDCProvider(ctx, cfg.Auth.OAuth.OIDC.IssuerURL, cfg.Auth.OAuth.OIDC.ClientID, cfg.Auth.OAuth.OIDC.ClientSecret, "")
			if oidcErr != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to initialize OIDC")
				return
			}
			info, err = oidcProvider.ExchangeAndGetUser(ctx, body.Code)
		default:
			writeError(w, http.StatusBadRequest, "bad_request", "unsupported provider")
			return
		}

		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "OAuth exchange failed: "+err.Error())
			return
		}

		// Try to find existing user by OAuth provider ID.
		user, _ := st.GetUserByOAuth(info.Provider, info.ProviderID)
		if user == nil && info.Email != "" {
			user, _ = st.GetUserByEmail(info.Email)
			if user != nil {
				// Link OAuth to existing email account.
				if err := st.UpdateUser(user.ID, map[string]interface{}{
					"oauth_provider": info.Provider,
					"oauth_id":       info.ProviderID,
					"avatar_url":     info.AvatarURL,
				}); err != nil {
					// Non-fatal: log and continue with login.
					_ = err
				}
			}
		}

		if user == nil {
			// Create new user from OAuth.
			user = &types.User{
				Email:         info.Email,
				Name:          info.Name,
				AvatarURL:     info.AvatarURL,
				OAuthProvider: info.Provider,
				OAuthID:       info.ProviderID,
				MaxProjects:   5,
				Status:        types.UserStatusActive,
			}
			if err := st.CreateUser(user); err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to create user")
				return
			}
		}

		if user.Status != types.UserStatusActive {
			writeError(w, http.StatusForbidden, "forbidden", "account is disabled")
			return
		}

		issueTokens(w, jwtMgr, user)
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

func issueTokens(w http.ResponseWriter, jwtMgr *auth.JWTManager, user *types.User) {
	access, refresh, err := jwtMgr.GenerateTokenPair(user.ID, user.Email, user.IsSuperadmin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to generate tokens")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token":  access,
		"refresh_token": refresh,
		"user":          user,
	})
}
