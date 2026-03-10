package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// GoogleOAuth handles Google OAuth2 flow.
type GoogleOAuth struct {
	config *oauth2.Config
}

// NewGoogleOAuth creates a Google OAuth provider.
func NewGoogleOAuth(clientID, clientSecret, redirectURL string) *GoogleOAuth {
	return &GoogleOAuth{
		config: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     google.Endpoint,
			RedirectURL:  redirectURL,
			Scopes:       []string{"openid", "email", "profile"},
		},
	}
}

// ExchangeAndGetUser exchanges an auth code for user info.
func (g *GoogleOAuth) ExchangeAndGetUser(ctx context.Context, code string) (*OAuthUserInfo, error) {
	token, err := g.config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}

	client := g.config.Client(ctx, token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		return nil, fmt.Errorf("fetch user info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google userinfo API returned %d", resp.StatusCode)
	}

	var profile struct {
		ID      string `json:"id"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("decode user info: %w", err)
	}

	return &OAuthUserInfo{
		Email:      profile.Email,
		Name:       profile.Name,
		AvatarURL:  profile.Picture,
		ProviderID: profile.ID,
		Provider:   "google",
	}, nil
}
