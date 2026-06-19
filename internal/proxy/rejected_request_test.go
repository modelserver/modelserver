package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestRequestKindFromRequest_AllRoutes(t *testing.T) {
	cases := []struct {
		method, path string
		want         string
	}{
		// Proxy POST surface mounted in router.go.
		{"POST", "/v1/messages", types.KindAnthropicMessages},
		{"POST", "/v1/messages/count_tokens", types.KindAnthropicCountTokens},
		{"POST", "/v1/responses", types.KindOpenAIResponses},
		{"POST", "/v1/responses/compact", types.KindOpenAIResponsesCompact},
		{"POST", "/v1/chat/completions", types.KindOpenAIChatCompletions},
		{"POST", "/v1/images/generations", types.KindOpenAIImagesGenerations},
		{"POST", "/v1/images/edits", types.KindOpenAIImagesEdits},
		{"POST", "/v1beta/models/gemini-2.5-flash:generateContent", types.KindGoogleGenerateContent},
		{"POST", "/v1beta/models/gemini-2.5-flash:streamGenerateContent", types.KindGoogleGenerateContent},
		{"POST", "/v1beta/models/foo:streamRawPredict", types.KindGoogleGenerateContent},

		// Non-proxy surface — should not be classified.
		{"GET", "/v1/messages", ""},
		{"GET", "/v1/models", ""},
		{"GET", "/v1/usage", ""},
		{"POST", "/admin/upstreams", ""},
		{"POST", "/healthz", ""},
		{"POST", "/v1beta/other/path", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := requestKindFromRequest(r); got != c.want {
			t.Errorf("%s %s → %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

func TestPeekStreaming(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		want                     bool
	}{
		// Path-based streaming (Gemini native).
		{"gemini stream", "POST", "/v1beta/models/gemini-2.5-flash:streamGenerateContent", "", true},
		{"gemini stream raw predict", "POST", "/v1beta/models/foo:streamRawPredict", "", true},
		{"gemini unary", "POST", "/v1beta/models/gemini-2.5-flash:generateContent", "", false},

		// Body-based streaming (Anthropic / OpenAI).
		{"anthropic stream true", "POST", "/v1/messages", `{"model":"x","stream":true}`, true},
		{"anthropic stream false", "POST", "/v1/messages", `{"model":"x","stream":false}`, false},
		{"anthropic stream absent", "POST", "/v1/messages", `{"model":"x"}`, false},
		{"openai stream true", "POST", "/v1/chat/completions", `{"stream":true}`, true},
		{"openai responses stream true", "POST", "/v1/responses", `{"stream":true}`, true},

		// Non-streaming surfaces — always false.
		{"count_tokens with stream true ignored", "POST", "/v1/messages/count_tokens", `{"stream":true}`, false},
		{"images generations", "POST", "/v1/images/generations", `{"stream":true}`, false},
		{"images edits", "POST", "/v1/images/edits", `{"stream":true}`, false},

		// Malformed inputs.
		{"malformed body", "POST", "/v1/messages", `{not json`, false},
		{"empty body", "POST", "/v1/messages", "", false},
		{"unknown path", "POST", "/v1/whatever", `{"stream":true}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r *http.Request
			if c.body == "" {
				r = httptest.NewRequest(c.method, c.path, nil)
			} else {
				r = httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			}
			got := peekStreaming(r)
			if got != c.want {
				t.Errorf("peekStreaming(%s %s, body=%q)=%v, want %v", c.method, c.path, c.body, got, c.want)
			}
			// Body must remain readable downstream.
			if c.body != "" && r.Body != nil {
				rest, _ := io.ReadAll(r.Body)
				if string(rest) != c.body {
					t.Errorf("body not restored: got %q, want %q", string(rest), c.body)
				}
			}
		})
	}
}
