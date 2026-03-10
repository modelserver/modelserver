package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListChannels(st *store.Store, _ []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channels, err := st.ListChannels()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list channels")
			return
		}
		writeData(w, http.StatusOK, channels)
	}
}

func handleCreateChannel(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Provider        string   `json:"provider"`
			Name            string   `json:"name"`
			BaseURL         string   `json:"base_url"`
			APIKey          string   `json:"api_key"`
			SupportedModels []string `json:"supported_models"`
			Weight          int      `json:"weight"`
			Priority        int      `json:"selection_priority"`
			MaxConcurrent   int      `json:"max_concurrent"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Name == "" || body.Provider == "" || body.APIKey == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name, provider, and api_key are required")
			return
		}

		encrypted, err := crypto.Encrypt(encKey, []byte(body.APIKey))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to encrypt API key")
			return
		}

		weight := body.Weight
		if weight <= 0 {
			weight = 1
		}

		ch := &types.Channel{
			Provider:          body.Provider,
			Name:              body.Name,
			BaseURL:           body.BaseURL,
			APIKeyEncrypted:   encrypted,
			SupportedModels:   body.SupportedModels,
			Weight:            weight,
			SelectionPriority: body.Priority,
			Status:            types.ChannelStatusActive,
			MaxConcurrent:     body.MaxConcurrent,
		}

		if err := st.CreateChannel(ch); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create channel")
			return
		}
		writeData(w, http.StatusCreated, ch)
	}
}

func handleGetChannel(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ch, err := st.GetChannelByID(chi.URLParam(r, "channelID"))
		if err != nil || ch == nil {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}
		writeData(w, http.StatusOK, ch)
	}
}

func handleUpdateChannel(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channelID := chi.URLParam(r, "channelID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"name", "base_url", "provider", "supported_models", "weight", "selection_priority", "status", "max_concurrent"} {
			if v, ok := body[field]; ok {
				updates[field] = v
			}
		}

		// Handle API key re-encryption if provided.
		if rawKey, ok := body["api_key"].(string); ok && rawKey != "" {
			encrypted, err := crypto.Encrypt(encKey, []byte(rawKey))
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to encrypt API key")
				return
			}
			updates["api_key_encrypted"] = encrypted
		}

		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "no valid fields to update")
			return
		}

		if err := st.UpdateChannel(channelID, updates); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update channel")
			return
		}

		ch, _ := st.GetChannelByID(channelID)
		writeData(w, http.StatusOK, ch)
	}
}

func handleDeleteChannel(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := st.DeleteChannel(chi.URLParam(r, "channelID")); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to delete channel")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- Channel Routes ---

func handleListRoutes(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		routes, err := st.ListChannelRoutes()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list routes")
			return
		}
		writeData(w, http.StatusOK, routes)
	}
}

func handleCreateRoute(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProjectID     string   `json:"project_id"`
			ModelPattern  string   `json:"model_pattern"`
			ChannelIDs    []string `json:"channel_ids"`
			MatchPriority int      `json:"match_priority"`
			Enabled       *bool    `json:"enabled"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.ModelPattern == "" || len(body.ChannelIDs) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "model_pattern and channel_ids are required")
			return
		}

		enabled := true
		if body.Enabled != nil {
			enabled = *body.Enabled
		}

		route := &types.ChannelRoute{
			ProjectID:     body.ProjectID,
			ModelPattern:  body.ModelPattern,
			ChannelIDs:    body.ChannelIDs,
			MatchPriority: body.MatchPriority,
			Enabled:       enabled,
		}
		if err := st.CreateChannelRoute(route); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create route")
			return
		}
		writeData(w, http.StatusCreated, route)
	}
}

func handleUpdateRoute(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		routeID := chi.URLParam(r, "routeID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"model_pattern", "channel_ids", "match_priority", "enabled"} {
			if v, ok := body[field]; ok {
				updates[field] = v
			}
		}
		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "no valid fields to update")
			return
		}

		if err := st.UpdateChannelRoute(routeID, updates); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update route")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleDeleteRoute(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := st.DeleteChannelRoute(chi.URLParam(r, "routeID")); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to delete route")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
