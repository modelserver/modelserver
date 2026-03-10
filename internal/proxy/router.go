package proxy

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/store"
)

// MountRoutes mounts all proxy routes onto the given router.
func MountRoutes(r chi.Router, st *store.Store, handler *Handler, traceCfg config.TraceConfig) {
	r.Route("/v1", func(r chi.Router) {
		r.Use(AuthMiddleware(st))
		r.Use(TraceMiddleware(traceCfg))

		r.Post("/messages", handler.HandleMessages)
		r.Get("/models", handler.HandleListModels)
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
		seen := make(map[string]bool)
		for _, ch := range h.channelRouter.channels {
			if ch.Status == "active" {
				for _, m := range ch.SupportedModels {
					if !seen[m] {
						seen[m] = true
						models = append(models, m)
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data": models,
	})
}
