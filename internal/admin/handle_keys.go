package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListKeys(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		p := parsePagination(r)
		keys, total, err := st.ListAPIKeys(projectID, p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list keys")
			return
		}
		writeList(w, keys, total, p.Page, p.Limit())
	}
}

func handleCreateKey(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		user := UserFromContext(r.Context())
		var body struct {
			Name              string   `json:"name"`
			Description       string   `json:"description"`
			AllowedModels     []string `json:"allowed_models"`
			RateLimitPolicyID string   `json:"rate_limit_policy_id"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name is required")
			return
		}

		// Generate a random API key.
		rawBytes := make([]byte, 32)
		if _, err := rand.Read(rawBytes); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to generate key")
			return
		}
		plaintext := types.APIKeyPrefix + hex.EncodeToString(rawBytes)
		hash := sha256.Sum256([]byte(plaintext))

		key := &types.APIKey{
			ProjectID:         projectID,
			CreatedBy:         user.ID,
			KeyHash:           hex.EncodeToString(hash[:]),
			KeyPrefix:         plaintext[:len(types.APIKeyPrefix)+8],
			Name:              body.Name,
			Description:       body.Description,
			Status:            types.APIKeyStatusActive,
			RateLimitPolicyID: body.RateLimitPolicyID,
			AllowedModels:     body.AllowedModels,
		}

		if err := st.CreateAPIKey(key); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create key")
			return
		}

		// Return the full plaintext key only on creation.
		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"data": key,
			"key":  plaintext,
		})
	}
}

func handleGetKey(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, err := st.GetAPIKeyByID(chi.URLParam(r, "keyID"))
		if err != nil || key == nil {
			writeError(w, http.StatusNotFound, "not_found", "key not found")
			return
		}
		writeData(w, http.StatusOK, key)
	}
}

func handleUpdateKey(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keyID := chi.URLParam(r, "keyID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"name", "description", "status", "rate_limit_policy_id", "allowed_models"} {
			if v, ok := body[field]; ok {
				updates[field] = v
			}
		}
		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "no valid fields to update")
			return
		}

		if err := st.UpdateAPIKey(keyID, updates); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update key")
			return
		}

		key, _ := st.GetAPIKeyByID(keyID)
		writeData(w, http.StatusOK, key)
	}
}
