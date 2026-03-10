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
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	PasswordHash  string    `json:"-"`
	Name          string    `json:"name"`
	AvatarURL     string    `json:"avatar_url,omitempty"`
	OAuthProvider string    `json:"oauth_provider,omitempty"`
	OAuthID       string    `json:"-"`
	IsSuperadmin  bool      `json:"is_superadmin"`
	MaxProjects   int       `json:"max_projects"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ProjectMember links a User to a Project with an assigned role.
type ProjectMember struct {
	UserID    string    `json:"user_id"`
	ProjectID string    `json:"project_id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`

	// User is populated when the record is fetched with a join.
	User *User `json:"user,omitempty"`
}
