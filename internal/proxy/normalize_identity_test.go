package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"
	"unicode/utf16"

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
			incoming := cchRe.ReplaceAll([]byte(tc.body), []byte("${1}00000${3}"))

			result := recomputeCCH(incoming)

			m := cchRe.FindSubmatch(result)
			if m == nil {
				t.Fatal("result should contain a cch field")
			}
			got := string(m[2])

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

func TestValidateCCH_UppercaseIsMismatch(t *testing.T) {
	// Real Claude Code CLI always emits lowercase hex. Uppercase cch is a
	// signal of a non-authentic client — reported as mismatch under the
	// byte-exact comparison policy.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.abc; cc_entrypoint=cli; cch=BBC65;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`)
	status, client, expected := ValidateCCH(body)
	if status != CCHStatusMismatch {
		t.Errorf("status = %q, want %q (uppercase should mismatch)", status, CCHStatusMismatch)
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

func TestCCH_UserMessageCchNotTouched(t *testing.T) {
	// User message content containing a literal "cch=abcde;" must NOT be
	// treated as the billing-header cch — the regex is scoped to the
	// x-anthropic-billing-header context.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.114.abc; cc_entrypoint=cli; cch=00000;"}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"example: cch=abcde; here"}]}`)

	out := recomputeCCH(body)

	// The user-message literal must be unchanged.
	if !bytes.Contains(out, []byte("example: cch=abcde; here")) {
		t.Error("recomputeCCH modified user-message content containing cch=abcde")
	}

	// The billing-header cch should now be a non-zero 5-hex value.
	m := cchRe.FindSubmatch(out)
	if m == nil {
		t.Fatal("no cch in billing header after recompute")
	}
	if string(m[2]) == "00000" {
		t.Errorf("billing-header cch was not recomputed (still 00000)")
	}

	// And ValidateCCH on the re-signed body should report match.
	status, client, expected := ValidateCCH(out)
	if status != CCHStatusMatch {
		t.Errorf("ValidateCCH = %s, want match (client=%s expected=%s)", status, client, expected)
	}
}

// fingerprintJSReference mirrors the CLI's JavaScript algorithm:
//
//	chars = [4,7,20].map(i => msg[i] || '0').join('')
//	return sha256(SALT + chars + version).hex().slice(0, 3)
//
// JS strings index by UTF-16 code unit, and Node encodes strings to UTF-8 for
// the hash (substituting U+FFFD for lone surrogates). utf16.Encode +
// utf16.Decode reproduces those semantics exactly.
func fingerprintJSReference(msg, version string) string {
	units := utf16.Encode([]rune(msg))
	var picked [3]uint16
	for i, idx := range [3]int{4, 7, 20} {
		if idx < len(units) {
			picked[i] = units[idx]
		} else {
			picked[i] = '0'
		}
	}
	input := fingerprintSalt + string(utf16.Decode(picked[:])) + version
	hash := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", hash[:])[:3]
}

func TestComputeFingerprint_MatchesCLIAlgorithm(t *testing.T) {
	// Cross-validated against the CLI source (fingerprint.ts):
	//   chars = [4,7,20].map(i => msg[i] || '0').join('')
	//   SHA256(SALT + chars + version).hex().slice(0, 3)
	tests := []struct {
		name    string
		msg     string
		version string
	}{
		{name: "short_message", msg: "hello world here we go", version: "2.1.114"},
		{name: "message_shorter_than_21", msg: "hi", version: "2.1.114"},
		{name: "empty_message", msg: "", version: "2.1.114"},
		// Non-ASCII first user message. Pre-fix Go byte-indexed this and
		// produced the wrong fingerprint for the entire CJK user base.
		{name: "cjk_message", msg: "在 project overview 现在只有 req 统计", version: "2.1.114"},
		{name: "emoji_before_indices", msg: "😀😀😀hello world pad pad pad", version: "2.1.114"},
		{name: "accented_latin", msg: "café résumé naïve façade sample text", version: "2.1.114"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := fingerprintJSReference(tc.msg, tc.version)
			got := computeFingerprint(tc.msg, tc.version)
			if got != want {
				t.Errorf("computeFingerprint(%q, %q) = %q, want %q", tc.msg, tc.version, got, want)
			}
		})
	}
}

// TestComputeFingerprint_KnownVectors pins computeFingerprint to byte-exact
// output from known-good CLI requests. These vectors were captured from real
// billing headers (cc_version=X.Y.Z.<suffix>) — if these break, something
// upstream is diverging from the real CLI algorithm.
func TestComputeFingerprint_KnownVectors(t *testing.T) {
	tests := []struct {
		name    string
		msg     string
		version string
		want    string
	}{
		{
			// Captured from sample.json: first non-injected user text block
			// on a real CLI 2.1.114 request.
			name:    "sample_json_cjk",
			msg:     "在 project overview 现在只有 req 统计，请你也加上 credits 统计。给出详细的实现计划",
			version: "2.1.114",
			want:    "d69",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeFingerprint(tc.msg, tc.version)
			if got != tc.want {
				t.Errorf("computeFingerprint(...) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateFingerprint_Match(t *testing.T) {
	// Build a body where we know the fingerprint is correct.
	msg := "hello world test message here"
	version := "2.1.114"
	fp := computeFingerprint(msg, version)

	body := []byte(fmt.Sprintf(
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli; cch=00000;"}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"%s"}]}`,
		version, fp, msg))

	status, client, expected := ValidateFingerprint(body)
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want match (client=%s expected=%s)", status, client, expected)
	}
}

func TestValidateFingerprint_Mismatch(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.114.zzz; cc_entrypoint=cli; cch=00000;"}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello world test message here"}]}`)

	status, _, _ := ValidateFingerprint(body)
	if status != CCHStatusMismatch {
		t.Errorf("status = %q, want mismatch (fake suffix .zzz)", status)
	}
}

func TestValidateFingerprint_Absent(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`)
	status, _, _ := ValidateFingerprint(body)
	if status != CCHStatusAbsent {
		t.Errorf("status = %q, want absent", status)
	}
}

