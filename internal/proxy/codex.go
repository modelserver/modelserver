package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"path"
)

// codex outbound constants. Pinned to a recent codex CLI release at
// implementation time; bumping is a deliberate maintenance task.
const (
	codexDefaultBaseURL = "https://chatgpt.com/backend-api/codex"
	// The originator string ("codex_cli_rs") is part of the User-Agent
	// prefix per codex CLI's get_codex_user_agent(); it is NOT sent as a
	// standalone header.
	codexVersion   = "0.55.0"
	codexUserAgent = "codex_cli_rs/0.55.0 (Linux; x64) Codex"
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
		if u.Path != "" && u.Path != "/" {
			req.URL.Path = path.Join(u.Path, req.URL.Path)
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
	req.Header.Set("User-Agent", codexUserAgent)
	req.Header.Set("Version", codexVersion)
	req.Header.Set("Connection", "keep-alive")

	// session_id: preserve whatever the client provided; otherwise fill
	// a fresh random UUID-style value.  Header NAME is lowercase with an
	// underscore (matches codex CLI's build_conversation_headers).
	if req.Header.Get("session_id") == "" {
		req.Header.Set("session_id", randomCodexSessionID())
	}
}

// randomCodexSessionID returns a 32-hex-char value used as a fallback
// session_id when the client didn't supply one. Cheaper than a real UUIDv4
// formatter and equally opaque to the upstream.
func randomCodexSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
