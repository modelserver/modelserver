package types

import "time"

// LB policy constants.
const (
	LBPolicyWeightedRandom = "weighted_random" // Current behavior (default)
	LBPolicyRoundRobin     = "round_robin"     // Even distribution
	LBPolicyLeastConn      = "least_conn"      // Fewest active connections
)

// UpstreamGroup represents a pool of upstreams with LB and retry config (nginx: upstream {} block).
// Provider-heterogeneous: members may have different providers (e.g. anthropic + bedrock)
// as long as each member's SupportedModels covers the models routed to this group.
// Body transform is applied per-attempt using each upstream's provider, not per-group.
type UpstreamGroup struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	LBPolicy    string       `json:"lb_policy"`    // Load balancing policy
	RetryPolicy *RetryPolicy `json:"retry_policy"` // Retry/failover config
	Status      string       `json:"status"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// UpstreamGroupMember links an upstream to a group with group-specific overrides.
type UpstreamGroupMember struct {
	UpstreamGroupID string `json:"upstream_group_id"`
	UpstreamID      string `json:"upstream_id"`
	Weight          *int   `json:"weight,omitempty"` // nil = use upstream's default weight; 0 is a valid value (effectively disabled)
	IsBackup        bool   `json:"is_backup"`        // Only used when all primary upstreams fail (nginx: backup)
}

// Validation: AddMember must verify the upstream's SupportedModels overlaps with models
// routed to this group. Matching uses the routed model name (the name the client sends),
// NOT the ModelMap-resolved name -- ModelMap rewriting is the upstream's own concern and
// happens at request time, not at group membership validation time.
// Provider mismatch is allowed -- cross-provider retry is a feature.

// RetryPolicy configures retry and failover behavior for an upstream group.
type RetryPolicy struct {
	MaxRetries int           `json:"max_retries"` // Max cross-upstream retries (0 = no retry)
	RetryOn    []string      `json:"retry_on"`    // Error types to retry: "5xx", "timeout", "connection_error"
	RetryDelay time.Duration `json:"retry_delay"` // Delay between retries
}
