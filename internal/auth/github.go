package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

// GitHubOAuth handles GitHub OAuth2 flow.
type GitHubOAuth struct {
	config *oauth2.Config
}

// NewGitHubOAuth creates a GitHub OAuth provider.
func NewGitHubOAuth(clientID, clientSecret, redirectURL string) *GitHubOAuth {
	return &GitHubOAuth{
		config: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     github.Endpoint,
			RedirectURL:  redirectURL,
			Scopes:       []string{"user:email"},
		},
	}
}

// AuthCodeURL returns the authorization URL the user should visit.
func (g *GitHubOAuth) AuthCodeURL(state, redirectURL string) string {
	return g.config.AuthCodeURL(state, oauth2.SetAuthURLParam("redirect_uri", redirectURL))
}

// ExchangeAndGetUser exchanges an auth code for user info.
func (g *GitHubOAuth) ExchangeAndGetUser(ctx context.Context, code string) (*OAuthUserInfo, error) {
	token, err := g.config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}

	client := g.config.Client(ctx, token)

	// Fetch user profile.
	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		return nil, fmt.Errorf("fetch user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github user API returned %d", resp.StatusCode)
	}

	var profile struct {
		ID        int    `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return nil, fmt.Errorf("decode user: %w", err)
	}

	// If email is private, fetch from /user/emails.
	email := profile.Email
	if email == "" {
		email, _ = g.fetchPrimaryEmail(ctx, client)
	}

	return &OAuthUserInfo{
		Email:      email,
		Name:       coalesce(profile.Name, profile.Login),
		Picture:    profile.AvatarURL,
		ProviderID: fmt.Sprintf("%d", profile.ID),
		Provider:   "github",
	}, nil
}

func (g *GitHubOAuth) fetchPrimaryEmail(ctx context.Context, client *http.Client) (string, error) {
	resp, err := client.Get("https://api.github.com/user/emails")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return "", err
	}

	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email, nil
		}
	}
	for _, e := range emails {
		if e.Verified {
			return e.Email, nil
		}
	}
	return "", fmt.Errorf("no verified email found")
}

func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
