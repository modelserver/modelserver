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

// vertexUnsupportedBetaPrefixes lists beta flag prefixes that Vertex AI does
// not recognise. Flags matching any prefix are dropped to avoid 400 errors.
//   - prompt-caching-*: Vertex handles caching via cache_control in the body.
//   - context-management-*: Vertex does not support this beta flag.
var vertexUnsupportedBetaPrefixes = []string{
	"prompt-caching-",
	"context-management-",
}

// filterVertexBetas drops beta flags that Vertex AI does not support.
// Unlike Bedrock's allowlist approach, Vertex uses a denylist: only flags
// matching vertexUnsupportedBetaPrefixes are dropped; everything else passes through.
func filterVertexBetas(betas []string) (supported, dropped []string) {
	for _, b := range betas {
		drop := false
		for _, prefix := range vertexUnsupportedBetaPrefixes {
			if strings.HasPrefix(b, prefix) {
				drop = true
				break
			}
		}
		if drop {
			dropped = append(dropped, b)
		} else {
			supported = append(supported, b)
		}
	}
	return
}

// vertexUnsupportedBodyFields lists top-level request body fields that Vertex AI
// does not accept. These are stripped before forwarding to avoid 400 errors.
var vertexUnsupportedBodyFields = []string{
	"context_management",
}

// transformVertexBody modifies the request body for Vertex AI format:
//   - Sets anthropic_version to "vertex-2023-10-16" if not present
//   - Moves anthropic-beta header values into body as anthropic_beta array
//   - Removes model field (encoded in the URL)
//   - Strips body fields that Vertex AI does not support
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
