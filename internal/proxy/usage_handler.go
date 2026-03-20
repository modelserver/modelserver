package proxy

import (
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/internal/ratelimit"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// UsageProgress represents credit usage progress for a single window.
type UsageProgress struct {
	Window      string  `json:"window"`
	WindowType  string  `json:"window_type"`
	Scope       string  `json:"scope"`
	Percentage  float64 `json:"percentage"`
	WindowStart string  `json:"window_start"`
	WindowEnd   string  `json:"window_end"`
}

// UsageResponse is the response for GET /v1/usage.
type UsageResponse struct {
	Subscription *SubscriptionInfo `json:"subscription,omitempty"`
	Plan         string            `json:"plan,omitempty"`
	CreditUsage  []UsageProgress   `json:"credit_usage"`
	TotalUsage   *TotalUsageInfo   `json:"total_usage"`
}

// SubscriptionInfo summarizes the active subscription.
type SubscriptionInfo struct {
	ID        string `json:"id"`
	PlanName  string `json:"plan_name"`
	Status    string `json:"status"`
	StartsAt  string `json:"starts_at"`
	ExpiresAt string `json:"expires_at"`
}

// TotalUsageInfo summarizes total usage within the subscription period.
type TotalUsageInfo struct {
	RequestCount int64  `json:"request_count"`
	TotalTokens  int64  `json:"total_tokens"`
	Since        string `json:"since"`
	Until        string `json:"until"`
}

// HandleUsage returns the current usage progress for the authenticated user's subscription.
func (h *Handler) HandleUsage(w http.ResponseWriter, r *http.Request) {
	apiKey := APIKeyFromContext(r.Context())
	project := ProjectFromContext(r.Context())
	policy := PolicyFromContext(r.Context())
	subscription := SubscriptionFromContext(r.Context())

	if apiKey == nil || project == nil {
		writeProxyError(w, http.StatusUnauthorized, "missing auth context")
		return
	}

	resp := UsageResponse{
		CreditUsage: []UsageProgress{},
	}

	if subscription != nil {
		resp.Subscription = &SubscriptionInfo{
			ID:        subscription.ID,
			PlanName:  subscription.PlanName,
			Status:    subscription.Status,
			StartsAt:  subscription.StartsAt.Format(time.RFC3339),
			ExpiresAt: subscription.ExpiresAt.Format(time.RFC3339),
		}
		resp.Plan = subscription.PlanName

		// Compute total usage within subscription period.
		overview, err := h.store.GetUsageOverview(project.ID, subscription.StartsAt, subscription.ExpiresAt)
		if err == nil {
			resp.TotalUsage = &TotalUsageInfo{
				RequestCount: overview["request_count"].(int64),
				TotalTokens:  overview["total_tokens"].(int64),
				Since:        subscription.StartsAt.Format(time.RFC3339),
				Until:        subscription.ExpiresAt.Format(time.RFC3339),
			}
		}
	}

	if policy != nil {
		for _, rule := range policy.CreditRules {
			progress, err := computeCreditProgress(h.store, project.ID, apiKey.ID, rule)
			if err != nil {
				h.logger.Error("failed to compute credit progress", "error", err, "window", rule.Window)
				continue
			}
			resp.CreditUsage = append(resp.CreditUsage, *progress)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func computeCreditProgress(st *store.Store, projectID, apiKeyID string, rule types.CreditRule) (*UsageProgress, error) {
	now := time.Now().UTC()
	windowStart := computeWindowStart(now, rule.Window, rule.WindowType, rule.AnchorTime)
	windowEnd := computeWindowEnd(windowStart, rule.Window, rule.WindowType)

	var used float64
	var err error

	if rule.EffectiveScope() == types.CreditScopeProject {
		used, err = st.SumCreditsInWindowByProject(projectID, windowStart)
	} else {
		used, err = st.SumCreditsInWindow(apiKeyID, windowStart)
	}
	if err != nil {
		return nil, err
	}

	percentage := 0.0
	if rule.MaxCredits > 0 {
		percentage = (used / float64(rule.MaxCredits)) * 100
		if percentage > 100 {
			percentage = 100
		}
		// Round to 2 decimal places.
		percentage = math.Round(percentage*100) / 100
	}

	return &UsageProgress{
		Window:      rule.Window,
		WindowType:  rule.WindowType,
		Scope:       rule.EffectiveScope(),
		Percentage:  percentage,
		WindowStart: windowStart.Format(time.RFC3339),
		WindowEnd:   windowEnd.Format(time.RFC3339),
	}, nil
}

func computeWindowStart(now time.Time, window, windowType string, anchorTime *time.Time) time.Time {
	return ratelimit.WindowStartTimeAt(now, window, windowType, anchorTime)
}

func computeWindowEnd(windowStart time.Time, window, windowType string) time.Time {
	if windowType == types.WindowTypeCalendar {
		switch window {
		case "1w":
			return windowStart.AddDate(0, 0, 7)
		case "1M":
			return windowStart.AddDate(0, 1, 0)
		}
	}
	if windowType == types.WindowTypeFixed {
		return windowStart.Add(ratelimit.ParseDurationStr(window))
	}
	// Sliding window: end is now.
	return time.Now().UTC()
}

