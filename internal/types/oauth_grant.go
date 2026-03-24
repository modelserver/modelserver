package types

import "time"

// OAuthGrant represents a recorded OAuth consent grant linking a user, project, and OAuth client.
type OAuthGrant struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	UserID       string    `json:"user_id"`
	UserNickname string    `json:"user_nickname,omitempty"`
	UserPicture  string    `json:"user_picture,omitempty"`
	ClientID     string    `json:"client_id"`
	Scopes       []string  `json:"scopes"`
	CreatedAt    time.Time `json:"created_at"`
}
