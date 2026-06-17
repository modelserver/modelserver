package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/OneOfOne/xxhash"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	ccVersionRe    = regexp.MustCompile(`cc_version=[^;]*;`)
	ccEntrypointRe = regexp.MustCompile(`cc_entrypoint=[^;]*;`)
	// billingCCHRe matches `cch=<5-hex>;` within the (already isolated) text
	// of the system[*] billing-header entry. Locating the entry itself is done
	// JSON-aware via gjson so embedded copies of the header text inside other
	// JSON strings (e.g. tool results quoting prior wire bodies) are ignored.
	billingCCHRe = regexp.MustCompile(`\bcch=([0-9a-fA-F]{5});`)
	// cchRe is kept for tests only — it scans for the FIRST cch= in the body
	// scoped by the billing-header prefix. Production code must not use this:
	// when a JSON-string-encoded copy of the header appears in a user-message
	// text block, the [^"] filter does not actually prevent matching inside
	// that string. Use findBillingHeaderCCHRange instead.
	cchRe = regexp.MustCompile(`(x-anthropic-billing-header:[^"]*?\bcch=)([0-9a-fA-F]{5})(;)`)
)

const (
	cchSeed         uint64 = 0x4d659218e32a3268
	fingerprintSalt        = "59cf53e54c78"
)

// Constants pinned to CLI 2.1.179 wire signature. Bumping these is a
// deliberate maintenance task — see docs/superpowers/plans for the
// reverse-engineering write-up, and testdata/cch_2179_*.bin for the
// fixtures that pin the CCH canonicalization to this version's behavior.
const (
	fixedUserAgent      = "claude-cli/2.1.179 (external, cli)"
	fixedStainlessOS    = "Linux"
	fixedStainlessRtVer = "v24.3.0"
	fixedStainlessPkgV  = "0.94.0"
	fixedStainlessArch  = "x64"
	fixedStainlessLang  = "js"
	fixedStainlessRt    = "node"
	fixedCCVersion      = "2.1.179"
)

// deviceIDHMACKey is the fixed HMAC key used to derive a stable per-upstream
// device_id from upstream.ID. The real Claude Code CLI generates device_id
// as randomBytes(32).toString("hex") stored in the user's global config — a
// stable 64-hex-char ID per install. Mirroring that semantics here, each
// ClaudeCode upstream gets its own deterministically-derived device_id rather
// than sharing a single fixed one.
const deviceIDHMACKey = "modelserver:claudecode:device_id:v1"

// DeriveClaudeCodeDeviceID returns the 64-hex-char device_id for a given
// ClaudeCode upstream. Deterministic in upstream.ID so the value is stable
// across restarts without needing DB storage. Panics if upstreamID is empty,
// which signals a caller-side bug (every upstream row has an ID).
func DeriveClaudeCodeDeviceID(upstreamID string) string {
	if upstreamID == "" {
		panic("DeriveClaudeCodeDeviceID: empty upstreamID")
	}
	mac := hmac.New(sha256.New, []byte(deviceIDHMACKey))
	mac.Write([]byte(upstreamID))
	return hex.EncodeToString(mac.Sum(nil))
}

func normalizeClientIdentity(req *http.Request) {
	req.Header.Set("User-Agent", fixedUserAgent)
	// X-App is always "cli" on real CLI 2.1.179 requests, regardless of
	// entrypoint (interactive vs. --print/sdk-cli). Confirmed by raw capture.
	req.Header.Set("X-App", "cli")
	req.Header.Set("X-Stainless-Lang", fixedStainlessLang)
	req.Header.Set("X-Stainless-Package-Version", fixedStainlessPkgV)
	req.Header.Set("X-Stainless-Os", fixedStainlessOS)
	req.Header.Set("X-Stainless-Runtime", fixedStainlessRt)
	req.Header.Set("X-Stainless-Runtime-Version", fixedStainlessRtVer)
	req.Header.Set("X-Stainless-Arch", fixedStainlessArch)

	req.Header.Del("X-Client-App")
	req.Header.Del("X-Claude-Remote-Container-Id")
	req.Header.Del("X-Claude-Remote-Session-Id")
	req.Header.Del("X-Anthropic-Additional-Protection")
}

