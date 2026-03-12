package auth

// OAuthUserInfo holds user information returned by an OAuth provider.
type OAuthUserInfo struct {
	Email      string
	Name       string
	Picture    string
	ProviderID string
	Provider   string
}
