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

// vertexSupportedBetas is the set of anthropic_beta flags that Vertex AI
// recognises. Uses the same set as Bedrock since both host Claude models.
var vertexSupportedBetas = map[string]bool{
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

// filterVertexBetas returns only the beta flags that Vertex AI supports.
func filterVertexBetas(betas []string) (supported, dropped []string) {
	for _, b := range betas {
		if vertexSupportedBetas[b] {
			supported = append(supported, b)
		} else {
			dropped = append(dropped, b)
		}
	}
	return
}

// transformVertexBody modifies the request body for Vertex AI format:
//   - Sets anthropic_version to "vertex-2023-10-16" if not present
//   - Moves anthropic-beta header values into body as anthropic_beta array
//   - Removes model field (encoded in the URL)
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
