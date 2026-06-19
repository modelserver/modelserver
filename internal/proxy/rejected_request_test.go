package proxy

import (
	"net/http/httptest"
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