func normalizeRequestBody(body []byte, deviceID string) []byte {
	body = normalizeMetadataDeviceID(body, deviceID)
	body = normalizeAttributionHeader(body)
	body = recomputeCCH(body)
	return body
}

func normalizeMetadataDeviceID(body []byte, deviceID string) []byte {
	if deviceID == "" {
		panic("normalizeMetadataDeviceID: empty deviceID")
	}
	raw := gjson.GetBytes(body, "metadata.user_id")
	if !raw.Exists() || raw.Type != gjson.String {
		return body
	}

	// metadata.user_id is itself a JSON object encoded as a string. The real
	// CLI emits keys in insertion order: device_id, account_uuid, session_id.
	// Unmarshalling into a map[string]interface{} loses that ordering — Go's
	// json.Marshal then sorts keys alphabetically, which would be a tell on
	// the wire. Mutate the inner JSON string via sjson instead so the
	// original key order is preserved.
	inner := raw.Str
	if !gjson.Get(inner, "device_id").Exists() {
		return body
	}

	// sjson.Set on a string: only writes if the field already exists at this
	// path, which is what we want — don't materialize fields the client didn't
	// send. account_uuid is treated the same way.
	updated, err := sjson.Set(inner, "device_id", deviceID)
	if err != nil {
		return body
	}
	if gjson.Get(updated, "account_uuid").Exists() {
		if next, err := sjson.Set(updated, "account_uuid", ""); err == nil {
			updated = next
		}
	}

	result, err := sjson.SetBytes(body, "metadata.user_id", updated)
	if err != nil {
		return body
	}
	return result
}

func normalizeAttributionHeader(body []byte) []byte {
	sys := gjson.GetBytes(body, "system")
	if !sys.Exists() {
		return body
	}

	// Recompute the fingerprint suffix for fixedCCVersion using the current
	// body's first user message. Keeping the client's original suffix (which
	// was computed against its own cc_version) would leave cc_version
	// internally inconsistent after the version rewrite below — a genuine CLI
	// of fixedCCVersion always has a suffix derived from its own version.
	newSuffix := computeFingerprint(extractFirstUserMessageText(body), fixedCCVersion)

	if sys.IsArray() {
		for i, item := range sys.Array() {
			text := item.Get("text")
			if !text.Exists() || text.Type != gjson.String {
				continue
			}
			if strings.HasPrefix(text.Str, "x-anthropic-billing-header") {
				normalized := normalizeAttributionString(text.Str, newSuffix)
				path := "system." + strconv.Itoa(i) + ".text"
				if result, err := sjson.SetBytes(body, path, normalized); err == nil {
					body = result
				}
				break
			}
		}
	} else if sys.Type == gjson.String {
		if strings.HasPrefix(sys.Str, "x-anthropic-billing-header") {
			normalized := normalizeAttributionString(sys.Str, newSuffix)
			if result, err := sjson.SetBytes(body, "system", normalized); err == nil {
				body = result
			}
		}
	}

	return body
}

func normalizeAttributionString(s, newSuffix string) string {
	s = ccVersionRe.ReplaceAllString(s, "cc_version="+fixedCCVersion+"."+newSuffix+";")
	s = ccEntrypointRe.ReplaceAllString(s, "cc_entrypoint=cli;")
	return s
}

