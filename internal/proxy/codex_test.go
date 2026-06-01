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
	if r.URL.Path != "/backend-api/codex/responses" {
		t.Errorf("Path = %q, want /backend-api/codex/responses", r.URL.Path)
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
	// Upstream codex 0.135.0 no longer emits a standalone "Version" header.
	if got := r.Header.Get("Version"); got != "" {
		t.Errorf("Version header should not be sent (upstream dropped it), got %q", got)
	}
	if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "codex_cli_rs/") {
		t.Errorf("User-Agent = %q, want codex_cli_rs/* prefix", got)
	}
	if got := r.Header.Get("Originator"); got != "codex_cli_rs" {
		t.Errorf("Originator = %q, want codex_cli_rs", got)
	}
	if got := r.Header.Get("session-id"); got == "" {
		t.Error("session-id should be auto-filled")
	}
	if got := r.Header.Get("thread-id"); got == "" {
		t.Error("thread-id should be auto-filled (codex 0.135.0 always sends it)")
	}
	if got := r.Header.Get("session_id"); got != "" {
		t.Errorf("underscored session_id should not be sent (openai/codex#22193), got %q", got)
	}
	if got := r.Header.Get("thread_id"); got != "" {
		t.Errorf("underscored thread_id should not be sent (openai/codex#22193), got %q", got)
	}
	// Streaming /responses requests should advertise SSE acceptance, matching
	// codex CLI's explicit Accept header.
	if got := r.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", got)
	}
	// We no longer send a Connection header explicitly — reqwest doesn't,
	// and matching that keeps our wire shape byte-aligned with codex CLI.
	if got := r.Header.Get("Connection"); got != "" {
		t.Errorf("Connection header should not be set, got %q", got)
	}
	if r.Header.Get("OpenAI-Beta") != "" {
		t.Error("OpenAI-Beta must NOT be sent on HTTP /responses")
	}
}

func TestDirectorSetCodexUpstream_PreservesClientSessionAndThreadID(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	r.Header.Set("session-id", "client-supplied-uuid")
	r.Header.Set("thread-id", "client-thread-id")
	r.Header.Set("Accept", "application/json")
	directorSetCodexUpstream(r, "", "tok", "org_1", "up-1")
	if got := r.Header.Get("session-id"); got != "client-supplied-uuid" {
		t.Errorf("session-id = %q, want client-supplied-uuid", got)
	}
	if got := r.Header.Get("thread-id"); got != "client-thread-id" {
		t.Errorf("thread-id = %q, want client-thread-id", got)
	}
	// Client-provided Accept must be preserved — director only fills it as a
	// fallback when the client didn't send one.
	if got := r.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want client-supplied application/json", got)
	}
}

func TestDirectorSetCodexUpstream_MigratesLegacyUnderscoredHeaders(t *testing.T) {
	// A legacy codex CLI (≤0.124.x) sends underscored session_id / thread_id.
	// Director should migrate them to the hyphenated form (which is what
	// upstream 0.135.0 expects) so the value survives the outbound allowlist.
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	r.Header.Set("session_id", "legacy-session")
	r.Header.Set("thread_id", "legacy-thread")
	directorSetCodexUpstream(r, "", "tok", "org_1", "up-1")
	if got := r.Header.Get("session-id"); got != "legacy-session" {
		t.Errorf("session-id = %q, want migrated value 'legacy-session'", got)
	}
	if got := r.Header.Get("thread-id"); got != "legacy-thread" {
		t.Errorf("thread-id = %q, want migrated value 'legacy-thread'", got)
	}
	// Originals must be dropped — otherwise both spellings would be sent.
	if got := r.Header.Get("session_id"); got != "" {
		t.Errorf("legacy session_id should be dropped, got %q", got)
	}
	if got := r.Header.Get("thread_id"); got != "" {
		t.Errorf("legacy thread_id should be dropped, got %q", got)
	}
}

