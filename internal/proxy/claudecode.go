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

	// Set all required headers from scratch — do not inherit from client.
	req.Header.Del("x-api-key")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Connection", "keep-alive")
	// Merge required Claude Code beta flags with any client-provided betas.
	mergeClaudeCodeBetaHeaders(req)
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
