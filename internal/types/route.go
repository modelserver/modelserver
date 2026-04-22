package types

import "time"

// Route maps a set of canonical model names to an upstream group
// (nginx: location block). The route matches a request when its
// canonical model name (post-alias-resolution) appears in ModelNames.
// Ordering among competing routes is given by MatchPriority.
type Route struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id,omitempty"` // "" = global route
	ModelNames      []string          `json:"model_names"`          // Canonical model names only (no aliases, no globs)
	RequestKinds    []string          `json:"request_kinds"`        // Wire-level endpoint kinds this route serves; values from internal/types/request_kind.go
	UpstreamGroupID string            `json:"upstream_group_id"`
	MatchPriority   int               `json:"match_priority"` // Higher = matched first
	Conditions      map[string]string `json:"conditions,omitempty"`
	Status          string            `json:"status"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}