// findBillingHeaderCCHRange returns [start, end) byte offsets of the 5-hex-char
// cch value inside the system[*].text entry that begins with
// "x-anthropic-billing-header:". Returns (-1, -1) if no such header exists or
// it lacks a cch=<5hex>; segment.
//
// Locating the entry through gjson — instead of a body-wide regex — is what
// keeps embedded copies of the header text (e.g. inside a tool result that
// quotes a prior wire body) from hijacking the match.
func findBillingHeaderCCHRange(body []byte) (start, end int) {
	sys := gjson.GetBytes(body, "system")
	if !sys.Exists() {
		return -1, -1
	}
	var (
		text     string
		bodyBase int
	)
	switch {
	case sys.IsArray():
		for _, item := range sys.Array() {
			t := item.Get("text")
			if t.Exists() && t.Type == gjson.String && strings.HasPrefix(t.Str, "x-anthropic-billing-header:") {
				text = t.Str
				bodyBase = t.Index + 1 // skip the opening quote
				goto found
			}
		}
		return -1, -1
	case sys.Type == gjson.String && strings.HasPrefix(sys.Str, "x-anthropic-billing-header:"):
		text = sys.Str
		bodyBase = sys.Index + 1
	default:
		return -1, -1
	}
found:
	// The billing-header text is plain ASCII (no JSON escapes), so byte
	// offsets within `text` map 1:1 onto the raw body buffer.
	m := billingCCHRe.FindStringSubmatchIndex(text)
	if m == nil {
		return -1, -1
	}
	return bodyBase + m[2], bodyBase + m[3]
}

// canonicalizeForCCH returns the byte sequence CLI 2.1.179 actually feeds
// into xxh64 when computing the cch attestation. Three transforms relative
// to the wire body:
//
//  1. cch=<5hex>;       → cch=00000;   (placeholder so the field hashes the
//     same regardless of its eventual value)
//  2. "model":"<X>"     → "model":""   (CLI/SDK may rewrite model on
//     fallback/retry without re-signing)
//  3. ,?"max_tokens":<N> → ""          (server can dynamically cap; not part
//     of the prompt-content fingerprint)
//
// Reverse-engineered by hooking xxh64 update at 0x2ad99c0 in the 2.1.179
// binary under gdb; 6 captured requests across 3 models and 2 entrypoints
// all verified against this rule. See testdata/cch_2179_*.bin.
//
// The result is a hashing intermediate only — it's never sent on the wire.
// Returns nil if the body has no cch field to anchor canonicalization.
func canonicalizeForCCH(body []byte) []byte {
	s, e := findBillingHeaderCCHRange(body)
	if s < 0 {
		return nil
	}
	out := make([]byte, len(body))
	copy(out, body)
	// (1) cch placeholder — byte-copy preserves length.
	copy(out[s:e], []byte("00000"))
	// (2) Clear "model" value via sjson. Skip when the field is missing or
	// already empty (sjson would still write the field, slightly changing
	// the JSON shape).
	if v := gjson.GetBytes(out, "model"); v.Exists() && v.Type == gjson.String && v.Str != "" {
		if next, err := sjson.SetBytes(out, "model", ""); err == nil {
			out = next
		}
	}
	// (3) Drop "max_tokens" entirely.
	if gjson.GetBytes(out, "max_tokens").Exists() {
		if next, err := sjson.DeleteBytes(out, "max_tokens"); err == nil {
			out = next
		}
	}
	return out
}

// recomputeCCH writes the CCH attestation that CLI 2.1.179 would produce
// for body's content into body's existing cch=XXXXX; slot. Returns body
// unchanged when no billing-header cch field is present.
//
// The hash is computed over canonicalizeForCCH(body), but the 5-hex result
// is written back into the ORIGINAL wire body — only the cch bytes change,
// the model and max_tokens fields are preserved on the wire.
func recomputeCCH(body []byte) []byte {
	s, e := findBillingHeaderCCHRange(body)
	if s < 0 {
		return body
	}
	canon := canonicalizeForCCH(body)
	if canon == nil {
		// Defensive: findBillingHeaderCCHRange already succeeded above, so
		// canonicalizeForCCH should also succeed. Bail rather than panic.
		return body
	}
	h := xxhash.NewS64(cchSeed)
	h.Write(canon)
	out := make([]byte, len(body))
	copy(out, body)
	copy(out[s:e], []byte(fmt.Sprintf("%05x", h.Sum64()&0xFFFFF)))
	return out
}

// CCHStatus describes the result of validating a client's cch attestation
// against a locally recomputed value. Used only for observability in request
// metadata; never affects request forwarding.
type CCHStatus string

const (
	CCHStatusMatch    CCHStatus = "match"
	CCHStatusMismatch CCHStatus = "mismatch"
	CCHStatusAbsent   CCHStatus = "absent"
)

