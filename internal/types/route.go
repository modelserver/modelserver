package types

import "time"

// Route maps a model pattern to an upstream group (nginx: location block).
type Route struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id,omitempty"` // "" = global route
	ModelPattern    string            `json:"model_pattern"`        // Glob pattern: "claude-*", "gpt-4o", "*"
	UpstreamGroupID string            `json:"upstream_group_id"`    // Which upstream group to use
	MatchPriority   int               `json:"match_priority"`       // Higher = matched first
	Conditions      map[string]string `json:"conditions,omitempty"` // Extra match conditions for future use (e.g. "streaming": "true", "thinking": "enabled")
	Status          string            `json:"status"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}
