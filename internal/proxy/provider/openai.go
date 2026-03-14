package provider

import (
	"net/http"
	"net/url"
	"path"
)

// OpenAI implements the Provider interface for the OpenAI API.
type OpenAI struct{}

func (o *OpenAI) Name() string { return "openai" }

func (o *OpenAI) Director(req *http.Request, baseURL, apiKey string) {
	target, err := url.Parse(baseURL)
	if err != nil {
		return
	}

	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.Host = target.Host
	if target.Path != "" && target.Path != "/" {
		req.URL.Path = path.Join(target.Path, req.URL.Path)
	}

	req.Header.Del("x-api-key")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")
	req.Header.Del("Accept-Encoding")

	req.Header.Set("Authorization", "Bearer "+apiKey)
}
