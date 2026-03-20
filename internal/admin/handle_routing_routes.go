package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
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

func handleCreateRoutingRoute(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProjectID       string            `json:"project_id"`
			ModelPattern    string            `json:"model_pattern"`
			UpstreamGroupID string            `json:"upstream_group_id"`
			MatchPriority   int               `json:"match_priority"`
			Conditions      map[string]string `json:"conditions"`
			Status          string            `json:"status"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.ModelPattern == "" || body.UpstreamGroupID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "model_pattern and upstream_group_id are required")
			return
		}

		status := body.Status
		if status == "" {
			status = "active"
		}

		route := &types.Route{
			ProjectID:       body.ProjectID,
			ModelPattern:    body.ModelPattern,
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

func handleUpdateRoutingRoute(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		routeID := chi.URLParam(r, "routeID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"project_id", "model_pattern", "upstream_group_id", "match_priority", "conditions", "status"} {
			if v, ok := body[field]; ok {
				// Convert empty project_id to NULL for the UUID column.
				if field == "project_id" {
					if s, ok := v.(string); ok && s == "" {
						v = nil
					}
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
