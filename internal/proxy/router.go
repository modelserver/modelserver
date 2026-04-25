package proxy

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

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

// HandleListModels returns available models in OpenAI or Anthropic format
// depending on the auth-header style the client used. Bearer or fallback →
// OpenAI; x-api-key → Anthropic.
func (h *Handler) HandleListModels(w http.ResponseWriter, r *http.Request) {
	apiKey := APIKeyFromContext(r.Context())
	if apiKey == nil {
		writeProxyError(w, http.StatusUnauthorized, "missing api key")
		return
	}

	var names []string
	if len(apiKey.AllowedModels) > 0 {
		names = apiKey.AllowedModels
	} else {
		names = h.router.ActiveModels()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if r.Header.Get("x-api-key") != "" {
		writeAnthropicModelsList(w, h.catalog, names)
		return
	}
	writeOpenAIModelsList(w, h.catalog, names)
}

type openaiModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type openaiModelsList struct {
	Object string        `json:"object"`
	Data   []openaiModel `json:"data"`
}

func writeOpenAIModelsList(w http.ResponseWriter, catalog modelcatalog.Catalog, names []string) {
	out := openaiModelsList{
		Object: "list",
		Data:   make([]openaiModel, 0, len(names)),
	}
	for _, name := range names {
		m, _ := catalog.Get(name)
		ownedBy := "system"
		var createdAt time.Time
		if m != nil {
			if m.Publisher != "" {
				ownedBy = m.Publisher
			}
			createdAt = m.CreatedAt
		}
		out.Data = append(out.Data, openaiModel{
			ID:      name,
			Object:  "model",
			Created: createdAt.Unix(),
			OwnedBy: ownedBy,
		})
	}
	_ = json.NewEncoder(w).Encode(out)
}

type anthropicModel struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

type anthropicModelsList struct {
	Data    []anthropicModel `json:"data"`
	FirstID string           `json:"first_id"`
	LastID  string           `json:"last_id"`
	HasMore bool             `json:"has_more"`
}

func writeAnthropicModelsList(w http.ResponseWriter, catalog modelcatalog.Catalog, names []string) {
	out := anthropicModelsList{
		Data:    make([]anthropicModel, 0, len(names)),
		HasMore: false,
	}
	for _, name := range names {
		m, _ := catalog.Get(name)
		displayName := name
		var createdAt time.Time
		if m != nil {
			if m.DisplayName != "" {
				displayName = m.DisplayName
			}
			createdAt = m.CreatedAt
		}
		out.Data = append(out.Data, anthropicModel{
			Type:        "model",
			ID:          name,
			DisplayName: displayName,
			CreatedAt:   createdAt.Format(time.RFC3339),
		})
	}
	if len(out.Data) > 0 {
		out.FirstID = out.Data[0].ID
		out.LastID = out.Data[len(out.Data)-1].ID
	}
	_ = json.NewEncoder(w).Encode(out)
}
