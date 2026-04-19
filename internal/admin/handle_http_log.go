package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/httplog"
	"github.com/modelserver/modelserver/internal/store"
)

func handleGetHttpLog(st *store.Store, bl *httplog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bl == nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable", "http logging is not configured")
			return
		}

		requestID := chi.URLParam(r, "requestID")
		if requestID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "missing request ID")
			return
		}

		req, err := st.GetRequest(requestID)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "request not found")
			return
		}

		user := UserFromContext(r.Context())
		if !user.IsSuperadmin {
			projectID := chi.URLParam(r, "projectID")
			if req.ProjectID != projectID {
				writeError(w, http.StatusForbidden, "forbidden", "request does not belong to this project")
				return
			}
		}

		if req.HttpLogPath == "" {
			writeError(w, http.StatusNotFound, "not_found", "no http log available for this request")
			return
		}

		data, err := bl.Retrieve(r.Context(), req.HttpLogPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to retrieve http log")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}
}
