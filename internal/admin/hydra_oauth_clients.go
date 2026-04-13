package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// HydraOAuthClient represents an OAuth2 client in Hydra.
type HydraOAuthClient struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	CreatedAt               string   `json:"created_at,omitempty"`
	UpdatedAt               string   `json:"updated_at,omitempty"`
}

// ListOAuthClients returns all OAuth2 clients from Hydra.
func (c *HydraClient) ListOAuthClients(ctx context.Context) ([]HydraOAuthClient, error) {
	endpoint := fmt.Sprintf("%s/admin/clients", c.adminURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("hydra: build list clients request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hydra: list clients: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("hydra: list clients: %w", err)
	}

	var clients []HydraOAuthClient
	if err := json.NewDecoder(resp.Body).Decode(&clients); err != nil {
		return nil, fmt.Errorf("hydra: decode clients list: %w", err)
	}
	return clients, nil
}

// GetOAuthClient returns a single OAuth2 client from Hydra.
func (c *HydraClient) GetOAuthClient(ctx context.Context, clientID string) (*HydraOAuthClient, error) {
	endpoint := fmt.Sprintf("%s/admin/clients/%s", c.adminURL, clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("hydra: build get client request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hydra: get client: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("hydra: get client: %w", err)
	}

	var client HydraOAuthClient
	if err := json.NewDecoder(resp.Body).Decode(&client); err != nil {
		return nil, fmt.Errorf("hydra: decode client: %w", err)
	}
	return &client, nil
}

// CreateOAuthClient creates a new OAuth2 client in Hydra.
func (c *HydraClient) CreateOAuthClient(ctx context.Context, client *HydraOAuthClient) (*HydraOAuthClient, error) {
	endpoint := fmt.Sprintf("%s/admin/clients", c.adminURL)

	body, err := json.Marshal(client)
	if err != nil {
		return nil, fmt.Errorf("hydra: marshal create client body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("hydra: build create client request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hydra: create client: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("hydra: create client: %w", err)
	}

	var created HydraOAuthClient
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, fmt.Errorf("hydra: decode created client: %w", err)
	}
	return &created, nil
}

// UpdateOAuthClient updates an existing OAuth2 client in Hydra.
func (c *HydraClient) UpdateOAuthClient(ctx context.Context, clientID string, client *HydraOAuthClient) (*HydraOAuthClient, error) {
	endpoint := fmt.Sprintf("%s/admin/clients/%s", c.adminURL, clientID)

	body, err := json.Marshal(client)
	if err != nil {
		return nil, fmt.Errorf("hydra: marshal update client body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("hydra: build update client request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hydra: update client: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("hydra: update client: %w", err)
	}

	var updated HydraOAuthClient
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		return nil, fmt.Errorf("hydra: decode updated client: %w", err)
	}
	return &updated, nil
}

// DeleteOAuthClient deletes an OAuth2 client from Hydra.
func (c *HydraClient) DeleteOAuthClient(ctx context.Context, clientID string) error {
	endpoint := fmt.Sprintf("%s/admin/clients/%s", c.adminURL, clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("hydra: build delete client request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("hydra: delete client: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return fmt.Errorf("hydra: delete client: %w", err)
	}
	return nil
}
