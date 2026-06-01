package proxy

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// codex outbound constants. Pinned to codex CLI 0.135.0; bumping is a
// deliberate maintenance task.
const (
	codexDefaultBaseURL = "https://chatgpt.com/backend-api/codex"
	// CodexVersion and CodexUserAgent are exported so internal/admin can reuse
	// them for token-refresh and utilization requests without duplicating the
	// literals. CodexVersion is no longer sent as a standalone "Version"
	// header (upstream stopped emitting it; PR openai/codex#22193 era) but
	// remains the source of truth for the version embedded in CodexUserAgent.
	CodexVersion   = "0.135.0"
	CodexUserAgent = "codex_cli_rs/0.135.0 (Linux; x64) Codex"
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
	// access on these. The "Version" header used by older codex CLIs
	// (≤0.124.x) is intentionally NOT sent here: upstream removed it on
	// its way to 0.135.0 and the ChatGPT backend no longer relies on it.
	// "Connection: keep-alive" is also omitted to stay byte-identical with
	// codex CLI, which lets reqwest manage connection reuse itself.
	req.Header.Set("User-Agent", CodexUserAgent)
	req.Header.Set("Originator", codexOriginator)

	// session-id / thread-id: preserve whatever the client provided;
	// otherwise fill fresh random UUID-style values. Codex 0.135.0 emits
	// BOTH on every /responses request via build_session_headers
	// (codex-rs/codex-api/src/requests/headers.rs) and only in the
	// hyphenated form (openai/codex#22193 dropped the underscored aliases
	// because some proxies reject "_" in header names).
	//
	// Legacy codex CLIs (≤0.124.x) sent the underscored `session_id` /
	// `thread_id` form. If we see those, migrate them to the hyphenated
	// form (and drop the originals) so the upstream allowlist passes them
	// through — otherwise sanitizeOutboundHeaders would strip them and we'd
	// lose the client's correlation id at the proxy → backend hop.
	migrateCodexLegacySessionHeader(req, "session_id", "session-id")
	migrateCodexLegacySessionHeader(req, "thread_id", "thread-id")
	if req.Header.Get("session-id") == "" {
		req.Header.Set("session-id", randomCodexSessionID())
	}
	if req.Header.Get("thread-id") == "" {
		req.Header.Set("thread-id", randomCodexSessionID())
	}

	// For SSE streaming requests, codex CLI explicitly sets
	// `Accept: text/event-stream` (codex-rs/codex-api/src/endpoint/responses.rs).
	// The Responses API will negotiate SSE from `stream: true` in the body
	// regardless, but matching the CLI's wire shape avoids surprises if the
	// backend ever starts gating on Accept.
	if isCodexStreamingPath(req.URL.Path) && req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
}

// isCodexStreamingPath reports whether the rewritten request targets a codex
// endpoint that emits Server-Sent Events. Today only `/responses` (the unary
// `/responses/compact` path is JSON, not SSE).
func isCodexStreamingPath(p string) bool {
	return strings.HasSuffix(p, "/responses")
}

// migrateCodexLegacySessionHeader rewrites a legacy underscored header name
// (`session_id` / `thread_id`) to its modern hyphenated equivalent so the
// outbound allowlist forwards it. If the hyphenated form is already set the
// legacy one is just dropped — modern wins.
func migrateCodexLegacySessionHeader(req *http.Request, legacy, modern string) {
	val := req.Header.Get(legacy)
	if val == "" {
		return
	}
	req.Header.Del(legacy)
	if req.Header.Get(modern) == "" {
		req.Header.Set(modern, val)
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
