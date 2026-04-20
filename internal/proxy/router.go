package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/ratelimit"
	"github.com/modelserver/modelserver/internal/store"
)

// MountRoutes mounts all proxy routes onto the given router.
// introspector may be nil; if set, OAuth token introspection (e.g. via Hydra)
// is used as a fallback when API key validation fails.
//
// Middleware order (spec §3.1):
//   Auth → Trace → ResolveModel → SubscriptionEligibility
//        → RateLimit → ExtraUsageGuard → Handler
func MountRoutes(
	r chi.Router,
	st *store.Store,
	handler *Handler,
	traceCfg config.TraceConfig,
	limiter ratelimit.RateLimiter,
	catalog modelcatalog.Catalog,
	euCfg config.ExtraUsageConfig,
	maxBodySize int64,
	encKey []byte,
	logger *slog.Logger,
	introspector TokenIntrospector,
) {
	wire := func(r chi.Router) {
		r.Use(AuthMiddleware(st, encKey, introspector))
		r.Use(TraceMiddleware(traceCfg, st, logger))
		r.Use(ResolveModelMiddleware(catalog, maxBodySize))
		r.Use(SubscriptionEligibilityMiddleware())
		if limiter != nil {
			r.Use(RateLimitMiddleware(limiter, st, logger))
		}
		r.Use(ExtraUsageGuardMiddleware(euCfg, st, logger))
	}

	r.Route("/v1", func(r chi.Router) {
		wire(r)
		r.Post("/messages", handler.HandleMessages)
		r.Post("/messages/count_tokens", handler.HandleCountTokens)
		r.Post("/responses", handler.HandleResponses)
		r.Post("/chat/completions", handler.HandleChatCompletions)
		r.Get("/models", handler.HandleListModels)
		r.Get("/usage", handler.HandleUsage)
	})

	// Gemini native API proxy: /v1beta/models/{model}:{method}
	r.Route("/v1beta", func(r chi.Router) {
		wire(r)
		r.Post("/models/*", handler.HandleGemini)
	})
}

// HandleListModels returns available models for the authenticated API key.
func (h *Handler) HandleListModels(w http.ResponseWriter, r *http.Request) {
	apiKey := APIKeyFromContext(r.Context())
	if apiKey == nil {
		writeProxyError(w, http.StatusUnauthorized, "missing api key")
		return
	}

	var models []string
	if len(apiKey.AllowedModels) > 0 {
		models = apiKey.AllowedModels
	} else {
		models = h.router.ActiveModels()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": models,
	})
}
