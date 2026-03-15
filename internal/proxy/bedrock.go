package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"path"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const bedrockDefaultVersion = "bedrock-2023-05-31"

// transformBedrockBody modifies the request body for Bedrock format:
//   - Sets anthropic_version to "bedrock-2023-05-31" if not present
//   - Moves anthropic-beta header values into body as anthropic_beta array
//   - Removes model and stream fields
func transformBedrockBody(body []byte, betaHeaderValues []string) ([]byte, error) {
	var err error

	if !gjson.GetBytes(body, "anthropic_version").Exists() {
		body, err = sjson.SetBytes(body, "anthropic_version", bedrockDefaultVersion)
		if err != nil {
			return nil, fmt.Errorf("setting anthropic_version: %w", err)
		}
	}

	if len(betaHeaderValues) > 0 {
		body, err = sjson.SetBytes(body, "anthropic_beta", betaHeaderValues)
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
	rawSuffix := fmt.Sprintf("/model/%s/%s", url.QueryEscape(model), func() string {
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

	// Remove Anthropic-specific headers that Bedrock does not use.
	req.Header.Del("x-api-key")
	req.Header.Del("Authorization")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")
	req.Header.Del("Accept-Encoding") // let Transport handle compression

	// Suppress X-Forwarded-For so the client's IP is never forwarded to
	// the upstream. httputil.ReverseProxy skips appending X-Forwarded-For
	// when the header key is already present (even if nil), preventing
	// geo-restriction errors from gateways that inspect client IPs.
	req.Header["X-Forwarded-For"] = nil

	// Set Bearer token auth for Bedrock.
	req.Header.Set("Authorization", "Bearer "+apiKey)
}
