package proxy

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// claudeCodeBaseBetas is the minimum set of beta flags that must always be
// present for Claude Code API requests, matching CLI v2.1.76's JHA set.
var claudeCodeBaseBetas = []string{
	"claude-code-20250219",
	"interleaved-thinking-2025-05-14",
	"context-management-2025-06-27",
}

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

	// Capture client-sent beta flags before removing credentials.
	clientBeta := req.Header.Get("Anthropic-Beta")

	// Remove user credentials so they are never forwarded upstream.
	req.Header.Del("x-api-key")
	req.Header.Del("Authorization")
	req.Header.Del("Accept-Encoding")

	// Suppress X-Forwarded-For.
	req.Header["X-Forwarded-For"] = nil

	// Set OAuth Bearer auth.
	req.Header.Set("Authorization", "Bearer "+accessToken)

	// Merge base betas with any additional client-sent betas so that new
	// CLI beta flags (e.g. context-1m) pass through without code changes.
	req.Header.Set("Anthropic-Beta", mergeClaudeCodeBetas(clientBeta))

	// Claude Code specific headers — aligned with CLI v2.1.76.
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
	req.Header.Set("X-App", "cli")
	req.Header.Set("User-Agent", "claude-cli/2.1.76 (external, cli)")
	req.Header.Set("X-Stainless-Lang", "js")
	req.Header.Set("X-Stainless-Package-Version", "0.74.0")
	req.Header.Set("X-Stainless-OS", "Linux")
	req.Header.Set("X-Stainless-Runtime", "bun")
	req.Header.Set("X-Stainless-Runtime-Version", "1.3.11")
	req.Header.Set("X-Stainless-Arch", "x64")
	req.Header.Set("Connection", "keep-alive")
}

// mergeClaudeCodeBetas merges the base beta set with additional flags from
// the client request. Base betas always come first; client-only betas are
// appended in their original order, deduplicated.
func mergeClaudeCodeBetas(clientBeta string) string {
	if clientBeta == "" {
		return strings.Join(claudeCodeBaseBetas, ",")
	}

	seen := make(map[string]bool, len(claudeCodeBaseBetas))
	for _, b := range claudeCodeBaseBetas {
		seen[b] = true
	}

	merged := make([]string, len(claudeCodeBaseBetas))
	copy(merged, claudeCodeBaseBetas)

	for _, b := range strings.Split(clientBeta, ",") {
		b = strings.TrimSpace(b)
		if b != "" && !seen[b] {
			merged = append(merged, b)
			seen[b] = true
		}
	}

	return strings.Join(merged, ",")
}
