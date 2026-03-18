package admin

import (
	"net/http"
	"time"
)

// HealthProvider exposes routing health state from the router/health-checker
// subsystem. The admin API calls this to serve GET /api/v1/routing/health.
//
// TODO: Wire a concrete implementation once the Router is integrated in the
// server bootstrap (Phase 9/10). For now, the handler accepts this interface
// and can be tested with a stub.
type HealthProvider interface {
	GetRoutingHealth() RoutingHealthResponse
}

// RoutingHealthResponse is the top-level response for the routing health endpoint.
type RoutingHealthResponse struct {
	Upstreams []UpstreamHealth `json:"upstreams"`
	Groups    []GroupHealth    `json:"groups"`
}

// UpstreamHealth describes the runtime health of a single upstream.
type UpstreamHealth struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Provider          string     `json:"provider"`
	CircuitState      string     `json:"circuit_state"`
	HealthStatus      string     `json:"health_status"`
	ActiveConnections int64      `json:"active_connections"`
	RecentErrors      int64      `json:"recent_errors"`
	LastCheckAt       *time.Time `json:"last_check_at"`
	LastErrorAt       *time.Time `json:"last_error_at"`
}

// GroupHealth describes the runtime health of an upstream group.
type GroupHealth struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	LBPolicy       string `json:"lb_policy"`
	HealthyMembers int    `json:"healthy_members"`
	TotalMembers   int    `json:"total_members"`
}

func handleRoutingHealth(hp HealthProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp := hp.GetRoutingHealth()
		writeData(w, http.StatusOK, resp)
	}
}