func TestDirectorSetCodexUpstream_ModernHeaderWinsOverLegacy(t *testing.T) {
	// When both forms are present (unlikely but defensible), the modern
	// hyphenated value wins and the legacy one is dropped.
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	r.Header.Set("session_id", "old")
	r.Header.Set("session-id", "new")
	directorSetCodexUpstream(r, "", "tok", "", "up-1")
	if got := r.Header.Get("session-id"); got != "new" {
		t.Errorf("session-id = %q, want 'new'", got)
	}
	if got := r.Header.Get("session_id"); got != "" {
		t.Errorf("legacy session_id should be dropped, got %q", got)
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
	if r.URL.Path != "/api/responses" {
		t.Errorf("Path = %q, want /api/responses (/v1 stripped before join)", r.URL.Path)
	}
}

func TestDirectorSetCodexUpstream_StripsV1Prefix(t *testing.T) {
	// /v1/responses → /responses (no /v1 segment in final URL)
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	directorSetCodexUpstream(r, "", "tok", "", "up-1")
	if r.URL.Path != "/backend-api/codex/responses" {
		t.Errorf("Path = %q, want /backend-api/codex/responses (no /v1)", r.URL.Path)
	}
}

func TestDirectorSetCodexUpstream_NonV1PathPassesThrough(t *testing.T) {
	// A request that doesn't start with /v1 should be joined as-is.
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/responses", nil)
	directorSetCodexUpstream(r, "", "tok", "", "up-1")
	if r.URL.Path != "/backend-api/codex/responses" {
		t.Errorf("Path = %q, want /backend-api/codex/responses", r.URL.Path)
	}
}

func TestDirectorSetCodexUpstream_ResponsesCompactPath(t *testing.T) {
	// /v1/responses/compact must arrive at the codex backend as
	// /backend-api/codex/responses/compact (no /v1, single /responses).
	r := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses/compact", nil)
	directorSetCodexUpstream(r, "", "tok", "", "up-1")
	if r.URL.Path != "/backend-api/codex/responses/compact" {
		t.Errorf("Path = %q, want /backend-api/codex/responses/compact", r.URL.Path)
	}
	// /responses/compact is a unary JSON endpoint upstream; we must NOT
	// advertise SSE acceptance for it.
	if got := r.Header.Get("Accept"); got != "" {
		t.Errorf("Accept should not be auto-set on /responses/compact, got %q", got)
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
		"Accept":             {"text/event-stream"},
		"Chatgpt-Account-Id": {"org_1"},
		"Originator":         {"codex_cli_rs"},
		"Session-Id":         {"uuid"},
		"Thread-Id":          {"tid"},
		"X-Codex-Window-Id":  {"win-1"},
		"X-Random-Garbage":   {"drop me"},
	}
	out := sanitizeOutboundHeaders(in)
	for _, want := range []string{"Authorization", "Accept", "Chatgpt-Account-Id", "Originator", "Session-Id", "Thread-Id", "X-Codex-Window-Id"} {
		if out.Get(want) == "" {
			t.Errorf("expected header %q to pass through", want)
		}
	}
	if out.Get("X-Random-Garbage") != "" {
		t.Error("non-allowlisted header leaked through")
	}
	// Upstream codex 0.135.0 dropped both the "Version" header and the
	// underscored `session_id` alias; verify neither is on the allowlist.
	// `Content-Encoding` is also blocked because we always transform bodies
	// as plain JSON and cannot forward a pre-compressed payload faithfully.
	dropped := http.Header{
		"Version":          {"0.124.0"},
		"Session_id":       {"uuid"},
		"Content-Encoding": {"zstd"},
	}
	out = sanitizeOutboundHeaders(dropped)
	if v := out.Get("Version"); v != "" {
		t.Errorf("Version header should not pass through (upstream dropped it), got %q", v)
	}
	if v := out.Get("Session_id"); v != "" {
		t.Errorf("underscored Session_id should not pass through (openai/codex#22193), got %q", v)
	}
	if v := out.Get("Content-Encoding"); v != "" {
		t.Errorf("Content-Encoding should not pass through (proxy decompresses), got %q", v)
	}
}
