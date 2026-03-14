package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
)

func handleListTraces(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		p := parsePagination(r)
		traces, total, err := st.ListTraces(projectID, p)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list traces")
			return
		}
		writeList(w, traces, total, p.Page, p.Limit())
	}
}

func handleGetTrace(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		trace, err := st.GetTraceByID(chi.URLParam(r, "traceID"))
		if err != nil || trace == nil {
			writeError(w, http.StatusNotFound, "not_found", "trace not found")
			return
		}
		writeData(w, http.StatusOK, trace)
	}
}

func handleListTraceRequests(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		traceID := chi.URLParam(r, "traceID")
		requests, err := st.ListRequestsByTraceID(traceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to list trace requests")
			return
		}
		writeData(w, http.StatusOK, requests)
	}
}

