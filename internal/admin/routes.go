package admin

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
)

// MountRoutes mounts all admin API routes onto the given router.
func MountRoutes(r chi.Router, st *store.Store, cfg *config.Config, encKey []byte, logger *slog.Logger) {
	r.Route("/api/v1", func(r chi.Router) {
		// Public auth endpoints.
		r.Post("/auth/login", handleLogin(st, cfg.Auth))

		// Authenticated endpoints.
		r.Group(func(r chi.Router) {
			r.Use(AuthMiddleware(st, cfg.Auth.JWTSecret))

			// Current user.
			r.Get("/me", handleGetMe())

			// Users (superadmin only).
			r.Route("/users", func(r chi.Router) {
				r.Use(RequireSuperadmin)
				r.Get("/", handleListUsers(st))
				r.Get("/{userID}", handleGetUser(st))
				r.Put("/{userID}", handleUpdateUser(st))
			})

			// Projects.
			r.Route("/projects", func(r chi.Router) {
				r.Get("/", handleListProjects(st))
				r.Post("/", handleCreateProject(st))
				r.Route("/{projectID}", func(r chi.Router) {
					r.Use(projectAccessMiddleware(st))
					r.Get("/", handleGetProject(st))
					r.Put("/", handleUpdateProject(st))
					r.Delete("/", handleDeleteProject(st))

					// Project members.
					r.Get("/members", handleListMembers(st))
					r.Post("/members", handleAddMember(st))
					r.Put("/members/{userID}", handleUpdateMember(st))
					r.Delete("/members/{userID}", handleRemoveMember(st))

					// API Keys.
					r.Get("/keys", handleListKeys(st))
					r.Post("/keys", handleCreateKey(st))
					r.Route("/keys/{keyID}", func(r chi.Router) {
						r.Get("/", handleGetKey(st))
						r.Put("/", handleUpdateKey(st))
					})

					// Policies.
					r.Get("/policies", handleListPolicies(st))
					r.Post("/policies", handleCreatePolicy(st))
					r.Route("/policies/{policyID}", func(r chi.Router) {
						r.Get("/", handleGetPolicy(st))
						r.Put("/", handleUpdatePolicy(st))
						r.Delete("/", handleDeletePolicy(st))
					})

					// Subscriptions.
					r.Get("/subscriptions", handleListSubscriptions(st))
					r.Post("/subscriptions", handleCreateSubscription(st))
					r.Route("/subscriptions/{subID}", func(r chi.Router) {
						r.Get("/", handleGetSubscription(st))
						r.Put("/", handleUpdateSubscription(st))
					})

					// Requests & Usage.
					r.Get("/requests", handleListRequests(st))
					r.Get("/usage", handleGetUsage(st))

					// Traces & Threads.
					r.Get("/traces", handleListTraces(st))
					r.Get("/traces/{traceID}", handleGetTrace(st))
					r.Get("/threads", handleListThreads(st))
					r.Get("/threads/{threadID}", handleGetThread(st))
				})
			})

			// Channels (superadmin only).
			r.Route("/channels", func(r chi.Router) {
				r.Use(RequireSuperadmin)
				r.Get("/", handleListChannels(st, encKey))
				r.Post("/", handleCreateChannel(st, encKey))
				r.Route("/{channelID}", func(r chi.Router) {
					r.Get("/", handleGetChannel(st))
					r.Put("/", handleUpdateChannel(st, encKey))
					r.Delete("/", handleDeleteChannel(st))
				})
			})

			// Channel routes (superadmin only).
			r.Route("/routes", func(r chi.Router) {
				r.Use(RequireSuperadmin)
				r.Get("/", handleListRoutes(st))
				r.Post("/", handleCreateRoute(st))
				r.Route("/{routeID}", func(r chi.Router) {
					r.Put("/", handleUpdateRoute(st))
					r.Delete("/", handleDeleteRoute(st))
				})
			})
		})
	})
}

// projectAccessMiddleware ensures the authenticated user has access to the project.
func projectAccessMiddleware(st *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := UserFromContext(r.Context())
			if user == nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "not authenticated")
				return
			}

			projectID := chi.URLParam(r, "projectID")
			if projectID == "" {
				writeError(w, http.StatusBadRequest, "bad_request", "missing project ID")
				return
			}

			// Superadmins can access all projects.
			if user.IsSuperadmin {
				next.ServeHTTP(w, r)
				return
			}

			member, err := st.GetProjectMember(projectID, user.ID)
			if err != nil || member == nil {
				writeError(w, http.StatusForbidden, "forbidden", "you are not a member of this project")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