// CCH algorithm tags reported by ValidateCCH. Only meaningful for
// CCHStatusMatch — empty otherwise.
const (
	cchAlgo2179   = "v2.1.179" // canonical: model cleared, max_tokens stripped
	cchAlgoLegacy = "legacy"   // pre-2.1.179: hash the body verbatim
)

// ValidateCCH compares the client-provided cch against what a genuine Claude
// Code CLI would compute. Tries the canonical 2.1.179 algorithm first; falls
// back to the legacy (no-canonicalization) algorithm to support older clients
// still in the wild. Reports which algorithm matched via algo.
//
// Returns (Absent, "", "", "") when there's no cch field at all.
// Returns (Match, client, client, "v2.1.179"|"legacy") on success.
// Returns (Mismatch, client, expected_v2.1.179, "") when neither matches —
// the "expected" value uses the canonical algorithm since that's what a
// current-version client would produce.
//
// Comparison is byte-exact (case-sensitive): a real CLI always emits
// lowercase hex, so uppercase mismatches even if the bytes match — a signal
// that the client is not the authentic CLI.
//
// Does not mutate body.
func ValidateCCH(body []byte) (status CCHStatus, client, expected, algo string) {
	s, e := findBillingHeaderCCHRange(body)
	if s < 0 {
		return CCHStatusAbsent, "", "", ""
	}
	client = string(body[s:e])

	canon := canonicalizeForCCH(body)
	hCanon := xxhash.NewS64(cchSeed)
	hCanon.Write(canon)
	expectedCanon := fmt.Sprintf("%05x", hCanon.Sum64()&0xFFFFF)
	if client == expectedCanon {
		return CCHStatusMatch, client, expectedCanon, cchAlgo2179
	}

	// Legacy fallback: hash the body verbatim (with cch=00000 placeholder
	// substituted in place). This is what pre-2.1.179 CLIs do.
	legacy := make([]byte, len(body))
	copy(legacy, body)
	copy(legacy[s:e], []byte("00000"))
	hLegacy := xxhash.NewS64(cchSeed)
	hLegacy.Write(legacy)
	expectedLegacy := fmt.Sprintf("%05x", hLegacy.Sum64()&0xFFFFF)
	if client == expectedLegacy {
		return CCHStatusMatch, client, expectedLegacy, cchAlgoLegacy
	}

	// Neither algorithm matched. Report the canonical expected value since
	// that's what a current-version client would have produced.
	return CCHStatusMismatch, client, expectedCanon, ""
}

// ValidateFingerprint checks whether the version suffix (fingerprint) in the
// x-anthropic-billing-header matches what a genuine Claude Code CLI would
// compute from the first user message.
//
// The CLI algorithm (fingerprint.ts):
//
//	chars  = msg[4] + msg[7] + msg[20]   (pad with "0" if shorter)
//	suffix = SHA256(SALT + chars + version)[:3]
//
// Returns (status, client_suffix, expected_suffix).
func ValidateFingerprint(body []byte) (status CCHStatus, client, expected string) {
	// 1. Extract cc_version value from billing header.
	sys := gjson.GetBytes(body, "system")
	if !sys.Exists() {
		return CCHStatusAbsent, "", ""
	}

	var billingText string
	if sys.IsArray() {
		for _, item := range sys.Array() {
			t := item.Get("text")
			if t.Exists() && strings.HasPrefix(t.Str, "x-anthropic-billing-header") {
				billingText = t.Str
				break
			}
		}
	} else if sys.Type == gjson.String && strings.HasPrefix(sys.Str, "x-anthropic-billing-header") {
		billingText = sys.Str
	}
	if billingText == "" {
		return CCHStatusAbsent, "", ""
	}

	// 2. Parse cc_version=X.Y.Z.suffix from the billing header.
	m := ccVersionRe.FindString(billingText)
	if m == "" {
		return CCHStatusAbsent, "", ""
	}
	// m = "cc_version=2.1.114.d69;"  →  value = "2.1.114.d69"
	value := m[len("cc_version=") : len(m)-1]

	// Split into semver and suffix at the LAST dot.
	lastDot := strings.LastIndex(value, ".")
	if lastDot < 0 || lastDot == len(value)-1 {
		return CCHStatusAbsent, "", ""
	}
	version := value[:lastDot]  // "2.1.114"
	client = value[lastDot+1:]  // "d69"

	// 3. Extract first user message text.
	firstMsg := extractFirstUserMessageText(body)

	// 4. Compute expected fingerprint.
	expected = computeFingerprint(firstMsg, version)

	if client == expected {
		return CCHStatusMatch, client, expected
	}
	return CCHStatusMismatch, client, expected
}

