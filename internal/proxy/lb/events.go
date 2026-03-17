package lb

import (
	"context"
	"log/slog"
)

// RoutingEvent is emitted on every proxy attempt for observability.
// Logged at INFO level, queryable via structured log tooling.
type RoutingEvent struct {
	RequestID   string  `json:"request_id"`
	ProjectID   string  `json:"project_id"`
	Model       string  `json:"model"`
	RouteID     string  `json:"route_id"`              // Which route matched ("auto" if fallback)
	GroupID     string  `json:"group_id"`               // Which upstream group
	UpstreamID  string  `json:"upstream_id"`            // Selected upstream
	Provider    string  `json:"provider"`
	LBPolicy    string  `json:"lb_policy"`              // Which LB policy was used
	Attempt     int     `json:"attempt"`                // 1 = primary, 2+ = retry
	RetryReason string  `json:"retry_reason,omitempty"` // Why previous attempt failed
	SelectionMs float64 `json:"selection_ms"`           // Time spent in LB selection
	Outcome     string  `json:"outcome"`                // "success", "retry", "error"
}

// EmitRoutingEvent logs a routing decision as a structured slog event.
func EmitRoutingEvent(logger *slog.Logger, event RoutingEvent) {
	logger.LogAttrs(context.Background(), slog.LevelInfo, "routing_decision",
		slog.String("request_id", event.RequestID),
		slog.String("project_id", event.ProjectID),
		slog.String("model", event.Model),
		slog.String("route_id", event.RouteID),
		slog.String("group_id", event.GroupID),
		slog.String("upstream_id", event.UpstreamID),
		slog.String("provider", event.Provider),
		slog.String("lb_policy", event.LBPolicy),
		slog.Int("attempt", event.Attempt),
		slog.String("retry_reason", event.RetryReason),
		slog.Float64("selection_ms", event.SelectionMs),
		slog.String("outcome", event.Outcome),
	)
}
