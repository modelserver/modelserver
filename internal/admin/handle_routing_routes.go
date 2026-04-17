package admin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListRoutingRoutes(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := parsePagination(r)
		routes, total, err := st.ListRoutesPaginated(p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list routing routes")
			return
		}
		if routes == nil {
			routes = []types.Route{}
		}
		writeList(w, routes, total, p.Page, p.Limit())
	}
}

func handleCreateRoutingRoute(st *store.Store, catalog modelcatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProjectID       string            `json:"project_id"`
			ModelNames      []string          `json:"model_names"`
			UpstreamGroupID string            `json:"upstream_group_id"`
			MatchPriority   int               `json:"match_priority"`
			Conditions      map[string]string `json:"conditions"`
			Status          string            `json:"status"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if len(body.ModelNames) == 0 || body.UpstreamGroupID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "model_names and upstream_group_id are required")
			return
		}

		canonical, err := catalog.NormalizeNames(body.ModelNames)
		if err != nil {
			writeUnknownModelsError(w, err)
			return
		}

		status := body.Status
		if status == "" {
			status = "active"
		}

		route := &types.Route{
			ProjectID:       body.ProjectID,
			ModelNames:      canonical,
			UpstreamGroupID: body.UpstreamGroupID,
			MatchPriority:   body.MatchPriority,
			Conditions:      body.Conditions,
			Status:          status,
		}

		if err := st.CreateRoute(route); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create routing route")
			return
		}
		writeData(w, http.StatusCreated, route)
	}
}

func handleUpdateRoutingRoute(st *store.Store, catalog modelcatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		routeID := chi.URLParam(r, "routeID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"project_id", "model_names", "upstream_group_id", "match_priority", "conditions", "status"} {
			if v, ok := body[field]; ok {
				switch field {
				case "project_id":
					if s, ok := v.(string); ok && s == "" {
						v = nil
					}
				case "model_names":
					names, ok := toStringSlice(v)
					if !ok {
						writeError(w, http.StatusBadRequest, "bad_request", "model_names must be an array of strings")
						return
					}
					canonical, err := catalog.NormalizeNames(names)
					if err != nil {
						writeUnknownModelsError(w, err)
						return
					}
					v = canonical
				}
				updates[field] = v
			}
		}
		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "no valid fields to update")
			return
		}

		if err := st.UpdateRoute(routeID, updates); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update routing route")
			return
		}

		route, _ := st.GetRouteByID(routeID)
		writeData(w, http.StatusOK, route)
	}
}

func handleDeleteRoutingRoute(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := st.DeleteRoute(chi.URLParam(r, "routeID")); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to delete routing route")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// toStringSlice turns an interface{} decoded from JSON into []string.
// Accepts []string or []interface{}-of-string; returns (nil, false) otherwise.
func toStringSlice(v interface{}) ([]string, bool) {
	switch s := v.(type) {
	case []string:
		return s, true
	case []interface{}:
		out := make([]string, 0, len(s))
		for _, e := range s {
			str, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	case nil:
		return []string{}, true
	default:
		return nil, false
	}
}

// writeUnknownModelsError maps a *modelcatalog.UnknownModelsError to a 400
// response whose `details` carries every unknown name. Any other error is
// treated as a 500.
func writeUnknownModelsError(w http.ResponseWriter, err error) {
	var uerr *modelcatalog.UnknownModelsError
	if errors.As(err, &uerr) {
		writeErrorWithDetails(w, http.StatusBadRequest, "unknown_model", uerr.Error(), map[string]interface{}{"unknown": uerr.Names})
		return
	}
	writeError(w, http.StatusInternalServerError, "internal", err.Error())
}
