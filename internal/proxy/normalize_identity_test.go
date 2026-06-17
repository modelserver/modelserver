package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"testing"
	"unicode/utf16"

	"github.com/OneOfOne/xxhash"
	"github.com/tidwall/gjson"
)

// TestRecomputeCCH_Against2179RealBodies cross-validates recomputeCCH against
// actual CLI 2.1.179 wire bodies captured under gdb (xxh64 update hook at
// 0x2ad99c0). For each fixture: start with cch=00000, run recomputeCCH, assert
// the resulting cch equals the value the real CLI wrote on the wire. Six
// captured requests during reverse-engineering all verified the canonical
// algorithm; two are committed as fixtures for regression coverage.
func TestRecomputeCCH_Against2179RealBodies(t *testing.T) {
	tests := []struct {
		fixture string
		wantCCH string
	}{
		{"testdata/cch_2179_b13.bin", "769b8"},
		{"testdata/cch_2179_b14.bin", "2618e"},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			wire, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			// Reset cch to the placeholder so recomputeCCH has work to do —
			// matches the in-CLI state right before the hash is written.
			incoming := cchRe.ReplaceAll(wire, []byte("${1}00000${3}"))

			result := recomputeCCH(incoming)

			m := cchRe.FindSubmatch(result)
			if m == nil {
				t.Fatal("result should contain a cch field")
			}
			if got := string(m[2]); got != tc.wantCCH {
				t.Errorf("cch = %s, want %s (real 2.1.179 CLI value)", got, tc.wantCCH)
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

func TestValidateCCH_LegacyMatch(t *testing.T) {
	// cchMatchBody contains "max_tokens" implicitly absent and a non-empty
	// model — for these inputs the canonical (2.1.179) and legacy hashes
	// agree, so the body matches under either algorithm. The point of this
	// test is to assert ValidateCCH still recognises this kind of body —
	// reporting "match" with one of the two algorithms.
	status, client, expected, algo := ValidateCCH([]byte(cchMatchBody))
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want %q", status, CCHStatusMatch)
	}
	if client != "09880" {
		t.Errorf("client = %q, want %q", client, "09880")
	}
	if expected != "09880" {
		t.Errorf("expected = %q, want %q", expected, "09880")
	}
	if algo != "v2.1.179" && algo != "legacy" {
		t.Errorf("algo = %q, want v2.1.179 or legacy", algo)
	}
}

func TestValidateCCH_Mismatch(t *testing.T) {
	// Same body shape as cchMatchBody but with a wrong cch value.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli; cch=deadb;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`)
	status, client, expected, algo := ValidateCCH(body)
	if status != CCHStatusMismatch {
		t.Errorf("status = %q, want %q", status, CCHStatusMismatch)
	}
	if client != "deadb" {
		t.Errorf("client = %q, want %q", client, "deadb")
	}
	// "expected" on mismatch is the canonical (2.1.179) hash — that's what a
	// current-version client would have written.
	if expected == "" || expected == "deadb" {
		t.Errorf("expected = %q, want a non-empty hash distinct from the client value", expected)
	}
	if algo != "" {
		t.Errorf("algo on mismatch = %q, want empty", algo)
	}
}

func TestValidateCCH_Absent(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[]}`)
	status, client, expected, algo := ValidateCCH(body)
	if status != CCHStatusAbsent {
		t.Errorf("status = %q, want %q", status, CCHStatusAbsent)
	}
	if client != "" || expected != "" || algo != "" {
		t.Errorf("client/expected/algo should be empty, got %q / %q / %q", client, expected, algo)
	}
}

func TestValidateCCH_AbsentAttributionNoCCH(t *testing.T) {
	// Attribution header present but cch segment missing.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.c30; cc_entrypoint=cli;"}],"model":"claude-3","messages":[]}`)
	status, _, _, _ := ValidateCCH(body)
	if status != CCHStatusAbsent {
		t.Errorf("status = %q, want %q", status, CCHStatusAbsent)
	}
}

