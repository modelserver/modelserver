package provider

import (
	"net/http"
	"net/url"
)

// Anthropic implements the Provider interface for the Anthropic API.
type Anthropic struct{}

func (a *Anthropic) Name() string { return "anthropic" }

func (a *Anthropic) Director(req *http.Request, baseURL, apiKey string) {
	target, err := url.Parse(baseURL)
	if err != nil {
		return
	}

	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.Host = target.Host

	req.Header.Set("x-api-key", apiKey)

	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
}
