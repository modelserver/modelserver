package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListKeys(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		p := parsePagination(r)

		user := UserFromContext(r.Context())
		member := MemberFromContext(r.Context())

		// Developers can only see their own keys; owner/maintainer/superadmin see all.
		if member != nil && member.Role == types.RoleDeveloper {
			keys, total, err := st.ListAPIKeysByCreator(projectID, user.ID, p)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to list keys")
				return
			}
			writeList(w, keys, total, p.Page, p.Limit())
			return
		}

		keys, total, err := st.ListAPIKeys(projectID, p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list keys")
			return
		}
		writeList(w, keys, total, p.Page, p.Limit())
	}
}

func handleCreateKey(st *store.Store, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		user := UserFromContext(r.Context())
		var body struct {
			Name          string   `json:"name"`
			Description   string   `json:"description"`
			AllowedModels []string `json:"allowed_models"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name is required")
			return
		}

		// Generate 32 random bytes.
		randomBytes := make([]byte, crypto.APIKeyRandomLen)
		if _, err := rand.Read(randomBytes); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to generate key")
			return
		}

		// Compute 4-byte HMAC checksum over the random bytes.
		checksum := crypto.ComputeAPIKeyChecksum(encKey, randomBytes)

		// Concatenate random + checksum and encode as base64url (no padding).
		combined := append(randomBytes, checksum...)
		keyBody := base64.RawURLEncoding.EncodeToString(combined)

		plaintext := types.APIKeyPrefix + keyBody // ms- + 48 base64url chars
		hash := sha256.Sum256([]byte(plaintext))

		key := &types.APIKey{
			ProjectID:     projectID,
			CreatedBy:     user.ID,
			KeyHash:       hex.EncodeToString(hash[:]),
			KeyPrefix:     plaintext[:len(types.APIKeyPrefix)+8],
			Name:          body.Name,
			Description:   body.Description,
			Status:        types.APIKeyStatusActive,
			AllowedModels: body.AllowedModels,
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
		if !canAccessKey(r, key) {
			writeError(w, http.StatusForbidden, "forbidden", "you can only access your own keys")
			return
		}
		writeData(w, http.StatusOK, key)
	}
}

func handleUpdateKey(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keyID := chi.URLParam(r, "keyID")

		key, err := st.GetAPIKeyByID(keyID)
		if err != nil || key == nil {
			writeError(w, http.StatusNotFound, "not_found", "key not found")
			return
		}
		if !canAccessKey(r, key) {
			writeError(w, http.StatusForbidden, "forbidden", "you can only manage your own keys")
			return
		}

		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"name", "description", "status", "allowed_models"} {
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

		key, _ = st.GetAPIKeyByID(keyID)
		writeData(w, http.StatusOK, key)
	}
}

// canAccessKey checks whether the current user can access the given API key.
// Superadmins and owner/maintainer roles can access all keys.
// Developers can only access keys they created.
func canAccessKey(r *http.Request, key *types.APIKey) bool {
	member := MemberFromContext(r.Context())
	// No member in context means superadmin (bypassed projectAccessMiddleware).
	if member == nil {
		return true
	}
	if member.Role == types.RoleOwner || member.Role == types.RoleMaintainer {
		return true
	}
	user := UserFromContext(r.Context())
	return user != nil && key.CreatedBy == user.ID
}
