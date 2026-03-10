package auth

import (
	"context"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCProvider handles generic OIDC OAuth2 flow.
type OIDCProvider struct {
	oauthConfig *oauth2.Config
	verifier    *oidc.IDTokenVerifier
	providerName string
}

// NewOIDCProvider creates a generic OIDC OAuth provider.
func NewOIDCProvider(ctx context.Context, issuerURL, clientID, clientSecret, redirectURL string) (*OIDCProvider, error) {
	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}

	return &OIDCProvider{
		oauthConfig: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  redirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: clientID}),
		providerName: "oidc",
	}, nil
}

// ExchangeAndGetUser exchanges an auth code for user info.
func (o *OIDCProvider) ExchangeAndGetUser(ctx context.Context, code string) (*OAuthUserInfo, error) {
	token, err := o.oauthConfig.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("no id_token in response")
	}

	idToken, err := o.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}

	var claims struct {
		Sub     string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}

	return &OAuthUserInfo{
		Email:      claims.Email,
		Name:       claims.Name,
		AvatarURL:  claims.Picture,
		ProviderID: claims.Sub,
		Provider:   o.providerName,
	}, nil
}
