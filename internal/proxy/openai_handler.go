package proxy

import (
	"net/http"
	"net/url"
	"path"
)

func directorSetOpenAIUpstream(req *http.Request, baseURL, apiKey string) {
	req.URL.Scheme = "https"
	if baseURL != "" {
		if target, err := url.Parse(baseURL); err == nil {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			if target.Path != "" && target.Path != "/" {
				req.URL.Path = path.Join(target.Path, req.URL.Path)
			}
		}
	}
	req.Host = req.URL.Host

	// Remove headers that should not be forwarded to the upstream provider.
	req.Header.Del("x-api-key")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")

	// Suppress X-Forwarded-For so the client's IP is never forwarded to
	// the upstream provider, preventing geo-restriction errors.
	req.Header["X-Forwarded-For"] = nil

	// Remove Accept-Encoding so that Go's http.Transport controls compression.
	// When a client sends Accept-Encoding (e.g. gzip), the Transport forwards it
	// to the upstream but does NOT auto-decompress the response — leaving the
	// streamInterceptor unable to parse the compressed SSE bytes. By deleting
	// the header, the Transport adds its own Accept-Encoding, auto-decompresses,
	// and the interceptor always sees plain-text SSE data.
	req.Header.Del("Accept-Encoding")

	// Set the channel's own API key for the upstream request using Bearer auth.
	req.Header.Set("Authorization", "Bearer "+apiKey)
}
