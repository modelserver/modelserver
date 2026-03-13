package proxy

import (
	"fmt"
	"net/http"
	"net/url"

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
	if baseURL != "" {
		req.URL.Host = stripScheme(baseURL)
		if hasScheme(baseURL, "http") {
			req.URL.Scheme = "http"
		}
	}
	req.Host = req.URL.Host

	path := bedrockURLPath(model, streaming)
	req.URL.Path = path
	req.URL.RawPath = fmt.Sprintf("/model/%s/%s", url.QueryEscape(model), func() string {
		if streaming {
			return "invoke-with-response-stream"
		}
		return "invoke"
	}())

	// Remove Anthropic-specific headers that Bedrock does not use.
	req.Header.Del("x-api-key")
	req.Header.Del("Authorization")
	req.Header.Del("anthropic-version")
	req.Header.Del("anthropic-beta")

	// Set Bearer token auth for Bedrock.
	req.Header.Set("Authorization", "Bearer "+apiKey)
}
