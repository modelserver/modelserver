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
	"golang.org/x/oauth2/google"
)

func handleListUpstreams(st *store.Store, _ []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := parsePagination(r)
		upstreams, total, err := st.ListUpstreamsPaginated(p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list upstreams")
			return
		}
		if upstreams == nil {
			upstreams = []types.Upstream{}
		}
		writeList(w, upstreams, total, p.Page, p.Limit())
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

func handleTestUpstream(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := st.GetUpstreamByID(chi.URLParam(r, "upstreamID"))
		if err != nil || u == nil {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}

		apiKey, err := crypto.Decrypt(encKey, u.APIKeyEncrypted)
		if err != nil {
			writeData(w, http.StatusOK, map[string]interface{}{
				"success": false,
				"error":   "failed to decrypt API key",
			})
			return
		}

		testModel := u.TestModel
		if testModel == "" && len(u.SupportedModels) > 0 {
			testModel = u.SupportedModels[0]
		}
		if testModel == "" {
			testModel = "claude-haiku-4-5"
		}
		upstreamTestModel := u.ResolveModel(testModel)

		baseURL := u.BaseURL
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}

		var reqBody []byte
		var endpoint string
		switch u.Provider {
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
		case types.ProviderClaudeCode:
			endpoint = baseURL + "/v1/messages?beta=true"
			reqBody, _ = json.Marshal(map[string]interface{}{
				"model":      upstreamTestModel,
				"max_tokens": 10,
				"messages":   []map[string]string{{"role": "user", "content": "Hi"}},
			})
		case types.ProviderVertex:
			base := baseURL
			if len(base) > 0 && base[len(base)-1] == '/' {
				base = base[:len(base)-1]
			}
			endpoint = fmt.Sprintf("%s/%s:rawPredict", base, upstreamTestModel)
			reqBody, _ = json.Marshal(map[string]interface{}{
				"anthropic_version": "vertex-2023-10-16",
				"max_tokens":        10,
				"messages":          []map[string]string{{"role": "user", "content": "Hi"}},
			})
		default:
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

		switch u.Provider {
		case types.ProviderOpenAI:
			req.Header.Set("Authorization", "Bearer "+string(apiKey))
		case types.ProviderBedrock:
			req.Header.Set("Authorization", "Bearer "+string(apiKey))
		case types.ProviderClaudeCode:
			var creds struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(apiKey, &creds); err != nil || creds.AccessToken == "" {
				writeData(w, http.StatusOK, map[string]interface{}{
					"success": false,
					"error":   "failed to parse claudecode credentials",
				})
				return
			}
			req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
			req.Header.Set("Anthropic-Version", "2023-06-01")
			req.Header.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")
			req.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
		case types.ProviderVertex:
			creds, err := google.CredentialsFromJSON(r.Context(), apiKey, "https://www.googleapis.com/auth/cloud-platform")
			if err != nil {
				writeData(w, http.StatusOK, map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("failed to parse service account JSON: %v", err),
				})
				return
			}
			tok, err := creds.TokenSource.Token()
			if err != nil {
				writeData(w, http.StatusOK, map[string]interface{}{
					"success": false,
					"error":   fmt.Sprintf("failed to get access token: %v", err),
				})
				return
			}
			req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		default:
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
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

		success := resp.StatusCode >= 200 && resp.StatusCode < 300
		result := map[string]interface{}{
			"success":     success,
			"status_code": resp.StatusCode,
			"latency_ms":  latency,
			"model":       testModel,
		}
		if !success {
			result["error"] = fmt.Sprintf("upstream returned %d: %s", resp.StatusCode, string(respBody))
		}
		writeData(w, http.StatusOK, result)
	}
}

func handleUpstreamUsage(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		farPast := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

		// All-time usage.
		allTime, err := st.GetUsageByUpstream(farPast, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to get upstream usage")
			return
		}

		// Windowed credit sums for 5h and 7d.
		credits5h, err := st.GetCreditsByUpstreamSince(now.Add(-5 * time.Hour))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to get 5h credits")
			return
		}
		credits7d, err := st.GetCreditsByUpstreamSince(now.Add(-7 * 24 * time.Hour))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to get 7d credits")
			return
		}

		type entry struct {
			store.UpstreamUsageSummary
			Credits5h float64 `json:"credits_5h"`
			Credits7d float64 `json:"credits_7d"`
		}

		result := make([]entry, len(allTime))
		for i, u := range allTime {
			result[i] = entry{
				UpstreamUsageSummary: u,
				Credits5h:           credits5h[u.UpstreamID],
				Credits7d:           credits7d[u.UpstreamID],
			}
		}
		writeData(w, http.StatusOK, result)
	}
}
