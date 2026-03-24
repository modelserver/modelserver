package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HydraClient wraps Ory Hydra's Admin API.
type HydraClient struct {
	adminURL   string
	httpClient *http.Client
}

// HydraLoginRequest represents the response from Hydra's GET login request endpoint.
type HydraLoginRequest struct {
	Challenge  string `json:"challenge"`
	Subject    string `json:"subject"`
	Skip       bool   `json:"skip"`
	Client     struct {
		ClientID   string `json:"client_id"`
		ClientName string `json:"client_name"`
	} `json:"client"`
	RequestURL string `json:"request_url"`
}

// HydraRedirect represents the redirect response returned after accepting a login or consent request.
type HydraRedirect struct {
	RedirectTo string `json:"redirect_to"`
}

// HydraConsentRequest represents the response from Hydra's GET consent request endpoint.
type HydraConsentRequest struct {
	Challenge      string   `json:"challenge"`
	Subject        string   `json:"subject"`
	RequestedScope []string `json:"requested_scope"`
	Client         struct {
		ClientID   string `json:"client_id"`
		ClientName string `json:"client_name"`
	} `json:"client"`
}

// IntrospectResult represents the response from Hydra's token introspection endpoint.
type IntrospectResult struct {
	Active   bool                   `json:"active"`
	Sub      string                 `json:"sub"`
	Scope    string                 `json:"scope"`
	Ext      map[string]interface{} `json:"ext"`
	ClientID string                 `json:"client_id"`
}

// NewHydraClient creates a new HydraClient targeting the given adminURL with a 10s HTTP timeout.
func NewHydraClient(adminURL string) *HydraClient {
	return &HydraClient{
		adminURL: strings.TrimRight(adminURL, "/"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// GetLoginRequest fetches login request details for the given challenge from Hydra.
func (c *HydraClient) GetLoginRequest(ctx context.Context, challenge string) (*HydraLoginRequest, error) {
	endpoint := fmt.Sprintf("%s/admin/oauth2/auth/requests/login?login_challenge=%s",
		c.adminURL, url.QueryEscape(challenge))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("hydra: build get login request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hydra: get login request: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("hydra: get login request: %w", err)
	}

	var result HydraLoginRequest
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("hydra: decode login request: %w", err)
	}
	return &result, nil
}

// AcceptLogin accepts the login request identified by challenge and sets the authenticated subject.
// The session is remembered for 86400 seconds (24 hours).
func (c *HydraClient) AcceptLogin(ctx context.Context, challenge, subject string) (*HydraRedirect, error) {
	endpoint := fmt.Sprintf("%s/admin/oauth2/auth/requests/login/accept?login_challenge=%s",
		c.adminURL, url.QueryEscape(challenge))

	body, err := json.Marshal(map[string]interface{}{
		"subject":      subject,
		"remember":     true,
		"remember_for": 86400,
	})
	if err != nil {
		return nil, fmt.Errorf("hydra: marshal accept login body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("hydra: build accept login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hydra: accept login: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("hydra: accept login: %w", err)
	}

	var result HydraRedirect
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("hydra: decode accept login response: %w", err)
	}
	return &result, nil
}

// GetConsentRequest fetches consent request details for the given challenge from Hydra.
func (c *HydraClient) GetConsentRequest(ctx context.Context, challenge string) (*HydraConsentRequest, error) {
	endpoint := fmt.Sprintf("%s/admin/oauth2/auth/requests/consent?consent_challenge=%s",
		c.adminURL, url.QueryEscape(challenge))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("hydra: build get consent request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hydra: get consent request: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("hydra: get consent request: %w", err)
	}

	var result HydraConsentRequest
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("hydra: decode consent request: %w", err)
	}
	return &result, nil
}

// AcceptConsent accepts the consent request identified by challenge, granting the provided scopes.
// sessionData is embedded in the access token's extension claims.
// The consent is not remembered across sessions.
func (c *HydraClient) AcceptConsent(ctx context.Context, challenge string, grantScope []string, sessionData map[string]interface{}) (*HydraRedirect, error) {
	endpoint := fmt.Sprintf("%s/admin/oauth2/auth/requests/consent/accept?consent_challenge=%s",
		c.adminURL, url.QueryEscape(challenge))

	body, err := json.Marshal(map[string]interface{}{
		"grant_scope": grantScope,
		"remember":    false,
		"session": map[string]interface{}{
			"access_token": sessionData,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("hydra: marshal accept consent body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("hydra: build accept consent request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hydra: accept consent: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("hydra: accept consent: %w", err)
	}

	var result HydraRedirect
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("hydra: decode accept consent response: %w", err)
	}
	return &result, nil
}

// IntrospectToken calls Hydra's token introspection endpoint using form-encoded body.
func (c *HydraClient) IntrospectToken(ctx context.Context, token string) (*IntrospectResult, error) {
	endpoint := fmt.Sprintf("%s/admin/oauth2/introspect", c.adminURL)

	formBody := url.Values{"token": {token}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(formBody))
	if err != nil {
		return nil, fmt.Errorf("hydra: build introspect request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hydra: introspect token: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return nil, fmt.Errorf("hydra: introspect token: %w", err)
	}

	var result IntrospectResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("hydra: decode introspect response: %w", err)
	}
	return &result, nil
}

// RevokeConsent revokes all consent sessions for the given subject and client in Hydra.
func (c *HydraClient) RevokeConsent(ctx context.Context, subject, clientID string) error {
	endpoint := fmt.Sprintf("%s/admin/oauth2/auth/sessions/consent?subject=%s&client=%s",
		c.adminURL, url.QueryEscape(subject), url.QueryEscape(clientID))

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("hydra: build revoke consent request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("hydra: revoke consent: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return fmt.Errorf("hydra: revoke consent: %w", err)
	}
	return nil
}

// checkStatus returns an error if the HTTP response status is not 2xx.
// It reads and includes the response body in the error message for context.
func checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
