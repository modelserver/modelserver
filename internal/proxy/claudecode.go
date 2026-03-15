package proxy

import (
	"net/http"
	"net/url"
	"path"
)

// directorSetClaudeCodeUpstream configures the reverse-proxy request for a
// Claude Code (OAuth subscription) upstream. The request/response format is
// standard Anthropic — only the auth mechanism and headers differ.
func directorSetClaudeCodeUpstream(req *http.Request, baseURL, accessToken string) {
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

	// Append ?beta=true query parameter.
	q := req.URL.Query()
	q.Set("beta", "true")
	req.URL.RawQuery = q.Encode()

	// Remove user credentials so they are never forwarded upstream.
	req.Header.Del("x-api-key")
	req.Header.Del("Authorization")
	req.Header.Del("Accept-Encoding")

	// Suppress X-Forwarded-For.
	req.Header["X-Forwarded-For"] = nil

	// Set OAuth Bearer auth.
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Claude Code specific headers.
	req.Header.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
	req.Header.Set("X-App", "cli")
	req.Header.Set("User-Agent", "claude-cli/1.0.83 (external, cli)")
	req.Header.Set("X-Stainless-Lang", "js")
	req.Header.Set("X-Stainless-Package-Version", "0.52.0")
	req.Header.Set("X-Stainless-OS", "Linux")
	req.Header.Set("X-Stainless-Runtime", "node")
	req.Header.Set("X-Stainless-Runtime-Version", "v22.13.1")
	req.Header.Set("X-Stainless-Arch", "x64")
	req.Header.Set("Connection", "keep-alive")
}
