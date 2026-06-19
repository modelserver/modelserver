package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/types"
)

// These tests exercise deriveClientKind independently of extractTraceID so
// we pin the invariant spec §3.2 cares about: classification is decoupled
// from trace-id source precedence AND from the trace_*_enabled config flags.

func TestDeriveClientKind_ClaudeCodeDetectedEvenWithTraceHeader(t *testing.T) {
	body := `{"metadata":{"user_id":"{\"session_id\":\"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\"}"}}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", io.NopCloser(bytes.NewReader([]byte(body))))
	// Even with an explicit trace header that would win extractTraceID, the
	// client-kind detection must still classify as claude-code.
	r.Header.Set("X-Trace-Id", "externally-provided-trace")

	got, _ := deriveClientKind(r, config.TraceConfig{ClaudeCodeTraceEnabled: true})
	if got != types.ClientKindClaudeCode {
		t.Errorf("with X-Trace-Id present: got %q, want %q", got, types.ClientKindClaudeCode)
	}
}

func TestDeriveClientKind_IgnoresClaudeCodeDisabledFlag(t *testing.T) {
	body := `{"metadata":{"user_id":"{\"session_id\":\"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee\"}"}}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", io.NopCloser(bytes.NewReader([]byte(body))))

	// Operator disabled trace body inspection for trace-id purposes, but
	// subscription-eligibility must still classify correctly — otherwise
	// every Claude Code request would be pushed into extra usage.
	got, _ := deriveClientKind(r, config.TraceConfig{ClaudeCodeTraceEnabled: false})
	if got != types.ClientKindClaudeCode {
		t.Errorf("with ClaudeCodeTraceEnabled=false: got %q, want %q", got, types.ClientKindClaudeCode)
	}
}

func TestDeriveClientKind_OpenCodeViaUserAgent(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("User-Agent", "opencode/1.2.3 (darwin)")
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindOpenCode {
		t.Errorf("opencode UA → %q, want %q", got, types.ClientKindOpenCode)
	}
}

func TestDeriveClientKind_OpenClawViaUserAgent(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("User-Agent", "openclaw/2.0")
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindOpenClaw {
		t.Errorf("openclaw UA → %q, want %q", got, types.ClientKindOpenClaw)
	}
}

func TestDeriveClientKind_CodexViaSessionHeader(t *testing.T) {
	// Modern codex CLI (≥0.135.0) sends hyphenated session-id.
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("Session-Id", "codex-1234")
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindCodex {
		t.Errorf("Session-Id header → %q, want %q", got, types.ClientKindCodex)
	}

	// Legacy codex CLI (≤0.124.x) sent underscored session_id; we still
	// recognize it so older clients keep getting trace correlation.
	r = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("Session_id", "codex-legacy")
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindCodex {
		t.Errorf("legacy Session_id header → %q, want %q", got, types.ClientKindCodex)
	}
}

// Real Claude Desktop UA captured from a production rejection. The Electron
// shell concatenates Chromium's UA with the product `Claude/<version>` and
// `Electron/<version>` segments; CLI's UA is the unrelated
// `claude-cli/<version> (external, cli)` string set in normalize_identity.go.
const claudeDesktopRealUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Claude/1.11187.1 Chrome/146.0.7680.216 Electron/41.6.1 Safari/537.36"

func TestDeriveClientKind_ClaudeDesktopViaUserAgent(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("User-Agent", claudeDesktopRealUA)
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindClaudeDesktop {
		t.Errorf("real Claude Desktop UA → %q, want %q", got, types.ClientKindClaudeDesktop)
	}
}

// The CLI's UA contains "claude-cli/" not "claude/". The Electron substring
// gate prevents collision either way, but pin it as a regression test in case
// somebody simplifies the rule to a single substring match.
func TestDeriveClientKind_ClaudeCLIIsNotMisclassifiedAsDesktop(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("User-Agent", "claude-cli/2.1.116 (external, cli)")
	// No metadata.user_id in body — body classifier returns nothing, so this
	// should fall through to ClientKindUnknown (and definitely NOT to desktop).
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got == types.ClientKindClaudeDesktop {
		t.Errorf("claude-cli UA must not classify as desktop; got %q", got)
	}
}

// If somebody hand-fakes a UA with just "Claude/" but no Electron segment
// (e.g. some unrelated tool that uses the substring), don't promote them to
// desktop. The Electron gate is the meaningful signal.
func TestDeriveClientKind_ClaudeUASubstringWithoutElectronStaysUnknown(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("User-Agent", "claude/9.9.9 (some-other-tool)")
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindUnknown {
		t.Errorf("Claude/ UA without Electron/ → %q, want unknown", got)
	}
}

func TestDeriveClientKind_UnknownByDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindUnknown {
		t.Errorf("default → %q, want empty", got)
	}
}

// The Claude Agent SDK / first-party CC plugin (e.g. security-guidance) calls
// /v1/messages via Python urllib with no metadata.user_id, no CCH header, and
// UA "Python-urllib/3.x" — only signal is the SDK-attested system prompt.

// claudeAgentSDKSystemPrompt is the literal Anthropic checks against on the
// /v1/messages OAuth subscriber path; mirroring it here aligns our gate with
// Anthropic's. Keep byte-exact.
const claudeAgentSDKSystemPrompt = "You are a Claude agent, built on Anthropic's Claude Agent SDK."

// securityGuidanceBody mirrors the wire shape the security-guidance plugin's
// llm.py emits: SDK system prompt + structured-outputs output_config.
// Combined with the Python-urllib UA in the same request, it fingerprints
// the plugin uniquely.
const securityGuidanceBody = `{"model":"claude-opus-4-7","max_tokens":1024,"system":"` + claudeAgentSDKSystemPrompt + `","messages":[{"role":"user","content":"hi"}],"output_config":{"format":{"type":"json_schema","schema":{}}}}`