func TestValidateCCH_UppercaseIsMismatch(t *testing.T) {
	// Real Claude Code CLI always emits lowercase hex. Uppercase cch is a
	// signal of a non-authentic client — reported as mismatch under the
	// byte-exact comparison policy. The body below: an uppercase variant of
	// a value that WOULD match under the legacy algorithm in lowercase. The
	// match-via-legacy path is byte-exact so uppercase still mismatches even
	// though the bytes (after lowercasing) would match.
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.112.abc; cc_entrypoint=cli; cch=BBC65;"},{"type":"text","text":"You are Claude."}],"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]}`)
	status, client, _, algo := ValidateCCH(body)
	if status != CCHStatusMismatch {
		t.Errorf("status = %q, want %q (uppercase should mismatch)", status, CCHStatusMismatch)
	}
	if client != "BBC65" {
		t.Errorf("client = %q, want %q", client, "BBC65")
	}
	if algo != "" {
		t.Errorf("algo on uppercase mismatch = %q, want empty", algo)
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

// TestValidateCCH_2179RealBodies pins ValidateCCH against real captured wire
// bodies from CLI 2.1.179. Fixtures saved during the gdb reverse-engineering
// session (hooked xxh64 update at 0x2ad99c0; six different requests verified
// the canonical algorithm).
func TestValidateCCH_2179RealBodies(t *testing.T) {
	tests := []struct {
		fixture string
		wantCCH string
	}{
		{"testdata/cch_2179_b13.bin", "769b8"},
		{"testdata/cch_2179_b14.bin", "2618e"},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			body, err := os.ReadFile(tc.fixture)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			status, client, expected, algo := ValidateCCH(body)
			if status != CCHStatusMatch {
				t.Errorf("status = %q, want match (client=%s expected=%s algo=%s)", status, client, expected, algo)
			}
			if client != tc.wantCCH {
				t.Errorf("client = %q, want %q", client, tc.wantCCH)
			}
			if algo != "v2.1.179" {
				t.Errorf("algo = %q, want %q (real 2.1.179 client should match canonical algorithm)", algo, "v2.1.179")
			}
		})
	}
}

// TestValidateCCH_LegacyFallback verifies that bodies signed under the legacy
// (pre-2.1.179) algorithm still report Match — with algo="legacy" so the
// dashboard can track the long tail of old CLIs.
//
// cchMatchBody is constructed without a max_tokens field and with a non-empty
// model. For such bodies the canonical and legacy hashes happen to agree
// (canonicalization is a no-op except for the cch placeholder, which both
// algorithms apply). To force a "legacy only" match we'd need a body that has
// max_tokens and was signed with the legacy algorithm — synthesize one here.
func TestValidateCCH_LegacyFallback(t *testing.T) {
	// Body with max_tokens — under the canonical algorithm, max_tokens is
	// stripped before hashing, so a body signed with the legacy algorithm
	// (which hashes max_tokens as part of the body) will only match via the
	// legacy fallback.
	rawWithMaxTokens := `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.114.abc; cc_entrypoint=cli; cch=00000;"}],"model":"claude-opus-4-7","max_tokens":4096,"messages":[{"role":"user","content":"legacy client test"}]}`

	// Sign with the legacy algorithm: hash the body verbatim (with cch=00000)
	// using the same xxh64 seed, take low 20 bits, write back.
	legacySigned := recomputeCCHLegacyForTest(t, []byte(rawWithMaxTokens))

	status, client, expected, algo := ValidateCCH(legacySigned)
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want match via legacy fallback (client=%s expected=%s algo=%s)", status, client, expected, algo)
	}
	if algo != "legacy" {
		t.Errorf("algo = %q, want %q (body signed with legacy algorithm, canonical should miss)", algo, "legacy")
	}
}

// recomputeCCHLegacyForTest reproduces the pre-2.1.179 CCH algorithm:
// xxh64(body_with_cch_placeholder, seed) & 0xFFFFF, no canonicalization.
// Lives in the test file so the production code can drop the legacy writer
// entirely.
func recomputeCCHLegacyForTest(t *testing.T, body []byte) []byte {
	t.Helper()
	s, e := findBillingHeaderCCHRange(body)
	if s < 0 {
		t.Fatal("recomputeCCHLegacyForTest: no cch field found")
	}
	out := append([]byte{}, body...)
	copy(out[s:e], []byte("00000"))
	h := xxhash.NewS64(cchSeed)
	h.Write(out)
	copy(out[s:e], []byte(fmt.Sprintf("%05x", h.Sum64()&0xFFFFF)))
	return out
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

func TestNormalizeClientIdentity_SetsHeaders(t *testing.T) {
	// Verify the headers a real CLI 2.1.179 request always carries are
	// present on outbound requests. Captured under gdb on CLI 2.1.179:
	// User-Agent advertises the CLI version; X-App: cli is sent on every
	// request from the CLI entrypoint (interactive and --print/sdk-cli).
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	normalizeClientIdentity(req)

	if got := req.Header.Get("User-Agent"); got != fixedUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, fixedUserAgent)
	}
	if got := req.Header.Get("X-App"); got != "cli" {
		t.Errorf("X-App = %q, want %q", got, "cli")
	}
}

