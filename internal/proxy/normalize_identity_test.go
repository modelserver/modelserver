package proxy

import (
	"bytes"
	"regexp"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRecomputeCCH_CrossValidatedWithPythonPOC(t *testing.T) {
	// Test vectors computed with the reverse-engineered seed (CLI 2.1.114):
	//
	//   import xxhash
	//   CCH_SEED = 0x4d659218e32a3268
	//   cch = format(xxhash.xxh64(body.encode(), seed=CCH_SEED).intdigest() & 0xFFFFF, "05x")
	//
	// Each body contains cch=00000 as the placeholder (hash is computed over
	// the placeholder, then the result replaces "cch=00000" in the final output).

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "full_attribution_header",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`,
			want: "09880",
		},
		{
			name: "different_cc_version",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.abc; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`,
			want: "bbc65",
		},
		{
			name: "minimal_body",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cch=00000;"}],"messages":[]}`,
			want: "e15ba",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Start with a body that has a different cch value (simulating
			// an incoming request whose cch is stale after body modification).
			incoming := cchRe.ReplaceAll([]byte(tc.body), []byte("cch=fffff;"))

			result := recomputeCCH(incoming)

			loc := cchRe.FindIndex(result)
			if loc == nil {
				t.Fatal("result should contain a cch field")
			}
			got := string(result[loc[0]+4 : loc[1]-1])

			if got != tc.want {
				t.Errorf("cch = %s, want %s (cross-validated with Python POC)", got, tc.want)
			}
		})
	}
}

func TestRecomputeCCH_NoCCHField(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[]}`)
	result := recomputeCCH(body)
	if string(result) != string(body) {
		t.Error("recomputeCCH should not modify body without cch field")
	}
}

func TestRecomputeCCH_Idempotent(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli; cch=00000;"}],"model":"test","messages":[]}`)

	first := recomputeCCH(body)
	second := recomputeCCH(first)

	loc1 := cchRe.FindIndex(first)
	loc2 := cchRe.FindIndex(second)
	cch1 := string(first[loc1[0]+4 : loc1[1]-1])
	cch2 := string(second[loc2[0]+4 : loc2[1]-1])

	if cch1 != cch2 {
		t.Errorf("not idempotent: first=%s, second=%s", cch1, cch2)
	}
	if cch1 == "00000" {
		t.Error("cch should not remain 00000 after recomputation")
	}
}

// Bodies for ValidateCCH tests reuse vectors from
// TestRecomputeCCH_CrossValidatedWithPythonPOC: a body whose placeholdered
// form (cch=00000;) hashes to value X is a "correct" body when it carries
// cch=X itself.
//
// Full attribution header vector → expected hash "09880" (seed 0x4d659218e32a3268).
const cchMatchBody = `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli; cch=09880;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`

func TestValidateCCH_Match(t *testing.T) {
	status, client, expected := ValidateCCH([]byte(cchMatchBody))
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want %q", status, CCHStatusMatch)
	}
	if client != "09880" {
		t.Errorf("client = %q, want %q", client, "09880")
	}
	if expected != "09880" {
		t.Errorf("expected = %q, want %q", expected, "09880")
	}
}

func TestValidateCCH_Mismatch(t *testing.T) {
	// Same body shape as cchMatchBody but with a wrong cch value.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli; cch=deadb;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`)
	status, client, expected := ValidateCCH(body)
	if status != CCHStatusMismatch {
		t.Errorf("status = %q, want %q", status, CCHStatusMismatch)
	}
	if client != "deadb" {
		t.Errorf("client = %q, want %q", client, "deadb")
	}
	if expected != "09880" {
		t.Errorf("expected = %q, want %q", expected, "09880")
	}
}

func TestValidateCCH_Absent(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[]}`)
	status, client, expected := ValidateCCH(body)
	if status != CCHStatusAbsent {
		t.Errorf("status = %q, want %q", status, CCHStatusAbsent)
	}
	if client != "" || expected != "" {
		t.Errorf("client/expected should be empty, got %q / %q", client, expected)
	}
}

func TestValidateCCH_AbsentAttributionNoCCH(t *testing.T) {
	// Attribution header present but cch segment missing.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli;"}],"model":"claude-3","messages":[]}`)
	status, _, _ := ValidateCCH(body)
	if status != CCHStatusAbsent {
		t.Errorf("status = %q, want %q", status, CCHStatusAbsent)
	}
}

func TestValidateCCH_CaseInsensitive(t *testing.T) {
	// "different_cc_version" vector: expected cch is "bbc65". Verify
	// ValidateCCH accepts client-supplied uppercase "BBC65" as a match.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.abc; cc_entrypoint=cli; cch=BBC65;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`)
	status, client, expected := ValidateCCH(body)
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want %q (case-insensitive match)", status, CCHStatusMatch)
	}
	if client != "BBC65" {
		t.Errorf("client = %q, want %q", client, "BBC65")
	}
	if expected != "bbc65" {
		t.Errorf("expected = %q, want %q", expected, "bbc65")
	}
}

func TestValidateCCH_NoMutation(t *testing.T) {
	body := []byte(cchMatchBody)
	snapshot := append([]byte{}, body...)
	ValidateCCH(body)
	if !bytes.Equal(body, snapshot) {
		t.Error("ValidateCCH must not mutate the input body")
	}
}

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