// extractFirstUserMessageText returns the text content that the CLI used for
// fingerprint computation.
//
// The CLI computes the fingerprint on messagesForAPI[0] — the first user
// message in its internal model — at a point where the message contains only
// the user's "primary action" content (the actual prompt, a slash-command
// invocation block like <command-name>/effort..., or a /compact resume
// summary). AFTER fingerprint, the CLI merges in late-injected reminder
// blocks (skill listing, prependUserContext claudeMd, SessionStart hook
// output, the local-command-caveat that prefaces slash-command output).
// The wire body we receive shows the post-merge form, so we have to skip
// those late-injected blocks to recover what the CLI saw at fingerprint time.
//
// The scan also crosses consecutive user messages: when /compact bubbles an
// attachment user message ("<system-reminder>Called the Read tool...") to
// messages[0] as a string, the actual /compact summary lives inside
// messages[1]'s content array — both must be visible to the scan. The scan
// stops at the first non-user message (i.e., assistant), since the first
// user-turn group is the only thing that gets merged into messagesForAPI[0].
//
// Falls back to the first text block ever seen if every candidate is a
// late-injected reminder.
func extractFirstUserMessageText(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return ""
	}
	var fallback string
	fallbackSet := false
	for _, msg := range messages.Array() {
		if msg.Get("role").Str != "user" {
			break
		}
		for _, text := range userMessageTextBlocks(msg.Get("content")) {
			if !fallbackSet {
				fallback = text
				fallbackSet = true
			}
			if !isCLIInjectedBlock(text) {
				return text
			}
		}
	}
	return fallback
}

// userMessageTextBlocks yields the text-block strings of a single user
// message's content. String content is treated as a single text block so the
// caller can apply the same skip rules uniformly.
func userMessageTextBlocks(content gjson.Result) []string {
	if content.Type == gjson.String {
		return []string{content.Str}
	}
	if !content.IsArray() {
		return nil
	}
	blocks := content.Array()
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Get("type").Str != "text" {
			continue
		}
		out = append(out, block.Get("text").Str)
	}
	return out
}

// isCLIInjectedBlock returns true if the text block was injected by the CLI
// AFTER fingerprint computation. Only these "late-injected" wrappers are
// skipped:
//
//   - <system-reminder>...: SessionStart hook output, skill listing,
//     prependUserContext claudeMd, attachment previews — all wrapped in
//     <system-reminder> by the CLI as late metadata.
//   - <local-command-caveat>...: the caveat the CLI prepends to slash-command
//     output blocks.
//
// Slash-command blocks themselves (<command-name>, <command-message>,
// <command-args>, <local-command-stdout>, <local-command-stderr>) are NOT
// treated as injected — they are part of the user's action sequence and the
// CLI USES them as the fingerprint source (verified against real wire
// traffic for /effort, /model, /compact, etc.).
func isCLIInjectedBlock(text string) bool {
	return strings.HasPrefix(text, "<system-reminder>") ||
		strings.HasPrefix(text, "<local-command-caveat>")
}

// computeFingerprint computes the 3-char hex fingerprint exactly as the CLI
// does: SHA256(SALT + msg[4] + msg[7] + msg[20] + version)[:3].
//
// Indexing must match JavaScript string indexing, which is by UTF-16 code
// unit — NOT by byte. For BMP characters (incl. CJK) this is 1 code unit
// per rune; non-BMP (emoji) is a surrogate pair. When a picked code unit is
// a lone surrogate, Node's utf8 encoding substitutes U+FFFD, which is what
// utf16.Decode produces here.
func computeFingerprint(messageText, version string) string {
	units := utf16.Encode([]rune(messageText))
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

