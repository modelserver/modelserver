package proxy

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// codex outbound constants. Pinned to codex CLI 0.124.0; bumping is a
// deliberate maintenance task.
const (
	codexDefaultBaseURL = "https://chatgpt.com/backend-api/codex"
	// CodexVersion and CodexUserAgent are exported so internal/admin can reuse
	// them for token-refresh and utilization requests without duplicating the
	// literals.
	CodexVersion   = "0.124.0"
	CodexUserAgent = "codex_cli_rs/0.124.0 (Linux; x64) Codex"
	// codexOriginator is sent as both part of the User-Agent and as a
	// standalone "Originator" header on every outbound request. This matches
	// codex CLI's default_headers() which inserts it via reqwest's
	// default_headers mechanism (auth/default_client.rs).
	codexOriginator = "codex_cli_rs"
)

// directorSetCodexUpstream rewrites the request to target the codex backend
// using the supplied access token. apiKey resolution / refresh is the
// caller's responsibility (Executor uses CodexOAuthTokenManager).
func directorSetCodexUpstream(req *http.Request, baseURL, accessToken, accountID, _ string) {
	req.URL.Scheme = "https"
	target := codexDefaultBaseURL
	if baseURL != "" {
		target = baseURL
	}
	if u, err := url.Parse(target); err == nil {
		req.URL.Scheme = u.Scheme
		req.URL.Host = u.Host
		// The proxy receives requests at /v1/responses (OpenAI's path convention),
		// but the ChatGPT backend's codex endpoint is at /responses (no /v1 prefix).
		// Strip the leading /v1 before joining so the final URL is correct.
		incoming := strings.TrimPrefix(req.URL.Path, "/v1")
		if incoming == "" {
			incoming = "/"
		}
		if u.Path != "" && u.Path != "/" {
			req.URL.Path = path.Join(u.Path, incoming)
		} else {
			req.URL.Path = incoming
		}
	}
	req.Host = req.URL.Host

	// Strip client-supplied auth so we don't leak sk-... keys to the
	// ChatGPT backend.
	req.Header.Del("x-api-key")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}

	// Codex fingerprint headers always overwrite — the backend gates
	// access on these.
	req.Header.Set("User-Agent", CodexUserAgent)
	req.Header.Set("Originator", codexOriginator)
	req.Header.Set("Version", CodexVersion)
	req.Header.Set("Connection", "keep-alive")

	// session_id: preserve whatever the client provided; otherwise fill
	// a fresh random UUID-style value.  Header NAME is lowercase with an
	// underscore (matches codex CLI's build_conversation_headers).
	if req.Header.Get("session_id") == "" {
		req.Header.Set("session_id", randomCodexSessionID())
	}
}

// randomCodexSessionID returns a hyphenated UUIDv4 string used as a fallback
// session_id when the client didn't supply one. Codex CLI uses UUIDv7 (time-
// ordered); v4 (random) is interchangeable for this opaque correlator. The
// hyphenated 8-4-4-4-12 format matches what the ChatGPT backend expects.
func randomCodexSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	// Set version (4) and variant (RFC 4122) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