func TestNormalizeMetadataDeviceID_RewritesDeviceID(t *testing.T) {
	body := []byte(`{"metadata":{"user_id":"{\"device_id\":\"client-device-xyz\",\"account_uuid\":\"acct-1\",\"session_id\":\"sess-1\"}"}}`)
	out := normalizeMetadataDeviceID(body, "derived-device-id-value")

	raw := gjson.GetBytes(out, "metadata.user_id").String()
	deviceID := gjson.Get(raw, "device_id").String()
	if deviceID != "derived-device-id-value" {
		t.Errorf("device_id = %q, want %q", deviceID, "derived-device-id-value")
	}
	// account_uuid is forced to empty so the upstream account identity is
	// not leaked through the rewritten user_id.
	if got := gjson.Get(raw, "account_uuid").String(); got != "" {
		t.Errorf("account_uuid = %q, want empty", got)
	}
	// session_id is preserved.
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

func TestNormalizeAttributionHeader_RecomputesFingerprintSuffix(t *testing.T) {
	// Client sends a well-formed 2.1.117.<suffix> billing header. After normalize,
	// the version must be fixedCCVersion and the suffix must match what a genuine
	// CLI of fixedCCVersion would compute for the same first user message — the
	// client's original suffix (computed against its own version) is stale.
	firstMsg := "hello world fingerprint check message here"
	clientSuffix := computeFingerprint(firstMsg, "2.1.117")
	msgJSON, err := json.Marshal(firstMsg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	body := []byte(fmt.Sprintf(
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.117.%s; cc_entrypoint=claude-vscode; cch=00000;"}],`+
			`"model":"claude-opus-4-7","messages":[{"role":"user","content":[{"type":"text","text":%s}]}]}`,
		clientSuffix, string(msgJSON)))

	out := normalizeAttributionHeader(body)

	want := "cc_version=" + fixedCCVersion + "." + computeFingerprint(firstMsg, fixedCCVersion) + ";"
	if !bytes.Contains(out, []byte(want)) {
		t.Errorf("normalized body missing %q; got system[0]=%q",
			want, gjson.GetBytes(out, "system.0.text").Str)
	}
	if !bytes.Contains(out, []byte("cc_entrypoint=cli;")) {
		t.Errorf("cc_entrypoint should be rewritten to cli")
	}

	// And ValidateFingerprint on the normalized body must report match — proves
	// the suffix is internally consistent with cc_version after rewrite.
	fpStatus, fpClient, fpExpected := ValidateFingerprint(out)
	if fpStatus != CCHStatusMatch {
		t.Errorf("ValidateFingerprint after normalize = %s, want match (client=%s expected=%s)",
			fpStatus, fpClient, fpExpected)
	}
}

func TestNormalizeRequestBody_FingerprintAndCCHBothValid(t *testing.T) {
	// End-to-end: client sends a body with a stale-for-fixedCCVersion suffix.
	// After normalizeRequestBody the wire body must validate on BOTH attestations
	// for fixedCCVersion — this is what the ClaudeCode upstream receives.
	firstMsg := "e2e normalize check: both attestations must be valid"
	clientSuffix := computeFingerprint(firstMsg, "2.1.117") // intentionally wrong version
	msgJSON, err := json.Marshal(firstMsg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := []byte(fmt.Sprintf(
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.117.%s; cc_entrypoint=claude-vscode; cch=ffffa;"},`+
			`{"type":"text","text":"You are Claude."}],`+
			`"model":"claude-opus-4-7",`+
			`"metadata":{"user_id":"{\"device_id\":\"aaaabbbbccccddddeeee00000000\",\"account_uuid\":\"x\",\"session_id\":\"s\"}"},`+
			`"messages":[{"role":"user","content":[{"type":"text","text":%s}]}]}`,
		clientSuffix, string(msgJSON)))

	out := normalizeRequestBody(append([]byte{}, body...), DeriveClaudeCodeDeviceID("upstream-id-42"))

	if st, c, e, _ := ValidateCCH(out); st != CCHStatusMatch {
		t.Errorf("ValidateCCH = %s (client=%s expected=%s); body cch must be consistent", st, c, e)
	}
	if st, c, e := ValidateFingerprint(out); st != CCHStatusMatch {
		t.Errorf("ValidateFingerprint = %s (client=%s expected=%s); suffix must match fixedCCVersion", st, c, e)
	}
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
	status, client, expected, _ := ValidateCCH(out)
	if status != CCHStatusMatch {
		t.Errorf("ValidateCCH = %s, want match (client=%s expected=%s)", status, client, expected)
	}
}

func TestCCH_EmbeddedBillingHeaderInUserMessageNotMatched(t *testing.T) {
	// A user-message text block can contain the literal string
	// "x-anthropic-billing-header: ... cch=AAAAA;" — for example, a tool
	// result quoting an earlier session's wire body, or a Read of a captured
	// JSON file. Since JSON encodes inner double-quotes as \" (a backslash
	// followed by a quote), a byte-level regex with [^"] will still cross
	// from the user-message string into nowhere — but it CAN match entirely
	// inside that user-message string when no quote sits between the
	// embedded "x-anthropic-billing-header:" and its "cch=XXXXX;".
	//
	// Only system[*].text starting with the billing-header prefix is
	// authoritative. Both ValidateCCH and recomputeCCH must operate on that
	// header and leave the user-message text untouched.
	const embedded = "<system-reminder>quoted prior wire body: x-anthropic-billing-header: cc_version=2.1.114.d69; cc_entrypoint=cli; cch=097ba;</system-reminder>"
	embeddedJSON, err := json.Marshal(embedded)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := []byte(fmt.Sprintf(
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.116.0cb; cc_entrypoint=cli; cch=00000;"}],`+
			`"model":"claude-opus-4-7",`+
			`"messages":[{"role":"user","content":[{"type":"text","text":%s}]}]}`,
		string(embeddedJSON)))

	// recomputeCCH must NOT mutate the embedded user-message header.
	out := recomputeCCH(body)
	if !bytes.Contains(out, []byte("cch=097ba;")) {
		t.Error("recomputeCCH overwrote the embedded user-message cch=097ba")
	}

	// ValidateCCH on the re-signed body must report match — and the client
	// cch reported must be the value computed for the real system header,
	// NOT the embedded "097ba".
	status, client, expected, _ := ValidateCCH(out)
	if status != CCHStatusMatch {
		t.Errorf("ValidateCCH = %s, want match (client=%s expected=%s)", status, client, expected)
	}
	if client == "097ba" {
		t.Errorf("ValidateCCH client = %q (taken from embedded user-message text); must come from system[0] header", client)
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
	// <command-name>... — that block is the user's action, not a CLI-injected
	// reminder, so isCLIInjectedBlock returns false and extractFirstUserMessageText
	// returns it directly. This is what the CLI uses to compute the fingerprint.
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

func TestIsCLIInjectedBlock_LateInjectedReminders(t *testing.T) {
	// Only blocks the CLI injects AFTER fingerprint computation count as
	// "injected" for skip purposes: the system-reminder wrapper (used for
	// SessionStart hook output, skill list, claudeMd context, attachment
	// previews) and the local-command-caveat that prefaces slash-command
	// output. Anything else — including the slash-command blocks themselves
	// — is part of the user's action sequence and IS used by the CLI as
	// the fingerprint source.
	for _, s := range []string{
		"<system-reminder>\nfoo\n</system-reminder>",
		"<local-command-caveat>caveat text",
	} {
		if !isCLIInjectedBlock(s) {
			t.Errorf("isCLIInjectedBlock(%q) = false, want true", s)
		}
	}
}

func TestIsCLIInjectedBlock_SlashCommandsNotInjected(t *testing.T) {
	// Slash-command blocks (<command-name>, <command-message>, <command-args>,
	// <local-command-stdout>, <local-command-stderr>) are user-triggered
	// actions, not CLI-injected metadata. Real wire traffic shows the CLI
	// computes the fingerprint over <command-name>/effort..., <command-name>/model...,
	// etc. — so our extractor must NOT skip them.
	for _, s := range []string{
		"<command-name>/effort</command-name>",
		"<command-message>effort</command-message>",
		"<command-args>max</command-args>",
		"<local-command-stdout>Set effort level to max</local-command-stdout>",
		"<local-command-stderr>oops</local-command-stderr>",
	} {
		if isCLIInjectedBlock(s) {
			t.Errorf("isCLIInjectedBlock(%q) = true, want false (slash-command block is user action, not CLI injection)", s)
		}
	}
}

func TestValidateFingerprint_SlashCommandIsFingerprintSourceAfterReminders(t *testing.T) {
	// Real wire pattern (from sample 99d0ae18 / cc_version=2.1.112.c32):
	// the CLI's first user message in messagesForAPI at fingerprint time
	// contains only the slash-command block + its stdout + the user's actual
	// query. AFTER fingerprint, the CLI prepends skill_listing,
	// prependUserContext (claudeMd), and the local-command-caveat. Wire body
	// shows them all merged: [SR_skills, SR_claudeMd, caveat, command-name,
	// stdout, user_query]. The fingerprint matches the command-name block —
	// which means our extractor must skip past system-reminder + caveat and
	// stop on <command-name>, NOT skip past it to the actual user query.
	msg := "<command-name>/effort</command-name>\n            <command-message>effort</command-message>\n            <command-args>max</command-args>"
	version := "2.1.112"
	fp := computeFingerprint(msg, version)
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := []byte(fmt.Sprintf(
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli; cch=00000;"}],`+
			`"model":"claude-opus-4-7","messages":[{"role":"user","content":[`+
			`{"type":"text","text":"<system-reminder>\nThe following skills are available for use with the Skill tool\n</system-reminder>"},`+
			`{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context\n</system-reminder>"},`+
			`{"type":"text","text":"<local-command-caveat>Caveat: messages below were generated locally</local-command-caveat>"},`+
			`{"type":"text","text":%s},`+
			`{"type":"text","text":"<local-command-stdout>Set effort level to max</local-command-stdout>"},`+
			`{"type":"text","text":"the actual user query that should NOT be the fingerprint source"}]}]}`,
		version, fp, string(msgJSON)))

	status, client, expected := ValidateFingerprint(body)
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want match (client=%s expected=%s)", status, client, expected)
	}
}

func TestValidateFingerprint_CompactSummaryAcrossUserMessages(t *testing.T) {
	// Real wire pattern (from sample b64f2527 / cc_version=2.1.114.e2a):
	// after /compact, the wire body has TWO consecutive user messages —
	// messages[0] is a STRING that's a bubbled-up attachment
	// ("<system-reminder>Called the Read tool...</system-reminder>"), and
	// messages[1] is an array starting with more attachment results and
	// containing the /compact summary block deeper in. The CLI's fingerprint
	// is computed on the compact summary, so our extractor must:
	//   1) treat the string-content user message like a single text block
	//      (skipping it because it starts with <system-reminder>), and
	//   2) keep scanning into messages[1] until it hits the summary.
	msg := "This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation."
	version := "2.1.114"
	fp := computeFingerprint(msg, version)
	msgJSON, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := []byte(fmt.Sprintf(
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli; cch=00000;"}],`+
			`"model":"claude-opus-4-7","messages":[`+
			`{"role":"user","content":"<system-reminder>\nCalled the Read tool with the following input: {\"file_path\":\"/x\"}\n</system-reminder>"},`+
			`{"role":"user","content":[`+
			`{"type":"text","text":"<system-reminder>\nResult of calling the Read tool:\n...truncated...\n</system-reminder>"},`+
			`{"type":"text","text":"<system-reminder>\nAs you answer the user's questions...\n</system-reminder>"},`+
			`{"type":"text","text":%s},`+
			`{"type":"text","text":"<local-command-caveat>Caveat...</local-command-caveat>"},`+
			`{"type":"text","text":"<command-name>/compact</command-name>"},`+
			`{"type":"text","text":"trailing user query — not the fingerprint source"}]}]}`,
		version, fp, string(msgJSON)))

	status, client, expected := ValidateFingerprint(body)
	if status != CCHStatusMatch {
		t.Errorf("status = %q, want match (client=%s expected=%s)", status, client, expected)
	}
}

func TestExtractFirstUserMessageText_StopsAtAssistantMessage(t *testing.T) {
	// Cross-message scan must NOT cross an assistant boundary. If the first
	// user message has only late-injected blocks and the next message is an
	// assistant, fall back to the first user message's first block instead
	// of scanning into a later user message.
	body := []byte(`{"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"<system-reminder>\nfoo\n</system-reminder>"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"reply"}]},` +
		`{"role":"user","content":[{"type":"text","text":"second user message"}]}` +
		`]}`)
	got := extractFirstUserMessageText(body)
	want := "<system-reminder>\nfoo\n</system-reminder>"
	if got != want {
		t.Errorf("extractFirstUserMessageText = %q, want %q (must not scan past assistant)", got, want)
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
