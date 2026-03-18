package admin

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListUpstreams(st *store.Store, _ []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreams, err := st.ListUpstreams()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list upstreams")
			return
		}
		writeData(w, http.StatusOK, upstreams)
	}
}

func handleCreateUpstream(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Provider        string                   `json:"provider"`
			Name            string                   `json:"name"`
			BaseURL         string                   `json:"base_url"`
			APIKey          string                   `json:"api_key"`
			SupportedModels []string                 `json:"supported_models"`
			ModelMap        map[string]string         `json:"model_map"`
			Weight          int                       `json:"weight"`
			MaxConcurrent   int                       `json:"max_concurrent"`
			TestModel       string                    `json:"test_model"`
			HealthCheck     *types.HealthCheckConfig   `json:"health_check"`
			DialTimeout     time.Duration             `json:"dial_timeout"`
			ReadTimeout     time.Duration             `json:"read_timeout"`
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

		u := &types.Upstream{
			Provider:        body.Provider,
			Name:            body.Name,
			BaseURL:         body.BaseURL,
			APIKeyEncrypted: encrypted,
			SupportedModels: body.SupportedModels,
			ModelMap:        body.ModelMap,
			Weight:          weight,
			MaxConcurrent:   body.MaxConcurrent,
			TestModel:       body.TestModel,
			HealthCheck:     body.HealthCheck,
			DialTimeout:     body.DialTimeout,
			ReadTimeout:     body.ReadTimeout,
			Status:          types.UpstreamStatusActive,
		}

		if err := st.CreateUpstream(u); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create upstream")
			return
		}
		writeData(w, http.StatusCreated, u)
	}
}

func handleGetUpstream(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := st.GetUpstreamByID(chi.URLParam(r, "upstreamID"))
		if err != nil || u == nil {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}
		writeData(w, http.StatusOK, u)
	}
}

func handleUpdateUpstream(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamID := chi.URLParam(r, "upstreamID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{
			"name", "base_url", "provider", "supported_models", "model_map",
			"weight", "max_concurrent", "test_model", "health_check",
			"dial_timeout", "read_timeout", "status",
		} {
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

		if err := st.UpdateUpstream(upstreamID, updates); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update upstream")
			return
		}

		u, _ := st.GetUpstreamByID(upstreamID)
		writeData(w, http.StatusOK, u)
	}
}

func handleDeleteUpstream(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := st.DeleteUpstream(chi.URLParam(r, "upstreamID")); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to delete upstream")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
