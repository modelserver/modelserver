package proxy

import (
	"net/http"
	"testing"
)

func TestDirectorSetVertexOpenAIUpstream(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/chat/completions", nil)
	req.Header.Set("x-api-key", "user-key")
	req.Header.Set("anthropic-version", "2023-06-01")

	directorSetVertexOpenAIUpstream(req,
		"https://us-central1-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-central1/endpoints/openapi",
		"ya29.fake-token",
	)

	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %s, want https", req.URL.Scheme)
	}
	if req.URL.Host != "us-central1-aiplatform.googleapis.com" {
		t.Errorf("host = %s", req.URL.Host)
	}
	wantPath := "/v1/projects/my-proj/locations/us-central1/endpoints/openapi/chat/completions"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
	if req.Header.Get("Authorization") != "Bearer ya29.fake-token" {
		t.Errorf("Authorization = %s", req.Header.Get("Authorization"))
	}
	if req.Header.Get("x-api-key") != "" {
		t.Errorf("x-api-key should be removed")
	}
	if req.Header.Get("anthropic-version") != "" {
		t.Errorf("anthropic-version should be removed")
	}
}

func TestDirectorSetVertexOpenAIUpstream_TrailingSlash(t *testing.T) {
	req := mustNewRequest(t, "POST", "http://localhost/v1/chat/completions", nil)

	directorSetVertexOpenAIUpstream(req,
		"https://us-central1-aiplatform.googleapis.com/v1/projects/p/locations/us-central1/endpoints/openapi/",
		"ya29.token",
	)

	wantPath := "/v1/projects/p/locations/us-central1/endpoints/openapi/chat/completions"
	if req.URL.Path != wantPath {
		t.Errorf("path = %s, want %s", req.URL.Path, wantPath)
	}
}

func TestVertexOpenAITransformerTransformBody_NoOp(t *testing.T) {
	transformer := &VertexOpenAITransformer{}
	input := []byte(`{"model":"google/gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`)
	output, err := transformer.TransformBody(input, "google/gemini-2.5-flash", false, http.Header{})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if string(output) != string(input) {
		t.Errorf("TransformBody should be no-op")
	}
}
