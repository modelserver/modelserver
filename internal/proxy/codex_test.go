package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDirectorSetCodexUpstream_DefaultBaseURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader("{}"))
	r.Header.Set("x-api-key", "sk-leak")
	r.Header.Set("authorization", "Bearer client-token")

	directorSetCodexUpstream(r, "", "fresh-token", "org_42", "up-1")

	if r.URL.Host != "chatgpt.com" {
		t.Errorf("Host = %q, want chatgpt.com", r.URL.Host)
	}
	if r.URL.Path != "/backend-api/codex/v1/responses" {
		t.Errorf("Path = %q, want /backend-api/codex/v1/responses", r.URL.Path)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer fresh-token" {
		t.Errorf("Authorization = %q, want Bearer fresh-token", got)
	}
	if got := r.Header.Get("ChatGPT-Account-ID"); got != "org_42" {
		t.Errorf("ChatGPT-Account-ID = %q, want org_42", got)
	}
	if r.Header.Get("x-api-key") != "" {
		t.Error("x-api-key was not stripped")
	}
	if got := r.Header.Get("Version"); got != codexVersion {
		t.Errorf("Version = %q, want %q", got, codexVersion)
	}
	if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "codex_cli_rs/") {
		t.Errorf("User-Agent = %q, want codex_cli_rs/* prefix", got)
	}
	if got := r.Header.Get("Originator"); got != "codex_cli_rs" {
		t.Errorf("Originator = %q, want codex_cli_rs", got)
	}
	if got := r.Header.Get("session_id"); got == "" {
		t.Error("session_id should be auto-filled")
	}
	if r.Header.Get("OpenAI-Beta") != "" {
		t.Error("OpenAI-Beta must NOT be sent on HTTP /responses")
	}
}

func TestDirectorSetCodexUpstream_PreservesClientSessionID(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	r.Header.Set("session_id", "client-supplied-uuid")
	directorSetCodexUpstream(r, "", "tok", "org_1", "up-1")
	if got := r.Header.Get("session_id"); got != "client-supplied-uuid" {
		t.Errorf("session_id = %q, want client-supplied-uuid", got)
	}
}

func TestDirectorSetCodexUpstream_OmitsAccountIDWhenEmpty(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	directorSetCodexUpstream(r, "", "tok", "", "up-1")
	if v := r.Header.Get("ChatGPT-Account-ID"); v != "" {
		t.Errorf("expected no ChatGPT-Account-ID header, got %q", v)
	}
}

func TestDirectorSetCodexUpstream_CustomBaseURL(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	directorSetCodexUpstream(r, "https://example.com/api", "tok", "org_1", "up-1")
	if r.URL.Host != "example.com" {
		t.Errorf("Host = %q, want example.com", r.URL.Host)
	}
	if r.URL.Path != "/api/v1/responses" {
		t.Errorf("Path = %q, want /api/v1/responses", r.URL.Path)
	}
}

func TestRandomCodexSessionID_Format(t *testing.T) {
	got := randomCodexSessionID()
	// 36 chars, 4 hyphens at positions 8, 13, 18, 23
	if len(got) != 36 {
		t.Errorf("len = %d, want 36; got %q", len(got), got)
	}
	for _, pos := range []int{8, 13, 18, 23} {
		if got[pos] != '-' {
			t.Errorf("expected '-' at index %d, got %q", pos, got)
		}
	}
}

func TestSanitizeOutboundHeaders_PassesCodexHeaders(t *testing.T) {
	in := http.Header{
		"Authorization":      {"Bearer x"},
		"Chatgpt-Account-Id": {"org_1"},
		"Originator":         {"codex_cli_rs"},
		"Version":            {"0.55.0"},
		"Session_id":         {"uuid"},
		"X-Codex-Window-Id":  {"win-1"},
		"X-Random-Garbage":   {"drop me"},
	}
	out := sanitizeOutboundHeaders(in)
	for _, want := range []string{"Authorization", "Chatgpt-Account-Id", "Originator", "Version", "Session_id", "X-Codex-Window-Id"} {
		if out.Get(want) == "" {
			t.Errorf("expected header %q to pass through", want)
		}
	}
	if out.Get("X-Random-Garbage") != "" {
		t.Error("non-allowlisted header leaked through")
	}
}
