package admin

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
)

func handleListRequests(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		p := parsePagination(r)
		q := r.URL.Query()

		filters := store.RequestFilters{
			Model:    q.Get("model"),
			Status:   q.Get("status"),
			APIKeyID: q.Get("api_key_id"),
		}
		if since := q.Get("since"); since != "" {
			if t, err := time.Parse(time.RFC3339, since); err == nil {
				filters.Since = t
			}
		}
		if until := q.Get("until"); until != "" {
			if t, err := time.Parse(time.RFC3339, until); err == nil {
				filters.Until = t
			}
		}

		requests, total, err := st.ListRequests(projectID, p, filters)
		if err != nil {
			log.Printf("ListRequests error: project=%s filters=%+v err=%v", projectID, filters, err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to list requests")
			return
		}
		writeList(w, requests, total, p.Page, p.Limit())
	}
}

func handleListAllRequests(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := parsePagination(r)
		q := r.URL.Query()

		filters := store.RequestFilters{
			Model:  q.Get("model"),
			Status: q.Get("status"),
		}
		if since := q.Get("since"); since != "" {
			if t, err := time.Parse(time.RFC3339, since); err == nil {
				filters.Since = t
			}
		}
		if until := q.Get("until"); until != "" {
			if t, err := time.Parse(time.RFC3339, until); err == nil {
				filters.Until = t
			}
		}

		requests, total, err := st.ListAllRequests(p, filters)
		if err != nil {
			log.Printf("ListAllRequests error: filters=%+v err=%v", filters, err)
			writeError(w, http.StatusInternalServerError, "internal", "failed to list requests")
			return
		}
		writeList(w, requests, total, p.Page, p.Limit())
	}
}

func handleGetUsage(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		q := r.URL.Query()

		since := time.Now().AddDate(0, 0, -30) // Default: last 30 days.
		until := time.Now()
		if v := q.Get("since"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				since = t
			}
		}
		if v := q.Get("until"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				until = t
			}
		}

		breakdown := q.Get("breakdown") // "model", "key", "daily"

		switch breakdown {
		case "model":
			data, err := st.GetUsageByModel(projectID, since, until)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
				return
			}
			writeData(w, http.StatusOK, data)
		case "key":
			data, err := st.GetUsageByAPIKey(projectID, since, until)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
				return
			}
			writeData(w, http.StatusOK, data)
		case "daily":
			data, err := st.GetDailyUsage(projectID, since, until)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
				return
			}
			writeData(w, http.StatusOK, data)
		default:
			overview, err := st.GetUsageOverview(projectID, since, until)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
				return
			}
			writeData(w, http.StatusOK, overview)
		}
	}
}
