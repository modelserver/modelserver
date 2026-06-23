package admin

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/billing"
	"github.com/modelserver/modelserver/internal/modelcatalog"
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
			Model:       q.Get("model"),
			RequestKind: q.Get("request_kind"),
			Status:      q.Get("status"),
			APIKeyID:    q.Get("api_key_id"),
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
			Model:       q.Get("model"),
			RequestKind: q.Get("request_kind"),
			Status:      q.Get("status"),
			CreatedBy:   q.Get("created_by"),
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

func handleGetUsage(st *store.Store, catalog modelcatalog.Catalog, creditPriceCNYFen int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectID")
		q := r.URL.Query()

		since := time.Now().AddDate(0, 0, -30) // Default: last 30 days.
		until := time.Now()
		userProvidedWindow := false
		if v := q.Get("since"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				since = t
				userProvidedWindow = true
			}
		}
		if v := q.Get("until"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				until = t
				userProvidedWindow = true
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
			// Member ranking is always scoped to the active subscription
			// period (StartsAt → now), so the table reflects "who has burned
			// the most credits this billing cycle" regardless of any
			// since/until query params. Falls back to the user-supplied
			// window when there is no active subscription.
			memberSince, memberUntil := since, until
			if sub, err := st.GetActiveSubscription(projectID); err == nil && sub != nil {
				memberSince = sub.StartsAt
				memberUntil = time.Now()
			}
			data, total, err := st.GetUsageByMember(projectID, memberSince, memberUntil, p.Limit(), p.Offset())
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
			// Look up the active subscription + plan only when the caller
			// did not impose a window of their own. resolveOverviewPeriod
			// then picks the final window and provenance.
			var activeSub *types.Subscription
			var activeSubPlan *types.Plan
			if !userProvidedWindow {
				s, err := st.GetActiveSubscription(projectID)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
					return
				}
				if s != nil && s.PlanID != "" {
					p, err := st.GetPlanByID(s.PlanID)
					if err != nil {
						writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
						return
					}
					if p != nil {
						activeSub = s
						activeSubPlan = p
					}
				}
			}

			var periodSource string
			var sub *types.Subscription
			var plan *types.Plan
			since, until, periodSource, sub, plan = resolveOverviewPeriod(
				userProvidedWindow, since, until, activeSub, activeSubPlan)

			overview, err := st.GetUsageOverview(projectID, since, until)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
				return
			}
			overview["period_source"] = periodSource

			if !userProvidedWindow {
				sums, err := st.GetPerModelTokenSums(projectID, since, until)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
					return
				}
				extraCredits, err := st.GetExtraUsageSpendInWindow(projectID, since, until)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "internal", "failed to get usage")
					return
				}
				// savings.go gates out non-CNY subscriptions (USD cents must
				// not mix into a fen-denominated aggregate). Currency is
				// denormalized onto the subscription by DeliverOrder.
				var activeCurrency string
				if sub != nil {
					activeCurrency = sub.Currency
				}
				cb := billing.ComputeCostBreakdown(sums, extraCredits, catalog, creditPriceCNYFen,
					sub, plan, since, until, activeCurrency)
				overview["cost_breakdown"] = cb
			}

			writeData(w, http.StatusOK, overview)
		}
	}
}
