package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// modelLegalChars is the superset of characters allowed in canonical names
// and aliases: lowercase ASCII, digits, dot, underscore, dash.
const modelLegalChars = "abcdefghijklmnopqrstuvwxyz0123456789._-"

// modelListResponseRow is the LIST payload element. It embeds the Model and
// adds the per-row reference counts so the dashboard can disable Delete
// without a separate round trip.
type modelListResponseRow struct {
	types.Model
	ReferenceCounts store.ModelReferenceCounts `json:"reference_counts"`
}

func handleListModels(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var (
			models []types.Model
			err    error
		)
		if status := r.URL.Query().Get("status"); status != "" {
			models, err = st.ListModelsByStatus(status)
		} else {
			models, err = st.ListModels()
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list models")
			return
		}
		rows := make([]modelListResponseRow, 0, len(models))
		for _, m := range models {
			counts, err := st.ModelReferenceCountsFor(m.Name)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to count references: "+err.Error())
				return
			}
			rows = append(rows, modelListResponseRow{Model: m, ReferenceCounts: counts})
		}
		writeData(w, http.StatusOK, rows)
	}
}

func handleGetModel(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		m, err := st.GetModelByName(name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to get model")
			return
		}
		if m == nil {
			writeError(w, http.StatusNotFound, "not_found", "model not found")
			return
		}
		writeData(w, http.StatusOK, m)
	}
}

