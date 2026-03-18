package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const bedrockDefaultVersion = "bedrock-2023-05-31"

// bedrockSupportedBetas is the set of anthropic_beta flags that Bedrock
// recognises.  Flags not in this set cause a 400 "invalid beta flag" error.
// Source: https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages-request-response.html
var bedrockSupportedBetas = map[string]bool{
	"computer-use-2025-01-24":          true,
	"token-efficient-tools-2025-02-19": true,
	"interleaved-thinking-2025-05-14":  true,
	"output-128k-2025-02-19":           true,
	"dev-full-thinking-2025-05-14":     true,
	"context-1m-2025-08-07":            true,
	"context-management-2025-06-27":    true,
	"effort-2025-11-24":                true,
	"tool-search-tool-2025-10-19":      true,
	"tool-examples-2025-10-29":         true,
}

// splitBetaHeaders splits potentially comma-separated anthropic-beta header
// values into individual beta feature strings.
func splitBetaHeaders(headerValues []string) []string {
	var betas []string
	for _, v := range headerValues {
		for _, b := range strings.Split(v, ",") {
			if b = strings.TrimSpace(b); b != "" {
				betas = append(betas, b)
			}
		}
	}
	return betas
}

// filterBedrockBetas returns only the beta flags that Bedrock supports,
// plus any that are not in the whitelist (logged as dropped).
func filterBedrockBetas(betas []string) (supported, dropped []string) {
	for _, b := range betas {
		if bedrockSupportedBetas[b] {
			supported = append(supported, b)
		} else {
			dropped = append(dropped, b)
		}
	}
	return
}

// transformBedrockBody modifies the request body for Bedrock format:
//   - Sets anthropic_version to "bedrock-2023-05-31" if not present
//   - Moves anthropic-beta header values into body as anthropic_beta array
//   - Removes model and stream fields
func transformBedrockBody(body []byte, betas []string) ([]byte, error) {
	var err error

	if !gjson.GetBytes(body, "anthropic_version").Exists() {
		body, err = sjson.SetBytes(body, "anthropic_version", bedrockDefaultVersion)
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
	body, _ = sjson.DeleteBytes(body, "stream")

	return body, nil
}

// bedrockURLPath returns the Bedrock invoke URL path for the given model.
func bedrockURLPath(model string, streaming bool) string {
	method := "invoke"
	if streaming {
		method = "invoke-with-response-stream"
	}
	return fmt.Sprintf("/model/%s/%s", model, method)
}

// directorSetBedrockUpstream configures the reverse-proxy request for a Bedrock upstream.
func directorSetBedrockUpstream(req *http.Request, baseURL, apiKey string, model string, streaming bool) {
	req.URL.Scheme = "https"
	var basePath string
	if baseURL != "" {
		if target, err := url.Parse(baseURL); err == nil {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			basePath = target.Path
		}
	}
	req.Host = req.URL.Host

	bPath := bedrockURLPath(model, streaming)
	if basePath != "" && basePath != "/" {
		req.URL.Path = path.Join(basePath, bPath)
	} else {
		req.URL.Path = bPath
	}
	rawSuffix := fmt.Sprintf("/model/%s/%s", url.PathEscape(model), func() string {
		if streaming {
			return "invoke-with-response-stream"
		}
		return "invoke"
	}())
	if basePath != "" && basePath != "/" {
		req.URL.RawPath = path.Join(basePath, rawSuffix)
	} else {
		req.URL.RawPath = rawSuffix
	}

	// Set all required headers from scratch — do not inherit from client.
	req.Header.Set("Authorization", "Bearer "+apiKey)
}
