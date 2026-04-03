package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// vertexGoogleContextKey is used to pass Vertex Google-specific parameters through
// the request context from the Executor to SetUpstream.
type vertexGoogleContextKey struct{}

// vertexGoogleParams holds the resolved model and streaming flag that SetUpstream
// needs to construct the Vertex AI Gemini endpoint URL.
type vertexGoogleParams struct {
	Model    string
	IsStream bool
}

// withVertexGoogleParams returns a new request with Vertex Google routing parameters
// stored in its context.
func withVertexGoogleParams(r *http.Request, model string, isStream bool) *http.Request {
	ctx := context.WithValue(r.Context(), vertexGoogleContextKey{}, vertexGoogleParams{
		Model:    model,
		IsStream: isStream,
	})
	return r.WithContext(ctx)
}

// vertexGoogleEndpointURL constructs the full Vertex AI Gemini endpoint URL.
// Format: {baseURL}/{model}:generateContent or {baseURL}/{model}:streamGenerateContent?alt=sse
func vertexGoogleEndpointURL(baseURL, model string, streaming bool) string {
	base := strings.TrimRight(baseURL, "/")
	method := "generateContent"
	if streaming {
		method = "streamGenerateContent"
	}
	endpoint := fmt.Sprintf("%s/%s:%s", base, model, method)
	if streaming {
		endpoint += "?alt=sse"
	}
	return endpoint
}

// directorSetVertexGoogleUpstream configures the outbound request for a Vertex AI
// Gemini upstream. Uses Bearer token auth (same as Vertex Anthropic) but targets
// the generateContent endpoint instead of rawPredict.
func directorSetVertexGoogleUpstream(req *http.Request, baseURL, accessToken, model string, streaming bool) {
	endpoint := vertexGoogleEndpointURL(baseURL, model, streaming)
	target, err := url.Parse(endpoint)
	if err != nil {
		req.Header.Set("Authorization", "Bearer "+accessToken)
		return
	}

	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = target.Path
	req.URL.RawPath = target.RawPath
	req.URL.RawQuery = target.RawQuery
	req.Host = target.Host

	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Remove client headers that Vertex AI Gemini does not use.
	req.Header.Del("x-api-key")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")
}
