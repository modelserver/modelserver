package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const vertexDefaultVersion = "vertex-2023-10-16"

// vertexSupportedBetas maps incoming anthropic-beta header values to their
// Vertex AI equivalents. Only headers present in this map are forwarded from
// client requests; all others are silently dropped. A different value means
// the header is renamed for Vertex AI.
//
// Aligned with litellm's anthropic_beta_headers_config.json (vertex_ai section).
var vertexSupportedBetas = map[string]string{
	"advanced-tool-use-2025-11-20":    "tool-search-tool-2025-10-19",
	"computer-use-2025-01-24":         "computer-use-2025-01-24",
	"computer-use-2025-11-24":         "computer-use-2025-11-24",
	"context-1m-2025-08-07":           "context-1m-2025-08-07",
	"context-management-2025-06-27":   "context-management-2025-06-27",
	"interleaved-thinking-2025-05-14": "interleaved-thinking-2025-05-14",
	"tool-search-tool-2025-10-19":     "tool-search-tool-2025-10-19",
	"web-search-2025-03-05":           "web-search-2025-03-05",
}

// filterVertexBetas filters incoming beta flags through the Vertex AI allowlist,
// remapping where necessary. It also infers additional required betas from the
// request body (e.g. compact, context-management, web-search) following the same
// logic as litellm's transformation.py.
func filterVertexBetas(betas []string, body []byte) (supported, dropped []string) {
	seen := make(map[string]bool)

	// 1. Filter through allowlist with optional renaming.
	for _, b := range betas {
		if mapped, ok := vertexSupportedBetas[b]; ok {
			if !seen[mapped] {
				supported = append(supported, mapped)
				seen[mapped] = true
			}
		} else {
			dropped = append(dropped, b)
		}
	}

	// 2. Infer additional betas from request body content.
	for _, b := range inferVertexBetasFromBody(body) {
		if !seen[b] {
			supported = append(supported, b)
			seen[b] = true
		}
	}

	return
}

// inferVertexBetasFromBody inspects the request body for features that require
// specific beta flags and returns them. This ensures the correct beta flags are
// present even if the client didn't include them in the header.
//
// Aligned with litellm's VertexAIPartnerModelsAnthropicMessagesConfig.
func inferVertexBetasFromBody(body []byte) []string {
	var betas []string

	// Check context_management.edits for compact vs other edit types.
	edits := gjson.GetBytes(body, "context_management.edits")
	if edits.IsArray() {
		hasCompact := false
		hasOther := false
		for _, edit := range edits.Array() {
			if edit.Get("type").String() == "compact_20260112" {
				hasCompact = true
			} else {
				hasOther = true
			}
		}
		if hasCompact {
			betas = append(betas, "compact-2026-01-12")
		}
		if hasOther {
			betas = append(betas, "context-management-2025-06-27")
		}
	}

	// Check for web search tool.
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if strings.HasPrefix(tool.Get("type").String(), "web_search") {
			betas = append(betas, "web-search-2025-03-05")
			break
		}
	}

	// Check for tool search tool.
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("type").String() == "tool_search" {
			betas = append(betas, "tool-search-tool-2025-10-19")
			break
		}
	}

	return betas
}

// vertexUnsupportedBodyFields lists top-level request body fields that Vertex AI
// does not accept. These are stripped before forwarding to avoid 400 errors.
var vertexUnsupportedBodyFields = []string{
	"output_format",
	"output_config",
}

// stripCacheControlScope removes the "scope" field from cache_control objects
// throughout the request body. Vertex AI does not support the scope parameter
// in cache_control (used by the Anthropic API for automatic prompt caching).
// Caching on Vertex works via cache_control.type alone.
func stripCacheControlScope(body []byte) []byte {
	// Strip from messages[].content[].cache_control.scope
	for i, msg := range gjson.GetBytes(body, "messages").Array() {
		if msg.Get("content").IsArray() {
			for j, block := range msg.Get("content").Array() {
				if block.Get("cache_control.scope").Exists() {
					body, _ = sjson.DeleteBytes(body, fmt.Sprintf("messages.%d.content.%d.cache_control.scope", i, j))
				}
			}
		}
	}
	// Strip from system[].cache_control.scope (when system is an array of content blocks)
	for i, block := range gjson.GetBytes(body, "system").Array() {
		if block.Get("cache_control.scope").Exists() {
			body, _ = sjson.DeleteBytes(body, fmt.Sprintf("system.%d.cache_control.scope", i))
		}
	}
	// Strip from tools[].cache_control.scope
	for i, tool := range gjson.GetBytes(body, "tools").Array() {
		if tool.Get("cache_control.scope").Exists() {
			body, _ = sjson.DeleteBytes(body, fmt.Sprintf("tools.%d.cache_control.scope", i))
		}
	}
	return body
}

// transformVertexBody modifies the request body for Vertex AI format:
//   - Sets anthropic_version to "vertex-2023-10-16" if not present
//   - Moves anthropic-beta header values into body as anthropic_beta array
//   - Removes model field (encoded in the URL)
//   - Strips body fields that Vertex AI does not support
//   - Removes scope from cache_control objects (Vertex uses cache_control.type only)
//
// NOTE: Unlike Bedrock, the stream field is NOT removed. Vertex AI requires
// "stream": true in the request body in addition to using the streamRawPredict
// endpoint. Without it, Vertex returns a non-streaming JSON response.
func transformVertexBody(body []byte, betas []string) ([]byte, error) {
	var err error

	if !gjson.GetBytes(body, "anthropic_version").Exists() {
		body, err = sjson.SetBytes(body, "anthropic_version", vertexDefaultVersion)
		if err != nil {
			return nil, fmt.Errorf("setting anthropic_version: %w", err)
		}
	}

	if len(betas) > 0 {
		body, err = sjson.SetBytes(body, "anthropic_beta", betas)
		if err != nil {
			return nil, fmt.Errorf("setting anthropic_beta: %w", err)
		}
	}

	body, _ = sjson.DeleteBytes(body, "model")

	for _, field := range vertexUnsupportedBodyFields {
		body, _ = sjson.DeleteBytes(body, field)
	}

	body = stripCacheControlScope(body)

	return body, nil
}

// vertexEndpointURL constructs the full Vertex AI endpoint URL.
// Format: {baseURL}/{model}:rawPredict or {baseURL}/{model}:streamRawPredict
func vertexEndpointURL(baseURL, model string, streaming bool) string {
	base := strings.TrimRight(baseURL, "/")
	method := "rawPredict"
	if streaming {
		method = "streamRawPredict"
	}
	return fmt.Sprintf("%s/%s:%s", base, model, method)
}

// directorSetVertexUpstream configures the outbound request for a Vertex AI upstream.
func directorSetVertexUpstream(req *http.Request, baseURL, accessToken, model string, streaming bool) {
	endpoint := vertexEndpointURL(baseURL, model, streaming)
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

	// Remove client headers that Vertex AI does not use.
	req.Header.Del("x-api-key")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")
}
