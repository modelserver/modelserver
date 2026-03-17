package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListUpstreamGroups(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groups, err := st.ListUpstreamGroupsWithMembers()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list upstream groups")
			return
		}
		writeData(w, http.StatusOK, groups)
	}
}

func handleCreateUpstreamGroup(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name        string              `json:"name"`
			LBPolicy    string              `json:"lb_policy"`
			RetryPolicy *types.RetryPolicy  `json:"retry_policy"`
			Status      string              `json:"status"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.Name == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "name is required")
			return
		}

		lbPolicy := body.LBPolicy
		if lbPolicy == "" {
			lbPolicy = types.LBPolicyWeightedRandom
		}

		status := body.Status
		if status == "" {
			status = "active"
		}

		g := &types.UpstreamGroup{
			Name:        body.Name,
			LBPolicy:    lbPolicy,
			RetryPolicy: body.RetryPolicy,
			Status:      status,
		}

		if err := st.CreateUpstreamGroup(g); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to create upstream group")
			return
		}
		writeData(w, http.StatusCreated, g)
	}
}

func handleGetUpstreamGroup(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g, err := st.GetUpstreamGroupByID(chi.URLParam(r, "groupID"))
		if err != nil || g == nil {
			writeError(w, http.StatusNotFound, "not_found", "upstream group not found")
			return
		}
		writeData(w, http.StatusOK, g)
	}
}

func handleUpdateUpstreamGroup(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groupID := chi.URLParam(r, "groupID")
		var body map[string]interface{}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}

		updates := make(map[string]interface{})
		for _, field := range []string{"name", "lb_policy", "retry_policy", "status"} {
			if v, ok := body[field]; ok {
				updates[field] = v
			}
		}
		if len(updates) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "no valid fields to update")
			return
		}

		if err := st.UpdateUpstreamGroup(groupID, updates); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to update upstream group")
			return
		}

		g, _ := st.GetUpstreamGroupByID(groupID)
		writeData(w, http.StatusOK, g)
	}
}

func handleDeleteUpstreamGroup(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := st.DeleteUpstreamGroup(chi.URLParam(r, "groupID")); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to delete upstream group")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleAddGroupMember(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groupID := chi.URLParam(r, "groupID")
		var body struct {
			UpstreamID string `json:"upstream_id"`
			Weight     *int   `json:"weight,omitempty"`
			IsBackup   bool   `json:"is_backup"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid request body")
			return
		}
		if body.UpstreamID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "upstream_id is required")
			return
		}

		m := &types.UpstreamGroupMember{
			UpstreamGroupID: groupID,
			UpstreamID:      body.UpstreamID,
			Weight:          body.Weight,
			IsBackup:        body.IsBackup,
		}

		if err := st.AddUpstreamGroupMember(m); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to add group member")
			return
		}
		writeData(w, http.StatusCreated, m)
	}
}

func handleRemoveGroupMember(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groupID := chi.URLParam(r, "groupID")
		upstreamID := chi.URLParam(r, "upstreamID")
		if err := st.RemoveUpstreamGroupMember(groupID, upstreamID); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to remove group member")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleListGroupMembers(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groupID := chi.URLParam(r, "groupID")
		members, err := st.ListUpstreamGroupMembers(groupID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list group members")
			return
		}
		writeData(w, http.StatusOK, members)
	}
}
