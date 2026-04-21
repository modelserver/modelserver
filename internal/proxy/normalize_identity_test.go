package proxy

import (
	"bytes"
	"regexp"
	"testing"

	"github.com/tidwall/gjson"
)

var deviceIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestDeriveClaudeCodeDeviceID_FormatMatchesCLI(t *testing.T) {
	// Real CLI format: randomBytes(32).toString("hex") → 64 lowercase hex chars.
	got := DeriveClaudeCodeDeviceID("upstream-abc")
	if !deviceIDPattern.MatchString(got) {
		t.Errorf("device_id = %q, want 64 lowercase hex chars", got)
	}
}

func TestDeriveClaudeCodeDeviceID_DeterministicPerUpstream(t *testing.T) {
	a1 := DeriveClaudeCodeDeviceID("upstream-A")
	a2 := DeriveClaudeCodeDeviceID("upstream-A")
	b := DeriveClaudeCodeDeviceID("upstream-B")
	if a1 != a2 {
		t.Errorf("same upstream → different device_id: %q vs %q", a1, a2)
	}
	if a1 == b {
		t.Errorf("different upstreams → same device_id: %q", a1)
	}
}

func TestDeriveClaudeCodeDeviceID_PanicsOnEmpty(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on empty upstreamID")
		}
	}()
	DeriveClaudeCodeDeviceID("")
}

func TestNormalizeMetadataDeviceID_RewritesDeviceID(t *testing.T) {
	body := []byte(`{"metadata":{"user_id":"{\"device_id\":\"client-device-xyz\",\"account_uuid\":\"acct-1\",\"session_id\":\"sess-1\"}"}}`)
	out := normalizeMetadataDeviceID(body, "derived-device-id-value")

	raw := gjson.GetBytes(out, "metadata.user_id").String()
	deviceID := gjson.Get(raw, "device_id").String()
	if deviceID != "derived-device-id-value" {
		t.Errorf("device_id = %q, want %q", deviceID, "derived-device-id-value")
	}
	// Other fields must be preserved.
	if got := gjson.Get(raw, "account_uuid").String(); got != "acct-1" {
		t.Errorf("account_uuid = %q, want acct-1", got)
	}
	if got := gjson.Get(raw, "session_id").String(); got != "sess-1" {
		t.Errorf("session_id = %q, want sess-1", got)
	}
}

func TestNormalizeMetadataDeviceID_NoOpWithoutDeviceIDField(t *testing.T) {
	// metadata.user_id exists but has no device_id field → leave untouched.
	body := []byte(`{"metadata":{"user_id":"{\"session_id\":\"sess-1\"}"}}`)
	out := normalizeMetadataDeviceID(body, "derived")
	if !bytes.Equal(out, body) {
		t.Errorf("body was modified:\n  got:  %s\n  want: %s", out, body)
	}
}

func TestNormalizeMetadataDeviceID_PanicsOnEmptyDeviceID(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on empty deviceID")
		}
	}()
	normalizeMetadataDeviceID([]byte(`{}`), "")
}
