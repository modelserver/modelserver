package admin

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/auth"
	"github.com/modelserver/modelserver/internal/billing"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
)

// MountRoutes mounts all admin API routes onto the given router.
func MountRoutes(r chi.Router, st *store.Store, cfg *config.Config, encKey []byte, jwtMgr *auth.JWTManager) {
	// Construct payment client if configured.
	var payClient billing.PaymentClient
	if cfg.Billing.PaymentAPIURL != "" {
		payClient = billing.NewHTTPPaymentClient(cfg.Billing.PaymentAPIURL, cfg.Billing.PaymentAPIKey)
	}

	// Hoist hydraClient so it can be used both in the Hydra public endpoints
	// and in the authenticated OAuth grants revocation route below.
	var hydraClient *HydraClient
	if cfg.Auth.OAuth.Hydra.AdminURL != "" {
		hydraClient = NewHydraClient(cfg.Auth.OAuth.Hydra.AdminURL)
	}

	// Mount Hydra OAuth login/consent endpoints (public — no JWT auth required).
	// These are called by Hydra redirects from the user's browser.
	if hydraClient != nil {
		loginHandler, err := NewLoginHandler(hydraClient, st, encKey, cfg)
		if err != nil {
			panic("admin: failed to create Hydra login handler: " + err.Error())
		}

		consentHandler, err := NewConsentHandler(hydraClient, st)
		if err != nil {
			panic("admin: failed to create Hydra consent handler: " + err.Error())
		}

		r.Get("/oauth/login", loginHandler.ServeHTTP)
		r.Get("/oauth/consent", consentHandler.ServeHTTP)
		r.Post("/oauth/consent", consentHandler.ServeHTTP)

		// Device Flow (RFC 8628) endpoints (public — no JWT auth required).
		if cfg.Auth.OAuth.Hydra.DeviceFlow.ClientID != "" {
			deviceHandler, err := NewDeviceFlowHandler(st, encKey, cfg)
			if err != nil {
				panic("admin: failed to create device flow handler: " + err.Error())
			}
			r.Post("/oauth/device/code", deviceHandler.HandleDeviceAuthorize)
			r.Get("/oauth/device", deviceHandler.HandleVerificationPage)
			r.Post("/oauth/device", deviceHandler.HandleVerifyUserCode)
			r.Get("/oauth/device/callback", deviceHandler.HandleCallback)
			r.Post("/oauth/device/token", deviceHandler.HandleTokenPoll)
		}
	}

	r.Route("/api/v1", func(r chi.Router) {
		// Public auth endpoints.
		r.Get("/auth/config", handleAuthConfig(cfg))
		r.Post("/auth/refresh", handleRefresh(st, jwtMgr))

		// OAuth callbacks (public).
		r.Post("/auth/oauth/github", handleOAuthCallback(st, jwtMgr, cfg, encKey, "github"))
		r.Post("/auth/oauth/google", handleOAuthCallback(st, jwtMgr, cfg, encKey, "google"))
		r.Post("/auth/oauth/oidc", handleOAuthCallback(st, jwtMgr, cfg, encKey, "oidc"))

		// OAuth redirects — send user to provider's authorization page.
		r.Get("/auth/oauth/github/redirect", handleOAuthRedirect(cfg, "github"))
		r.Get("/auth/oauth/google/redirect", handleOAuthRedirect(cfg, "google"))
		r.Get("/auth/oauth/oidc/redirect", handleOAuthRedirect(cfg, "oidc"))

		// Billing webhook (HMAC auth, not JWT).
		if cfg.Billing.WebhookSecret != "" {
			r.Route("/billing/webhook", func(r chi.Router) {
				r.Use(billing.HMACAuthMiddleware(cfg.Billing.WebhookSecret))
				r.Post("/delivery", handleDeliveryWebhook(st))
			})
		}

		// Authenticated endpoints.
		r.Group(func(r chi.Router) {
			r.Use(JWTAuthMiddleware(jwtMgr, st))

			// Current user.
			r.Get("/me", handleGetMe())

			// Users (superadmin only).
			r.Route("/users", func(r chi.Router) {
				r.Use(RequireSuperadmin)
				r.Get("/", handleListUsers(st))
				r.Get("/{userID}", handleGetUser(st))
				r.Put("/{userID}", handleUpdateUser(st))
			})

			// Plans (superadmin only).
			r.Route("/plans", func(r chi.Router) {
				r.Use(RequireSuperadmin)
				r.Get("/", handleListPlans(st))
				r.Post("/", handleCreatePlan(st))
				r.Route("/{planID}", func(r chi.Router) {
					r.Get("/", handleGetPlan(st))
					r.Put("/", handleUpdatePlan(st))
					r.Delete("/", handleDeletePlan(st))
				})
			})

			// Admin: all projects (superadmin only).
			r.Route("/admin/projects", func(r chi.Router) {
				r.Use(RequireSuperadmin)
				r.Get("/", handleListAllProjects(st))
			})

			// Admin: global requests (superadmin only).
			r.Route("/admin/requests", func(r chi.Router) {
				r.Use(RequireSuperadmin)
				r.Get("/", handleListAllRequests(st))
			})

			// Projects.
			r.Route("/projects", func(r chi.Router) {
				r.Get("/", handleListProjects(st))
				r.Post("/", handleCreateProject(st))
				r.Route("/{projectID}", func(r chi.Router) {
					r.Use(projectAccessMiddleware(st))
					r.Get("/", handleGetProject(st))
					r.Put("/", handleUpdateProject(st))
					r.Post("/archive", handleArchiveProject(st))
					r.Post("/unarchive", handleUnarchiveProject(st))

					// Project members.
					r.Get("/members", handleListMembers(st))
					r.Post("/members", handleAddMember(st))
					r.Get("/members/usage", handleMembersUsage(st))
					r.Put("/members/{userID}", handleUpdateMember(st))
					r.Delete("/members/{userID}", handleRemoveMember(st))
					r.Get("/members/{userID}/quota-usage", handleQuotaUsage(st))
					r.Get("/my-quota", handleMyQuota(st))
					r.Get("/my-membership", handleMyMembership(st))

					// API Keys.
					r.Get("/keys", handleListKeys(st))
					r.Post("/keys", handleCreateKey(st, encKey))
					r.Route("/keys/{keyID}", func(r chi.Router) {
						r.Get("/", handleGetKey(st))
						r.Put("/", handleUpdateKey(st))
						r.Delete("/", handleDeleteKey(st))
					})

					// OAuth grants.
					r.Get("/oauth-grants", handleListOAuthGrants(st))
					r.Delete("/oauth-grants/{grantID}", handleRevokeOAuthGrant(st, hydraClient))

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
					r.Get("/subscription/usage", handleSubscriptionUsage(st))
					r.Route("/subscriptions/{subID}", func(r chi.Router) {
						r.Get("/", handleGetSubscription(st))
						r.Put("/", handleUpdateSubscription(st))
					})

					// Available plans & Orders.
					r.Get("/available-plans", handleListAvailablePlans(st))
					r.Get("/orders", handleListOrders(st))
					r.Post("/orders", handleCreateOrder(st, payClient, cfg.Billing))
					r.Get("/orders/{orderID}", handleGetOrder(st))
					r.Post("/orders/{orderID}/cancel", handleCancelOrder(st))

					// Requests & Usage.
					r.Get("/requests", handleListRequests(st))
					r.Get("/usage", handleGetUsage(st))

					// Traces.
					r.Get("/traces", handleListTraces(st))
					r.Get("/traces/{traceID}", handleGetTrace(st))
					r.Get("/traces/{traceID}/requests", handleListTraceRequests(st))
				})
			})

			// Upstreams (superadmin only).
			r.Route("/upstreams", func(r chi.Router) {
				r.Use(RequireSuperadmin)
				r.Get("/", handleListUpstreams(st, encKey))
				r.Post("/", handleCreateUpstream(st, encKey))
				r.Get("/usage", handleUpstreamUsage(st))
				r.Post("/claudecode/oauth/start", handleClaudeCodeOAuthStart())
				r.Post("/claudecode/oauth/exchange", handleClaudeCodeOAuthExchange())
				r.Route("/{upstreamID}", func(r chi.Router) {
					r.Get("/", handleGetUpstream(st))
					r.Put("/", handleUpdateUpstream(st, encKey))
					r.Delete("/", handleDeleteUpstream(st))
					r.Post("/test", handleTestUpstream(st, encKey))
					r.Get("/oauth/status", handleClaudeCodeTokenStatus(st, encKey))
					r.Post("/oauth/refresh", handleClaudeCodeTokenRefresh(st, encKey))
					r.Get("/utilization", handleClaudeCodeUtilization(st, encKey))
					r.Get("/utilization-snapshots", handleListUtilizationSnapshots(st))
					r.Get("/utilization-analysis", handleUtilizationAnalysis(st))
				})
			})

			// Upstream Groups (superadmin only).
			r.Route("/upstream-groups", func(r chi.Router) {
				r.Use(RequireSuperadmin)
				r.Get("/", handleListUpstreamGroups(st))
				r.Post("/", handleCreateUpstreamGroup(st))
				r.Route("/{groupID}", func(r chi.Router) {
					r.Get("/", handleGetUpstreamGroup(st))
					r.Put("/", handleUpdateUpstreamGroup(st))
					r.Delete("/", handleDeleteUpstreamGroup(st))
					r.Get("/members", handleListGroupMembers(st))
					r.Post("/members", handleAddGroupMember(st))
					r.Delete("/members/{upstreamID}", handleRemoveGroupMember(st))
				})
			})

			// Routing routes (superadmin only).
			r.Route("/routing", func(r chi.Router) {
				r.Use(RequireSuperadmin)
				r.Get("/routes", handleListRoutingRoutes(st))
				r.Post("/routes", handleCreateRoutingRoute(st))
				r.Put("/routes/{routeID}", handleUpdateRoutingRoute(st))
				r.Delete("/routes/{routeID}", handleDeleteRoutingRoute(st))
				// TODO: Wire HealthProvider once the Router is integrated.
				// r.Get("/health", handleRoutingHealth(hp))
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

			if user.IsSuperadmin {
				next.ServeHTTP(w, r)
				return
			}

			member, err := st.GetProjectMember(projectID, user.ID)
			if err != nil || member == nil {
				writeError(w, http.StatusForbidden, "forbidden", "you are not a member of this project")
				return
			}

			ctx := context.WithValue(r.Context(), ctxMember, member)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