func TestDeriveClientKind_ClaudeAgentSDKSecurityGuidanceFingerprint(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", io.NopCloser(bytes.NewReader([]byte(securityGuidanceBody))))
	r.Header.Set("User-Agent", "Python-urllib/3.14")
	kind, sdk := deriveClientKind(r, config.TraceConfig{})
	if kind != types.ClientKindClaudeCode {
		t.Errorf("security-guidance triad → kind %q, want %q", kind, types.ClientKindClaudeCode)
	}
	if sdk != ClaudeAgentSDKSourceSecurityGuidance {
		t.Errorf("security-guidance triad → sdk source %q, want %q", sdk, ClaudeAgentSDKSourceSecurityGuidance)
	}
}

func TestDeriveClientKind_ClaudeAgentSDKGenericString(t *testing.T) {
	// SDK system prompt but neither Python-urllib UA nor json_schema output —
	// shouldn't fingerprint as security-guidance, only as the generic SDK
	// label. Still admitted as ClaudeCode.
	body := `{"model":"claude-opus-4-7","system":"` + claudeAgentSDKSystemPrompt + `","messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", io.NopCloser(bytes.NewReader([]byte(body))))
	r.Header.Set("User-Agent", "MyAgent/1.0")
	kind, sdk := deriveClientKind(r, config.TraceConfig{})
	if kind != types.ClientKindClaudeCode {
		t.Errorf("generic SDK prompt → kind %q, want %q", kind, types.ClientKindClaudeCode)
	}
	if sdk != ClaudeAgentSDKSourceGeneric {
		t.Errorf("generic SDK prompt → sdk source %q, want %q", sdk, ClaudeAgentSDKSourceGeneric)
	}
}

func TestDeriveClientKind_ClaudeAgentSDKSystemPromptArray(t *testing.T) {
	// Forward-compat: handle the CC CLI's system-array shape even though the
	// current SDK plugin emits a bare string.
	body := `{"model":"claude-opus-4-7","system":[{"type":"text","text":"` + claudeAgentSDKSystemPrompt + `"}],"messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", io.NopCloser(bytes.NewReader([]byte(body))))
	kind, sdk := deriveClientKind(r, config.TraceConfig{})
	if kind != types.ClientKindClaudeCode {
		t.Errorf("SDK system prompt (array) → kind %q, want %q", kind, types.ClientKindClaudeCode)
	}
	if sdk != ClaudeAgentSDKSourceGeneric {
		// No Python-urllib UA was set, so this should fall to the generic label.
		t.Errorf("SDK system prompt (array) without plugin fingerprint → sdk source %q, want %q", sdk, ClaudeAgentSDKSourceGeneric)
	}
}

func TestDeriveClientKind_ClaudeAgentSDKSystemPromptMismatch(t *testing.T) {
	// Trailing space breaks the exact match — same behaviour as Anthropic's
	// own gate ("appending text fails the check").
	body := `{"system":"` + claudeAgentSDKSystemPrompt + ` ","messages":[]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", io.NopCloser(bytes.NewReader([]byte(body))))
	kind, sdk := deriveClientKind(r, config.TraceConfig{})
	if kind != types.ClientKindUnknown {
		t.Errorf("SDK prompt with trailing space → kind %q, want unknown", kind)
	}
	if sdk != "" {
		t.Errorf("SDK prompt with trailing space → sdk source %q, want empty", sdk)
	}

	// Unrelated system prompt must NOT promote to claude-code either.
	body = `{"system":"You are a helpful assistant.","messages":[]}`
	r = httptest.NewRequest(http.MethodPost, "/v1/messages", io.NopCloser(bytes.NewReader([]byte(body))))
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindUnknown {
		t.Errorf("unrelated system prompt → %q, want unknown", got)
	}
}

func TestDeriveClientKind_ClaudeAgentSDKOnlyOnMessagesEndpoint(t *testing.T) {
	body := `{"system":"` + claudeAgentSDKSystemPrompt + `"}`

	// Wrong path: /v1/responses (OpenAI-style) — must not classify even
	// though the body matches.
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", io.NopCloser(bytes.NewReader([]byte(body))))
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got == types.ClientKindClaudeCode {
		t.Errorf("/v1/responses with SDK body must not classify as claude-code; got %q", got)
	}

	// Wrong method: GET — body is not parsed.
	r = httptest.NewRequest(http.MethodGet, "/v1/messages", io.NopCloser(bytes.NewReader([]byte(body))))
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got == types.ClientKindClaudeCode {
		t.Errorf("GET /v1/messages must not classify as claude-code; got %q", got)
	}
}

// readAndRestoreBody buffers and restores the body, so downstream readers
// must still see the full payload after deriveClientKind runs. Pin this so a
// future refactor that swaps the body-reader can't silently break the
// proxy's body forwarding for SDK requests.
func TestDeriveClientKind_ClaudeAgentSDKBodyRestored(t *testing.T) {
	body := `{"model":"claude-opus-4-7","system":"` + claudeAgentSDKSystemPrompt + `","messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", io.NopCloser(bytes.NewReader([]byte(body))))
	if got, _ := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindClaudeCode {
		t.Fatalf("precondition: expected claude-code classification, got %q", got)
	}
	read, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("body re-read: %v", err)
	}
	if string(read) != body {
		t.Errorf("body after deriveClientKind = %q, want %q", string(read), body)
	}
}
