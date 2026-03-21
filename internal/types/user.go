package types

import "time"

// Role constants for project membership.
const (
	RoleOwner      = "owner"
	RoleMaintainer = "maintainer"
	RoleDeveloper  = "developer"
)

// User status constants.
const (
	UserStatusActive   = "active"
	UserStatusDisabled = "disabled"
)

// User represents an authenticated user of the system.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Nickname     string    `json:"nickname"`
	Picture      string    `json:"picture,omitempty"`
	IsSuperadmin bool      `json:"is_superadmin"`
	MaxProjects  int       `json:"max_projects"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// OAuthConnections is populated when needed.
	OAuthConnections []OAuthConnection `json:"oauth_connections,omitempty"`
}

// OAuthConnection links a user to an external OAuth / OIDC provider.
type OAuthConnection struct {
	UserID     string    `json:"user_id"`
	Provider   string    `json:"provider"`
	ProviderID string    `json:"provider_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// ProjectMember links a User to a Project with an assigned role.
type ProjectMember struct {
	UserID         string    `json:"user_id"`
	ProjectID      string    `json:"project_id"`
	Role           string    `json:"role"`
	CreditQuotaPct *float64  `json:"credit_quota_percent"` // nil = no limit (effective 100%)
	CreatedAt      time.Time `json:"created_at"`

	// User is populated when the record is fetched with a join.
	User *User `json:"user,omitempty"`
}
