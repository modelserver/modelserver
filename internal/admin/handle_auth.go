package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/auth"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

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
			oidcProvider, oidcErr := auth.NewOIDCProvider(ctx, cfg.Auth.OAuth.OIDC.IssuerURL, cfg.Auth.OAuth.OIDC.ClientID, cfg.Auth.OAuth.OIDC.ClientSecret, cfg.Auth.OAuth.OIDC.RedirectURI)
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
		if info.Email == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "OAuth provider did not return an email address")
			return
		}

		// Look up user: first by OAuth provider ID, then by email.
		// Email is unique, so different OAuth providers with the same email
		// will always resolve to the same user account.
		user, _ := st.GetUserByOAuth(info.Provider, info.ProviderID)
		if user == nil {
			user, _ = st.GetUserByEmail(info.Email)
		}

		if user != nil {
			// Existing user — ensure OAuth connection exists and sync profile fields.
			_ = st.CreateOAuthConnection(user.ID, info.Provider, info.ProviderID)
			updates := map[string]interface{}{}
			if info.Name != "" && info.Name != user.Nickname {
				updates["nickname"] = info.Name
			}
			if info.Picture != "" && info.Picture != user.Picture {
				updates["picture"] = info.Picture
			}
			if len(updates) > 0 {
				if err := st.UpdateUser(user.ID, updates); err != nil {
					log.Printf("WARN: failed to update OAuth user %s: %v", user.ID, err)
				}
				if fresh, err := st.GetUserByID(user.ID); err == nil && fresh != nil {
					user = fresh
				}
			}
		}

		if user == nil {
			// First registered user becomes superadmin.
			isFirst := false
			if exists, err := st.UserExists(); err == nil && !exists {
				isFirst = true
			}

			// Create new user from OAuth.
			user = &types.User{
				Email:        info.Email,
				Nickname:     info.Name,
				Picture:      info.Picture,
				IsSuperadmin: isFirst,
				MaxProjects:  5,
				Status:       types.UserStatusActive,
			}
			if isFirst {
				user.MaxProjects = 100
			}
			if err := st.CreateUser(user); err != nil {
				// Likely a duplicate email — race or stale lookup. Retry by email.
				log.Printf("WARN: create OAuth user failed (email=%s): %v, retrying lookup", info.Email, err)
				user, _ = st.GetUserByEmail(info.Email)
				if user == nil {
					writeError(w, http.StatusInternalServerError, "internal", "failed to create user")
					return
				}
			} else {
				// Auto-create default project for new OAuth user.
				project := &types.Project{
					Name:      "Default Project",
					CreatedBy: user.ID,
					Status:    types.ProjectStatusActive,
				}
				if err := st.CreateProject(project); err != nil {
					log.Printf("WARN: failed to create default project for OAuth user %s: %v", user.ID, err)
				} else {
					assignFreePlan(st, project.ID)
				}
			}
			_ = st.CreateOAuthConnection(user.ID, info.Provider, info.ProviderID)
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
		for _, field := range []string{"nickname", "status", "is_superadmin", "max_projects"} {
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

func handleOAuthRedirect(cfg *config.Config, provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Build the callback URL pointing to the frontend's /auth/callback page.
		// Prefer explicit config; fall back to inferring from request headers.
		var callbackURL string
		switch provider {
		case "oidc":
			callbackURL = cfg.Auth.OAuth.OIDC.RedirectURI
		}
		if callbackURL == "" {
			scheme := "https"
			if r.TLS == nil {
				if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
					scheme = fwd
				} else {
					scheme = "http"
				}
			}
			callbackURL = scheme + "://" + r.Host + "/auth/callback/" + provider
		}

		// Generate a random state parameter.
		stateBytes := make([]byte, 16)
		_, _ = rand.Read(stateBytes)
		state := hex.EncodeToString(stateBytes)

		ctx := r.Context()
		var authURL string

		switch provider {
		case "github":
			if cfg.Auth.OAuth.GitHub.ClientID == "" {
				writeError(w, http.StatusNotImplemented, "not_configured", "GitHub OAuth not configured")
				return
			}
			gh := auth.NewGitHubOAuth(cfg.Auth.OAuth.GitHub.ClientID, cfg.Auth.OAuth.GitHub.ClientSecret, "")
			authURL = gh.AuthCodeURL(state, callbackURL)
		case "google":
			if cfg.Auth.OAuth.Google.ClientID == "" {
				writeError(w, http.StatusNotImplemented, "not_configured", "Google OAuth not configured")
				return
			}
			g := auth.NewGoogleOAuth(cfg.Auth.OAuth.Google.ClientID, cfg.Auth.OAuth.Google.ClientSecret, "")
			authURL = g.AuthCodeURL(state, callbackURL)
		case "oidc":
			if cfg.Auth.OAuth.OIDC.IssuerURL == "" {
				writeError(w, http.StatusNotImplemented, "not_configured", "OIDC not configured")
				return
			}
			oidcProvider, err := auth.NewOIDCProvider(ctx, cfg.Auth.OAuth.OIDC.IssuerURL, cfg.Auth.OAuth.OIDC.ClientID, cfg.Auth.OAuth.OIDC.ClientSecret, callbackURL)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to initialize OIDC")
				return
			}
			authURL = oidcProvider.AuthCodeURL(state, callbackURL)
		default:
			writeError(w, http.StatusBadRequest, "bad_request", "unsupported provider")
			return
		}

		http.Redirect(w, r, authURL, http.StatusFound)
	}
}

func handleAuthConfig(cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var providers []string
		if cfg.Auth.OAuth.GitHub.ClientID != "" {
			providers = append(providers, "github")
		}
		if cfg.Auth.OAuth.Google.ClientID != "" {
			providers = append(providers, "google")
		}
		if cfg.Auth.OAuth.OIDC.IssuerURL != "" {
			providers = append(providers, "oidc")
		}
		if providers == nil {
			providers = []string{}
		}

		// Optional display-name overrides for OAuth providers.
		oauthLabels := map[string]string{}
		if cfg.Auth.OAuth.OIDC.DisplayName != "" {
			oauthLabels["oidc"] = cfg.Auth.OAuth.OIDC.DisplayName
		}

		resp := map[string]interface{}{
			"oauth_providers": providers,
		}
		if cfg.Auth.LoginDescription != "" {
			resp["login_description"] = cfg.Auth.LoginDescription
		}
		if len(oauthLabels) > 0 {
			resp["oauth_labels"] = oauthLabels
		}
		writeJSON(w, http.StatusOK, resp)
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
