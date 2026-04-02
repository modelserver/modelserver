package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// geminiContextKey is used to pass Gemini-specific parameters through the
// request context from the Executor to SetUpstream.
type geminiContextKey struct{}

// geminiParams holds the resolved model and streaming flag that SetUpstream
// needs to construct the Gemini API endpoint URL.
type geminiParams struct {
	Model    string
	IsStream bool
}

// withGeminiParams returns a new request with Gemini routing parameters
// stored in its context.
func withGeminiParams(r *http.Request, model string, isStream bool) *http.Request {
	ctx := context.WithValue(r.Context(), geminiContextKey{}, geminiParams{
		Model:    model,
		IsStream: isStream,
	})
	return r.WithContext(ctx)
}

// geminiEndpointURL constructs the full Gemini API endpoint URL.
// Format: {baseURL}/models/{model}:generateContent or :streamGenerateContent?alt=sse
func geminiEndpointURL(baseURL, model string, streaming bool) string {
	base := strings.TrimRight(baseURL, "/")
	method := "generateContent"
	if streaming {
		method = "streamGenerateContent"
	}
	endpoint := fmt.Sprintf("%s/models/%s:%s", base, model, method)
	if streaming {
		endpoint += "?alt=sse"
	}
	return endpoint
}

// directorSetGeminiUpstream configures the outbound request for a Gemini API upstream.
func directorSetGeminiUpstream(req *http.Request, baseURL, apiKey, model string, streaming bool) {
	endpoint := geminiEndpointURL(baseURL, model, streaming)
	target, err := url.Parse(endpoint)
	if err != nil {
		// Fallback: just set the API key header.
		req.Header.Set("x-goog-api-key", apiKey)
		return
	}

	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = target.Path
	req.URL.RawPath = target.RawPath
	req.URL.RawQuery = target.RawQuery
	req.Host = target.Host

	req.Header.Set("x-goog-api-key", apiKey)

	// Remove Anthropic-specific headers that Gemini does not use.
	req.Header.Del("x-api-key")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")
}
