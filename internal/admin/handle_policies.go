package admin

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListPolicies(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		policies, err := st.ListPolicies(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list policies")
			return
		}
		writeData(w, http.StatusOK, policies)
	}
}

func handleCreatePolicy(st *store.Store, catalog modelcatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		projectID := chi.URLParam(r, "projectID")
		var body struct {
			Name             string                       `json:"name"`
			IsDefault        bool                         `json:"is_default"`
			CreditRules      []types.CreditRule           `json:"credit_rules"`
			ModelCreditRates map[string]types.CreditRate  `json:"model_credit_rates"`
			ClassicRules     []types.ClassicRule           `json:"classic_rules"`
			StartsAt         string                       `json:"starts_at"`
			ExpiresAt        string                       `json:"expires_at"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name is required")
			return
		}

		rates, err := normalizeRateMapKeys(catalog, body.ModelCreditRates)
		if err != nil {
			writeUnknownModelsError(w, err)
			return
		}

		policy := &types.RateLimitPolicy{
			ProjectID:        projectID,
			Name:             body.Name,
			IsDefault:        body.IsDefault,
			CreditRules:      body.CreditRules,
			ModelCreditRates: rates,
			ClassicRules:     body.ClassicRules,
		}
		if body.StartsAt != "" {
			if t, err := time.Parse(time.RFC3339, body.StartsAt); err == nil {
				policy.StartsAt = &t
			}
		}
		if body.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, body.ExpiresAt); err == nil {
				policy.ExpiresAt = &t
			}
		}

		if err := st.CreatePolicy(policy); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create policy")
			return
		}
		writeData(w, http.StatusCreated, policy)
	}
}

func handleGetPolicy(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		policy, err := st.GetPolicyByID(chi.URLParam(r, "policyID"))
		if err != nil || policy == nil {
			writeError(w, http.StatusNotFound, "not_found", "policy not found")
			return
		}
		writeData(w, http.StatusOK, policy)
	}
}

func handleUpdatePolicy(st *store.Store, catalog modelcatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		policyID := chi.URLParam(r, "policyID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"name", "is_default", "starts_at", "expires_at"} {
			if v, ok := body[field]; ok {
				updates[field] = v
			}
		}
		if v, ok := body["model_credit_rates"]; ok {
			raw, ok := v.(map[string]interface{})
			if !ok {
				writeError(w, http.StatusBadRequest, "bad_request", "model_credit_rates must be an object")
				return
			}
			normalized, err := normalizeRateMapKeysRaw(catalog, raw)
			if err != nil {
				writeUnknownModelsError(w, err)
				return
			}
			b, _ := json.Marshal(normalized)
			updates["model_credit_rates"] = b
		}
		for _, field := range []string{"credit_rules", "classic_rules"} {
			if v, ok := body[field]; ok {
				b, _ := json.Marshal(v)
				updates[field] = b
			}
		}
		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "no valid fields to update")
			return
		}

		if err := st.UpdatePolicy(policyID, updates); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update policy")
			return
		}

		policy, _ := st.GetPolicyByID(policyID)
		writeData(w, http.StatusOK, policy)
	}
}

func handleDeletePolicy(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireRole(w, r, types.RoleOwner, types.RoleMaintainer) {
			return
		}
		if err := st.DeletePolicy(chi.URLParam(r, "policyID")); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to delete policy")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
