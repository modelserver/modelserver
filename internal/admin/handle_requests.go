package admin

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func handleListRequests(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		caller := UserFromContext(r.Context())
		callerMember := MemberFromContext(r.Context())
		p := parsePagination(r)
		q := r.URL.Query()

		filters := store.RequestFilters{
			Model:    q.Get("model"),
			Status:   q.Get("status"),
			APIKeyID: q.Get("api_key_id"),
		}

		// Developers can only see their own requests.
		isDeveloper := callerMember != nil && callerMember.Role == types.RoleDeveloper
		if isDeveloper {
			filters.CreatedBy = caller.ID
		} else if cb := q.Get("created_by"); cb != "" {
			filters.CreatedBy = cb
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

		// Strip provider for non-superadmin users.
		if !caller.IsSuperadmin {
			for i := range requests {
				requests[i].Provider = ""
			}
		}

		writeList(w, requests, total, p.Page, p.Limit())
	}
}

func handleListAllRequests(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := parsePagination(r)
		q := r.URL.Query()

		filters := store.RequestFilters{
			Model:     q.Get("model"),
			Status:    q.Get("status"),
			CreatedBy: q.Get("created_by"),
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

		breakdown := q.Get("breakdown") // "model", "member", "daily"

		switch breakdown {
		case "model":
			data, err := st.GetUsageByModel(projectID, since, until)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
				return
			}
			writeData(w, http.StatusOK, data)
		case "member":
			p := parsePagination(r)
			data, total, err := st.GetUsageByMember(projectID, since, until, p.Limit(), p.Offset())
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
				return
			}
			writeList(w, data, total, p.Page, p.Limit())
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
