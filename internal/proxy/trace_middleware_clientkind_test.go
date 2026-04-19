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

	got := deriveClientKind(r, config.TraceConfig{ClaudeCodeTraceEnabled: true})
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
	got := deriveClientKind(r, config.TraceConfig{ClaudeCodeTraceEnabled: false})
	if got != types.ClientKindClaudeCode {
		t.Errorf("with ClaudeCodeTraceEnabled=false: got %q, want %q", got, types.ClientKindClaudeCode)
	}
}

func TestDeriveClientKind_OpenCodeViaUserAgent(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("User-Agent", "opencode/1.2.3 (darwin)")
	if got := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindOpenCode {
		t.Errorf("opencode UA → %q, want %q", got, types.ClientKindOpenCode)
	}
}

func TestDeriveClientKind_OpenClawViaUserAgent(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("User-Agent", "openclaw/2.0")
	if got := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindOpenClaw {
		t.Errorf("openclaw UA → %q, want %q", got, types.ClientKindOpenClaw)
	}
}

func TestDeriveClientKind_CodexViaSessionHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("Session_id", "codex-1234")
	if got := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindCodex {
		t.Errorf("Session_id header → %q, want %q", got, types.ClientKindCodex)
	}
}

func TestDeriveClientKind_UnknownByDefault(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if got := deriveClientKind(r, config.TraceConfig{}); got != types.ClientKindUnknown {
		t.Errorf("default → %q, want empty", got)
	}
}
