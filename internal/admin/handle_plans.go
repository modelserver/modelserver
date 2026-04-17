package admin

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// normalizeRateMapKeys normalizes every non-sentinel key of a model-credit-rate
// map against the catalog. The sentinel `_default` is preserved verbatim —
// it is a plan-wide fallback, not a model name.
func normalizeRateMapKeys(catalog modelcatalog.Catalog, in map[string]types.CreditRate) (map[string]types.CreditRate, error) {
	if len(in) == 0 {
		return in, nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		if k == "_default" {
			continue
		}
		keys = append(keys, k)
	}
	canonical, err := catalog.NormalizeNames(keys)
	if err != nil {
		return nil, err
	}
	out := make(map[string]types.CreditRate, len(in))
	for i, k := range keys {
		out[canonical[i]] = in[k]
	}
	if def, ok := in["_default"]; ok {
		out["_default"] = def
	}
	return out, nil
}

// normalizeRateMapKeysRaw is the map[string]interface{} variant used by
// update handlers whose body is decoded into map[string]interface{}.
func normalizeRateMapKeysRaw(catalog modelcatalog.Catalog, raw map[string]interface{}) (map[string]interface{}, error) {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		if k == "_default" {
			continue
		}
		keys = append(keys, k)
	}
	canonical, err := catalog.NormalizeNames(keys)
	if err != nil {
		return nil, err
	}
	out := make(map[string]interface{}, len(raw))
	for i, k := range keys {
		out[canonical[i]] = raw[k]
	}
	if def, ok := raw["_default"]; ok {
		out["_default"] = def
	}
	return out, nil
}

func handleListPlans(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := parsePagination(r)
		plans, total, err := st.ListPlansPaginated(p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list plans")
			return
		}
		if plans == nil {
			plans = []types.Plan{}
		}
		writeList(w, plans, total, p.Page, p.Limit())
	}
}

func handleCreatePlan(st *store.Store, catalog modelcatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name             string                      `json:"name"`
			Slug             string                      `json:"slug"`
			DisplayName      string                      `json:"display_name"`
			Description      string                      `json:"description"`
			TierLevel        int                         `json:"tier_level"`
			GroupTag         string                      `json:"group_tag"`
			PricePerPeriod   int64                       `json:"price_per_period"`
			PeriodMonths     int                         `json:"period_months"`
			CreditRules      []types.CreditRule          `json:"credit_rules"`
			ModelCreditRates map[string]types.CreditRate `json:"model_credit_rates"`
			ClassicRules     []types.ClassicRule         `json:"classic_rules"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Name == "" || body.Slug == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name and slug are required")
			return
		}
		if body.PeriodMonths <= 0 {
			body.PeriodMonths = 1
		}
		if err := validateCreditRules(body.CreditRules); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		rates, err := normalizeRateMapKeys(catalog, body.ModelCreditRates)
		if err != nil {
			writeUnknownModelsError(w, err)
			return
		}

		plan := &types.Plan{
			Name:             body.Name,
			Slug:             body.Slug,
			DisplayName:      body.DisplayName,
			Description:      body.Description,
			TierLevel:        body.TierLevel,
			GroupTag:         body.GroupTag,
			PricePerPeriod:   body.PricePerPeriod,
			PeriodMonths:     body.PeriodMonths,
			CreditRules:      body.CreditRules,
			ModelCreditRates: rates,
			ClassicRules:     body.ClassicRules,
			IsActive:         true,
		}
		if err := st.CreatePlan(plan); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create plan: "+err.Error())
			return
		}
		writeData(w, http.StatusCreated, plan)
	}
}

func handleGetPlan(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plan, err := st.GetPlanByID(chi.URLParam(r, "planID"))
		if err != nil || plan == nil {
			writeError(w, http.StatusNotFound, "not_found", "plan not found")
			return
		}
		writeData(w, http.StatusOK, plan)
	}
}

func handleUpdatePlan(st *store.Store, catalog modelcatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID := chi.URLParam(r, "planID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"name", "slug", "display_name", "description", "tier_level",
			"group_tag", "price_per_period", "period_months", "is_active"} {
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
				b, err := json.Marshal(v)
				if err == nil {
					updates[field] = b
				}
			}
		}

		// Validate credit_rules if present.
		if raw, ok := body["credit_rules"]; ok {
			b, _ := json.Marshal(raw)
			var rules []types.CreditRule
			if err := json.Unmarshal(b, &rules); err == nil {
				if err := validateCreditRules(rules); err != nil {
					writeError(w, http.StatusBadRequest, "bad_request", err.Error())
					return
				}
			}
		}

		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "no valid fields to update")
			return
		}

		if err := st.UpdatePlan(planID, updates); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update plan")
			return
		}

		plan, _ := st.GetPlanByID(planID)
		writeData(w, http.StatusOK, plan)
	}
}

func handleDeletePlan(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID := chi.URLParam(r, "planID")
		if err := st.DeletePlan(planID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to delete plan")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleListAvailablePlans(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		plans, err := st.ListPlansForProject(projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list available plans")
			return
		}
		writeData(w, http.StatusOK, plans)
	}
}

// validateCreditRules checks for invalid CreditRule configurations.
func validateCreditRules(rules []types.CreditRule) error {
	for _, rule := range rules {
		if rule.WindowType == types.WindowTypeFixed && len(rule.Window) > 0 && rule.Window[len(rule.Window)-1] == 'M' {
			return fmt.Errorf("month-based window %q is not supported with window_type \"fixed\" — use duration-based intervals like \"7d\"", rule.Window)
		}
	}
	return nil
}
