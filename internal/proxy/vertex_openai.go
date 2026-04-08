package proxy

import (
	"net/http"
	"net/url"
	"strings"
)

// directorSetVertexOpenAIUpstream configures the outbound request for a Vertex AI
// OpenAI-compatible upstream. The base URL should point to the Vertex AI openapi
// endpoint (e.g. https://REGION-aiplatform.googleapis.com/v1/projects/P/locations/L/endpoints/openapi).
// The /chat/completions path is appended automatically.
func directorSetVertexOpenAIUpstream(req *http.Request, baseURL, accessToken string) {
	endpoint := strings.TrimRight(baseURL, "/") + "/chat/completions"
	target, err := url.Parse(endpoint)
	if err != nil {
		req.Header.Set("Authorization", "Bearer "+accessToken)
		return
	}

	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	req.URL.Path = target.Path
	req.URL.RawPath = target.RawPath
	req.Host = target.Host

	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Remove headers that Vertex AI does not use.
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")
}
