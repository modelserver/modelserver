package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestCodexTransformer_SetUpstream_RawToken(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	tr := &CodexTransformer{}
	if err := tr.SetUpstream(r, &types.Upstream{ID: "u1"}, "raw-token"); err != nil {
		t.Fatalf("SetUpstream: %v", err)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer raw-token" {
		t.Errorf("Authorization = %q, want Bearer raw-token", got)
	}
	if r.Header.Get("ChatGPT-Account-ID") != "" {
		t.Error("expected no account id header for raw-token path")
	}
}

func TestCodexTransformer_SetUpstream_JSONBlob(t *testing.T) {
	creds := CodexCredentials{
		AccessToken:      "blob-at",
		ChatGPTAccountID: "org_blob",
	}
	raw, _ := json.Marshal(creds)
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	tr := &CodexTransformer{}
	if err := tr.SetUpstream(r, &types.Upstream{ID: "u1"}, string(raw)); err != nil {
		t.Fatalf("SetUpstream: %v", err)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer blob-at" {
		t.Errorf("Authorization = %q, want Bearer blob-at", got)
	}
	if got := r.Header.Get("ChatGPT-Account-ID"); got != "org_blob" {
		t.Errorf("ChatGPT-Account-ID = %q, want org_blob", got)
	}
}

func TestCodexTransformer_TransformBody_PassThrough(t *testing.T) {
	tr := &CodexTransformer{}
	in := []byte(`{"model":"gpt-5","input":"hi"}`)
	out, err := tr.TransformBody(in, "gpt-5", true, http.Header{})
	if err != nil {
		t.Fatalf("TransformBody: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("body modified: got %s", string(out))
	}
}

func TestGetProviderTransformer_Codex(t *testing.T) {
	got := GetProviderTransformer(types.ProviderCodex)
	if _, ok := got.(*CodexTransformer); !ok {
		t.Errorf("GetProviderTransformer(codex) = %T, want *CodexTransformer", got)
	}
}