func TestValidateFingerprint_SkipsSystemReminderBlocks(t *testing.T) {
	// Mirrors sample.json: first user message has <system-reminder> context
	// blocks (from SessionStart hook / skills list / currentDate) followed by
	// the real user text. The CLI computes the fingerprint over the real user
	// text, so modelserver must too.
	msgJSON, err := json.Marshal("在 project overview 现在只有 req 统计，请你也加上 credits 统计。给出详细的实现计划")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := []byte(fmt.Sprintf(
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.114.d69; cc_entrypoint=cli; cch=dd416;"}],`+
			`"model":"claude-opus-4-7","messages":[{"role":"user","content":[`+
			`{"type":"text","text":"<system-reminder>\nSessionStart hook additional context\n</system-reminder>"},`+
			`{"type":"text","text":"<system-reminder>\nskills list\n</system-reminder>"},`+
			`{"type":"text","text":%s}]}]}`, string(msgJSON)))

	status, client, expected := ValidateFingerprint(body)
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want match (client=%s expected=%s)", status, client, expected)
	}
	if expected != "d69" {
		t.Errorf("expected fingerprint = %q, want %q", expected, "d69")
	}
}

func TestValidateFingerprint_SlashCommandUsesFirstBlock(t *testing.T) {
	// When the user types a slash command, the only text block starts with
	// <command-name>... — isCLIInjectedBlock flags every block as injected,
	// so extractFirstUserMessageText must fall back to the first block
	// (what the CLI actually uses to compute the fingerprint).
	msg := "<command-name>/foo</command-name>\n<command-message>foo</command-message>\n<command-args>bar</command-args>"
	version := "2.1.114"
	fp := computeFingerprint(msg, version)
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := []byte(fmt.Sprintf(
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli; cch=dd416;"}],`+
			`"model":"claude-opus-4-7","messages":[{"role":"user","content":[{"type":"text","text":%s}]}]}`,
		version, fp, string(msgJSON)))

	status, client, expected := ValidateFingerprint(body)
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want match (client=%s expected=%s)", status, client, expected)
	}
}

func TestExtractFirstUserMessageText_EmptyFirstBlockIsKept(t *testing.T) {
	// Regression for the fallback-sentinel edge case. If the first text block
	// is empty, the CLI returns "" (it's still the first text block). A naive
	// `if fallback == ""` sentinel would keep looking and promote a later
	// <system-reminder> block as the fallback, diverging from CLI behavior.
	body := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"text","text":""},` +
		`{"type":"text","text":"<system-reminder>\ncontext\n</system-reminder>"}]}]}`)
	got := extractFirstUserMessageText(body)
	if got != "" {
		t.Errorf("extractFirstUserMessageText = %q, want %q", got, "")
	}
}

func TestIsCLIInjectedBlock_LocalCommandStderr(t *testing.T) {
	// Symmetric to <local-command-stdout>; both come from slash commands.
	if !isCLIInjectedBlock("<local-command-stderr>oops</local-command-stderr>") {
		t.Error("<local-command-stderr> should be classified as CLI-injected")
	}
}

func TestValidateFingerprint_ContentArray(t *testing.T) {
	// First user message has content as array of blocks.
	msg := "hello world test message here"
	version := "2.1.114"
	fp := computeFingerprint(msg, version)

	body := []byte(fmt.Sprintf(
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli; cch=00000;"}],"model":"claude-opus-4-7","messages":[{"role":"user","content":[{"type":"text","text":"%s"}]}]}`,
		version, fp, msg))

	status, _, _ := ValidateFingerprint(body)
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want match (content array)", status)
	}
}
