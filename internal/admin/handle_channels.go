package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

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
			Provider        string            `json:"provider"`
			Name            string            `json:"name"`
			BaseURL         string            `json:"base_url"`
			APIKey          string            `json:"api_key"`
			SupportedModels []string          `json:"supported_models"`
			ModelMap        map[string]string `json:"model_map"`
			Weight          int               `json:"weight"`
			Priority        int               `json:"selection_priority"`
			MaxConcurrent   int               `json:"max_concurrent"`
			TestModel       string            `json:"test_model"`
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
			ModelMap:          body.ModelMap,
			Weight:            weight,
			SelectionPriority: body.Priority,
			Status:            types.ChannelStatusActive,
			MaxConcurrent:     body.MaxConcurrent,
			TestModel:         body.TestModel,
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
		for _, field := range []string{"name", "base_url", "provider", "supported_models", "model_map", "weight", "selection_priority", "status", "max_concurrent", "test_model"} {
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

func handleChannelStats(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		since := now.AddDate(0, 0, -30)
		until := now

		if v := r.URL.Query().Get("since"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				since = t
			}
		}
		if v := r.URL.Query().Get("until"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				until = t
			}
		}

		stats, err := st.GetUsageByChannel(since, until)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to get channel stats")
			return
		}
		if stats == nil {
			stats = []store.ChannelUsageSummary{}
		}
		writeData(w, http.StatusOK, stats)
	}
}

func handleTestChannel(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		channelID := chi.URLParam(r, "channelID")
		ch, err := st.GetChannelByID(channelID)
		if err != nil || ch == nil {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
			return
		}

		apiKey, err := crypto.Decrypt(encKey, ch.APIKeyEncrypted)
		if err != nil {
			writeData(w, http.StatusOK, map[string]interface{}{
				"success": false,
				"error":   "failed to decrypt API key",
			})
			return
		}

		// Pick test model: test_model field → first supported model → fallback
		testModel := ch.TestModel
		if testModel == "" && len(ch.SupportedModels) > 0 {
			testModel = ch.SupportedModels[0]
		}
		if testModel == "" {
			testModel = "claude-haiku-4-5"
		}
		// Resolve the test model through model_map so the upstream receives
		// the correct provider-specific model name.
		upstreamTestModel := ch.ResolveModel(testModel)

		baseURL := ch.BaseURL
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}

		// Build minimal request body based on provider
		var reqBody []byte
		var endpoint string
		switch ch.Provider {
		case types.ProviderOpenAI:
			endpoint = baseURL + "/v1/chat/completions"
			reqBody, _ = json.Marshal(map[string]interface{}{
				"model":      upstreamTestModel,
				"max_tokens": 10,
				"messages":   []map[string]string{{"role": "user", "content": "Hi"}},
			})
		case types.ProviderBedrock:
			endpoint = baseURL + "/model/" + upstreamTestModel + "/invoke"
			reqBody, _ = json.Marshal(map[string]interface{}{
				"anthropic_version": "bedrock-2023-05-31",
				"max_tokens":        10,
				"messages":          []map[string]string{{"role": "user", "content": "Hi"}},
			})
		default: // anthropic, gemini, etc.
			endpoint = baseURL + "/v1/messages"
			reqBody, _ = json.Marshal(map[string]interface{}{
				"model":      upstreamTestModel,
				"max_tokens": 10,
				"messages":   []map[string]string{{"role": "user", "content": "Hi"}},
			})
		}

		client := &http.Client{Timeout: 10 * time.Second}
		req, err := http.NewRequest("POST", endpoint, bytes.NewReader(reqBody))
		if err != nil {
			writeData(w, http.StatusOK, map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("failed to create request: %v", err),
			})
			return
		}
		req.Header.Set("Content-Type", "application/json")

		switch ch.Provider {
		case types.ProviderOpenAI:
			req.Header.Set("Authorization", "Bearer "+string(apiKey))
		case types.ProviderBedrock:
			req.Header.Set("Authorization", "Bearer "+string(apiKey))
		default: // anthropic
			req.Header.Set("x-api-key", string(apiKey))
			req.Header.Set("anthropic-version", "2023-06-01")
		}

		start := time.Now()
		resp, err := client.Do(req)
		latency := time.Since(start).Milliseconds()

		if err != nil {
			writeData(w, http.StatusOK, map[string]interface{}{
				"success":    false,
				"latency_ms": latency,
				"model":      testModel,
				"error":      fmt.Sprintf("request failed: %v", err),
			})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		success := resp.StatusCode >= 200 && resp.StatusCode < 300
		result := map[string]interface{}{
			"success":     success,
			"status_code": resp.StatusCode,
			"latency_ms":  latency,
			"model":       testModel,
		}
		if !success {
			result["error"] = fmt.Sprintf("upstream returned %d", resp.StatusCode)
		}
		writeData(w, http.StatusOK, result)
	}
}

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
			Status        string   `json:"status"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.ModelPattern == "" || len(body.ChannelIDs) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "model_pattern and channel_ids are required")
			return
		}

		status := "active"
		if body.Status != "" {
			status = body.Status
		}

		route := &types.ChannelRoute{
			ProjectID:     body.ProjectID,
			ModelPattern:  body.ModelPattern,
			ChannelIDs:    body.ChannelIDs,
			MatchPriority: body.MatchPriority,
			Status:        status,
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
		for _, field := range []string{"model_pattern", "channel_ids", "match_priority", "status"} {
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
