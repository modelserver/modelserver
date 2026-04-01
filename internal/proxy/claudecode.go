package proxy

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// claudeCodeRequiredBetas are beta flags that must always be present on
// Claude Code (OAuth subscription) upstream requests.
var claudeCodeRequiredBetas = []string{
	"claude-code-20250219",
	"oauth-2025-04-20",
}

// directorSetClaudeCodeUpstream configures the reverse-proxy request for a
// Claude Code (OAuth subscription) upstream. The request/response format is
// standard Anthropic — only the auth mechanism and headers differ.
//
// Client-provided User-Agent and X-Stainless-* headers are preserved so that
// the Anthropic backend sees accurate version/runtime metadata from the real
// Claude Code client. Defaults are only applied when the client omits them.
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

	// Append ?beta=true query parameter. The Anthropic SDK's beta.messages
	// endpoint always sends this (via anthropic.beta.messages.create()).
	q := req.URL.Query()
	q.Set("beta", "true")
	req.URL.RawQuery = q.Encode()

	// Authentication: replace API key with OAuth Bearer token.
	req.Header.Del("x-api-key")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Connection", "keep-alive")

	// Merge required Claude Code beta flags with any client-provided betas.
	mergeClaudeCodeBetaHeaders(req)

	// Anthropic-Version: preserve the client's value if provided.
	if req.Header.Get("Anthropic-Version") == "" {
		req.Header.Set("Anthropic-Version", "2023-06-01")
	}
	req.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")

	// X-App: always set (simple identifier the client also sends).
	req.Header.Set("X-App", "cli")

	// User-Agent and X-Stainless-* headers: pass through client values.
	// Only set defaults when the client doesn't provide them.
	setHeaderDefault(req, "User-Agent", "claude-cli/0.0.0 (external, cli)")
	setHeaderDefault(req, "X-Stainless-Lang", "js")
	setHeaderDefault(req, "X-Stainless-Package-Version", "0.0.0")
	setHeaderDefault(req, "X-Stainless-OS", "Linux")
	setHeaderDefault(req, "X-Stainless-Runtime", "bun")
	setHeaderDefault(req, "X-Stainless-Runtime-Version", "0.0.0")
	setHeaderDefault(req, "X-Stainless-Arch", "x64")
}

// setHeaderDefault sets a header only if the request does not already carry it.
func setHeaderDefault(req *http.Request, key, fallback string) {
	if req.Header.Get(key) == "" {
		req.Header.Set(key, fallback)
	}
}

// mergeClaudeCodeBetaHeaders ensures claudeCodeRequiredBetas are present in
// the Anthropic-Beta header while preserving any client-provided beta flags.
func mergeClaudeCodeBetaHeaders(req *http.Request) {
	clientBetas := splitBetaHeaders(req.Header.Values("Anthropic-Beta"))

	seen := make(map[string]struct{}, len(claudeCodeRequiredBetas)+len(clientBetas))
	merged := make([]string, 0, len(claudeCodeRequiredBetas)+len(clientBetas))

	// Required betas come first.
	for _, b := range claudeCodeRequiredBetas {
		if _, ok := seen[b]; !ok {
			seen[b] = struct{}{}
			merged = append(merged, b)
		}
	}
	// Then append client betas, skipping duplicates.
	for _, b := range clientBetas {
		if _, ok := seen[b]; !ok {
			seen[b] = struct{}{}
			merged = append(merged, b)
		}
	}

	req.Header.Set("Anthropic-Beta", strings.Join(merged, ","))
}
