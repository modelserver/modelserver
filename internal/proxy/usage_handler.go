package proxy

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// UsageProgress represents credit usage progress for a single window.
type UsageProgress struct {
	Window      string  `json:"window"`
	WindowType  string  `json:"window_type"`
	Scope       string  `json:"scope"`
	MaxCredits  int64   `json:"max_credits"`
	UsedCredits float64 `json:"used_credits"`
	Remaining   float64 `json:"remaining"`
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
	RequestCount int64   `json:"request_count"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCredits float64 `json:"total_credits"`
	Since        string  `json:"since"`
	Until        string  `json:"until"`
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
				TotalCredits: overview["total_credits"].(float64),
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
	windowStart := computeWindowStart(now, rule.Window, rule.WindowType)
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

	remaining := float64(rule.MaxCredits) - used
	if remaining < 0 {
		remaining = 0
	}
	percentage := 0.0
	if rule.MaxCredits > 0 {
		percentage = (used / float64(rule.MaxCredits)) * 100
		if percentage > 100 {
			percentage = 100
		}
	}

	return &UsageProgress{
		Window:      rule.Window,
		WindowType:  rule.WindowType,
		Scope:       rule.EffectiveScope(),
		MaxCredits:  rule.MaxCredits,
		UsedCredits: used,
		Remaining:   remaining,
		Percentage:  percentage,
		WindowStart: windowStart.Format(time.RFC3339),
		WindowEnd:   windowEnd.Format(time.RFC3339),
	}, nil
}

// computeWindowStart calculates the start of the current window.
func computeWindowStart(now time.Time, window, windowType string) time.Time {
	if windowType == "calendar" {
		switch window {
		case "1w":
			// Monday 00:00 UTC of this week.
			weekday := now.Weekday()
			if weekday == time.Sunday {
				weekday = 7
			}
			daysBack := int(weekday) - 1
			start := now.AddDate(0, 0, -daysBack)
			return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		case "1M":
			// First of current month.
			return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
	}

	// Sliding window: parse as duration and subtract.
	d := parseDuration(window)
	return now.Add(-d)
}

// computeWindowEnd calculates the end of the current window.
func computeWindowEnd(windowStart time.Time, window, windowType string) time.Time {
	if windowType == "calendar" {
		switch window {
		case "1w":
			return windowStart.AddDate(0, 0, 7)
		case "1M":
			return windowStart.AddDate(0, 1, 0)
		}
	}

	// Sliding window: end is now.
	return time.Now().UTC()
}

// parseDuration parses window strings like "5h", "24h", "1h" into time.Duration.
func parseDuration(window string) time.Duration {
	d, err := time.ParseDuration(window)
	if err != nil {
		// Fallback: treat "1d" as 24h.
		switch window {
		case "1d":
			return 24 * time.Hour
		case "7d":
			return 7 * 24 * time.Hour
		case "30d":
			return 30 * 24 * time.Hour
		default:
			return time.Hour
		}
	}
	return d
}