func handleCreateModel(st *store.Store, catalog modelcatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name              string              `json:"name"`
			DisplayName       string              `json:"display_name"`
			Description       string              `json:"description"`
			Aliases           []string            `json:"aliases"`
			DefaultCreditRate *types.CreditRate   `json:"default_credit_rate"`
			Status            string              `json:"status"`
			Publisher         string              `json:"publisher"`
			Metadata          types.ModelMetadata `json:"metadata"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if err := validateModelPayload(body.Name, body.Aliases, body.Status, body.DefaultCreditRate); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if err := validatePublisher(body.Publisher); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if body.DisplayName == "" {
			body.DisplayName = body.Name
		}

		m := &types.Model{
			Name:              body.Name,
			DisplayName:       body.DisplayName,
			Description:       body.Description,
			Aliases:           body.Aliases,
			DefaultCreditRate: body.DefaultCreditRate,
			Status:            body.Status,
			Publisher:         body.Publisher,
			Metadata:          body.Metadata,
		}
		if err := st.CreateModel(m); err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "conflict", err.Error())
				return
			}
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}

		// Refresh the catalog so subsequent admin writes (upstreams etc.) and
		// proxy lookups see the new row immediately, without waiting for the
		// 30s reload tick.
		refreshCatalog(st, catalog)

		writeData(w, http.StatusCreated, m)
	}
}

func handleUpdateModel(st *store.Store, catalog modelcatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")

		existing, err := st.GetModelByName(name)
		if err != nil || existing == nil {
			writeError(w, http.StatusNotFound, "not_found", "model not found")
			return
		}

		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if _, ok := body["name"]; ok {
			writeError(w, http.StatusBadRequest, "bad_request", "canonical name is immutable; create a new model and retire this one instead")
			return
		}

		updates := make(map[string]interface{})
		if v, ok := body["display_name"]; ok {
			updates["display_name"] = v
		}
		if v, ok := body["description"]; ok {
			updates["description"] = v
		}
		if v, ok := body["status"]; ok {
			status, _ := v.(string)
			if status != types.ModelStatusActive && status != types.ModelStatusDisabled {
				writeError(w, http.StatusBadRequest, "bad_request", "status must be active or disabled")
				return
			}
			updates["status"] = status
		}
		if v, ok := body["aliases"]; ok {
			aliases, ok := toStringSlice(v)
			if !ok {
				writeError(w, http.StatusBadRequest, "bad_request", "aliases must be an array of strings")
				return
			}
			if err := validateAliases(existing.Name, aliases); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
			updates["aliases"] = aliases
		}
		if v, ok := body["default_credit_rate"]; ok {
			if v == nil {
				updates["default_credit_rate"] = nil
			} else {
				b, err := json.Marshal(v)
				if err != nil {
					writeError(w, http.StatusBadRequest, "bad_request", "invalid default_credit_rate")
					return
				}
				var rate types.CreditRate
				if err := json.Unmarshal(b, &rate); err != nil {
					writeError(w, http.StatusBadRequest, "bad_request", "invalid default_credit_rate")
					return
				}
				if err := validateCreditRate(&rate); err != nil {
					writeError(w, http.StatusBadRequest, "bad_request", err.Error())
					return
				}
				updates["default_credit_rate"] = b
			}
		}
		if v, ok := body["publisher"]; ok {
			pub, _ := v.(string)
			if err := validatePublisher(pub); err != nil {
				writeError(w, http.StatusBadRequest, "bad_request", err.Error())
				return
			}
			updates["publisher"] = pub
		}
		if v, ok := body["metadata"]; ok {
			b, _ := json.Marshal(v)
			updates["metadata"] = b
		}

		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "no valid fields to update")
			return
		}
		if err := st.UpdateModel(name, updates); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}

		refreshCatalog(st, catalog)

		updated, _ := st.GetModelByName(name)
		writeData(w, http.StatusOK, updated)
	}
}

func handleDeleteModel(st *store.Store, catalog modelcatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		existing, err := st.GetModelByName(name)
		if err != nil || existing == nil {
			writeError(w, http.StatusNotFound, "not_found", "model not found")
			return
		}

		counts, err := st.ModelReferenceCountsFor(name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to count references")
			return
		}
		if counts.Total() > 0 {
			writeErrorWithDetails(w, http.StatusConflict, "conflict",
				"model is referenced; set status=disabled or clear references first",
				counts)
			return
		}

		if err := st.DeleteModel(name); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}

		refreshCatalog(st, catalog)

		w.WriteHeader(http.StatusNoContent)
	}
}

// refreshCatalog repopulates the in-memory catalog from the store. Called
// after every successful write to `models` so the router and admin
// validation paths see the change immediately. On error the in-memory view
// stays as-is until the periodic 30s reload tick recovers; we log so that
// silent staleness doesn't go unnoticed.
func refreshCatalog(st *store.Store, catalog modelcatalog.Catalog) {
	models, err := st.ListModels()
	if err != nil {
		slog.Default().Error("admin: failed to refresh model catalog after write", "error", err)
		return
	}
	catalog.Swap(models)
}

// validateModelPayload runs the create-time invariant checks that are
// cheap to express in Go. The trigger enforces the same rules at the DB
// level as a second line of defence.
func validateModelPayload(name string, aliases []string, status string, rate *types.CreditRate) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if err := validateModelName(name); err != nil {
		return err
	}
	if err := validateAliases(name, aliases); err != nil {
		return err
	}
	if status != "" && status != types.ModelStatusActive && status != types.ModelStatusDisabled {
		return fmt.Errorf("status must be active or disabled")
	}
	return validateCreditRate(rate)
}

func validateModelName(s string) error {
	if s != strings.ToLower(s) {
		return fmt.Errorf("name must be lowercase: %q", s)
	}
	for _, r := range s {
		if !strings.ContainsRune(modelLegalChars, r) {
			return fmt.Errorf("illegal character %q in name %q; allowed: %s", string(r), s, modelLegalChars)
		}
	}
	return nil
}

func validateAliases(canonical string, aliases []string) error {
	seen := make(map[string]struct{}, len(aliases))
	for _, a := range aliases {
		if err := validateModelName(a); err != nil {
			return fmt.Errorf("alias: %w", err)
		}
		if a == canonical {
			return fmt.Errorf("alias %q cannot equal canonical name", a)
		}
		if _, dup := seen[a]; dup {
			return fmt.Errorf("duplicate alias %q", a)
		}
		seen[a] = struct{}{}
	}
	return nil
}

// validatePublisher rejects empty strings. The controlled vocabulary is
// intentionally not enforced here so new publishers can be rolled out
// without a code change — admins just enter the string. Subscription
// eligibility switches on known values; anything unrecognised is treated as
// "not anthropic" = eligible.
func validatePublisher(p string) error {
	if p == "" {
		return fmt.Errorf("publisher is required (e.g. anthropic, openai, google)")
	}
	return nil
}

func validateCreditRate(r *types.CreditRate) error {
	if r == nil {
		return nil
	}
	if r.InputRate < 0 || r.OutputRate < 0 || r.CacheCreationRate < 0 || r.CacheReadRate < 0 {
		return fmt.Errorf("credit rates must be non-negative")
	}
	if r.LongContext != nil {
		if r.LongContext.ThresholdInputTokens <= 0 {
			return fmt.Errorf("long_context.threshold_input_tokens must be positive")
		}
		if r.LongContext.InputMultiplier <= 0 || r.LongContext.OutputMultiplier <= 0 {
			return fmt.Errorf("long_context multipliers must be positive")
		}
	}
	return nil
}

// isUniqueViolation reports whether err wraps a PostgreSQL unique-violation
// (pk collision on an existing canonical name).
func isUniqueViolation(err error) bool {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		return pgerr.Code == "23505"
	}
	return false
}
