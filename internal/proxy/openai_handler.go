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

	// Set all required headers from scratch — do not inherit from client.
	req.Header.Set("Authorization", "Bearer "+apiKey)
}
