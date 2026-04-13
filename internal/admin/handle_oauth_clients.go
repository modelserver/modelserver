package admin

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func handleListOAuthClients(hydra *HydraClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hydra == nil {
			writeError(w, http.StatusNotImplemented, "not_configured", "Hydra is not configured")
			return
		}
		clients, err := hydra.ListOAuthClients(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "hydra_error", "failed to list OAuth clients: "+err.Error())
			return
		}
		if clients == nil {
			clients = []HydraOAuthClient{}
		}
		// Strip secrets from list response.
		for i := range clients {
			clients[i].ClientSecret = ""
		}
		writeJSON(w, http.StatusOK, clients)
	}
}

func handleGetOAuthClient(hydra *HydraClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hydra == nil {
			writeError(w, http.StatusNotImplemented, "not_configured", "Hydra is not configured")
			return
		}
		clientID := chi.URLParam(r, "clientID")
		client, err := hydra.GetOAuthClient(r.Context(), clientID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "hydra_error", "failed to get OAuth client: "+err.Error())
			return
		}
		client.ClientSecret = "" // Never expose secret in GET
		writeJSON(w, http.StatusOK, client)
	}
}

func handleCreateOAuthClient(hydra *HydraClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hydra == nil {
			writeError(w, http.StatusNotImplemented, "not_configured", "Hydra is not configured")
			return
		}
		var input HydraOAuthClient
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		// Auto-generate a high-entropy client secret.
		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to generate secret")
			return
		}
		input.ClientSecret = hex.EncodeToString(secretBytes)

		// Set defaults if not provided.
		if len(input.GrantTypes) == 0 {
			input.GrantTypes = []string{"authorization_code", "refresh_token"}
		}
		if len(input.ResponseTypes) == 0 {
			input.ResponseTypes = []string{"code"}
		}
		if input.TokenEndpointAuthMethod == "" {
			input.TokenEndpointAuthMethod = "client_secret_post"
		}

		created, err := hydra.CreateOAuthClient(r.Context(), &input)
		if err != nil {
			writeError(w, http.StatusBadGateway, "hydra_error", "failed to create OAuth client: "+err.Error())
			return
		}

		// Return the secret in the create response only (one-time reveal).
		created.ClientSecret = input.ClientSecret
		writeJSON(w, http.StatusCreated, created)
	}
}

func handleUpdateOAuthClient(hydra *HydraClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hydra == nil {
			writeError(w, http.StatusNotImplemented, "not_configured", "Hydra is not configured")
			return
		}
		clientID := chi.URLParam(r, "clientID")

		var input HydraOAuthClient
		if err := decodeBody(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		// If regenerate_secret was requested (secret field is "regenerate"), generate a new one.
		var newSecret string
		if input.ClientSecret == "regenerate" {
			secretBytes := make([]byte, 32)
			if _, err := rand.Read(secretBytes); err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to generate secret")
				return
			}
			newSecret = hex.EncodeToString(secretBytes)
			input.ClientSecret = newSecret
		}

		updated, err := hydra.UpdateOAuthClient(r.Context(), clientID, &input)
		if err != nil {
			writeError(w, http.StatusBadGateway, "hydra_error", "failed to update OAuth client: "+err.Error())
			return
		}

		// Only include the new secret if one was regenerated.
		if newSecret != "" {
			updated.ClientSecret = newSecret
		} else {
			updated.ClientSecret = ""
		}
		writeJSON(w, http.StatusOK, updated)
	}
}

func handleDeleteOAuthClient(hydra *HydraClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if hydra == nil {
			writeError(w, http.StatusNotImplemented, "not_configured", "Hydra is not configured")
			return
		}
		clientID := chi.URLParam(r, "clientID")
		if err := hydra.DeleteOAuthClient(r.Context(), clientID); err != nil {
			writeError(w, http.StatusBadGateway, "hydra_error", "failed to delete OAuth client: "+err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
