package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

const (
	fixedUserAgent      = "claude-cli/2.1.116 (external, cli)"
	fixedStainlessOS    = "Linux"
	fixedStainlessRtVer = "v24.3.0"
	fixedStainlessPkgV  = "0.81.0"
	fixedStainlessArch  = "x64"
	fixedStainlessLang  = "js"
	fixedStainlessRt    = "node"
	fixedCCVersion      = "2.1.116"
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

	var uid map[string]interface{}
	if err := json.Unmarshal([]byte(raw.Str), &uid); err != nil {
		return body
	}
	if _, ok := uid["device_id"]; !ok {
		return body
	}

	uid["device_id"] = deviceID
	uid["account_uuid"] = ""
	encoded, err := json.Marshal(uid)
	if err != nil {
		return body
	}

	result, err := sjson.SetBytes(body, "metadata.user_id", string(encoded))
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

	if sys.IsArray() {
		for i, item := range sys.Array() {
			text := item.Get("text")
			if !text.Exists() || text.Type != gjson.String {
				continue
			}
			if strings.HasPrefix(text.Str, "x-anthropic-billing-header") {
				normalized := normalizeAttributionString(text.Str)
				path := "system." + strconv.Itoa(i) + ".text"
				if result, err := sjson.SetBytes(body, path, normalized); err == nil {
					body = result
				}
				break
			}
		}
	} else if sys.Type == gjson.String {
		if strings.HasPrefix(sys.Str, "x-anthropic-billing-header") {
			normalized := normalizeAttributionString(sys.Str)
			if result, err := sjson.SetBytes(body, "system", normalized); err == nil {
				body = result
			}
		}
	}

	return body
}

func normalizeAttributionString(s string) string {
	s = ccVersionRe.ReplaceAllStringFunc(s, func(match string) string {
		parts := match[len("cc_version=") : len(match)-1]
		dotIdx := -1
		for i := len(parts) - 1; i >= 0; i-- {
			if parts[i] == '.' {
				dotIdx = i
				break
			}
		}
		if dotIdx >= 0 {
			return "cc_version=" + fixedCCVersion + parts[dotIdx:] + ";"
		}
		return "cc_version=" + fixedCCVersion + ";"
	})
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

func recomputeCCH(body []byte) []byte {
	s, e := findBillingHeaderCCHRange(body)
	if s < 0 {
		return body
	}
	out := make([]byte, len(body))
	copy(out, body)
	copy(out[s:e], []byte("00000"))

	h := xxhash.NewS64(cchSeed)
	h.Write(out)
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

// ValidateCCH computes the expected cch over the given request body and
// compares it to the client-provided cch. Returns the status plus both
// values (empty strings when absent). Does not mutate body.
//
// Comparison is byte-exact (case-sensitive): a real Claude Code CLI always
// emits lowercase hex, so uppercase is reported as mismatch — it's a signal
// the client is not the authentic CLI.
func ValidateCCH(body []byte) (status CCHStatus, client, expected string) {
	s, e := findBillingHeaderCCHRange(body)
	if s < 0 {
		return CCHStatusAbsent, "", ""
	}
	client = string(body[s:e])

	scratch := make([]byte, len(body))
	copy(scratch, body)
	copy(scratch[s:e], []byte("00000"))
	h := xxhash.NewS64(cchSeed)
	h.Write(scratch)
	expected = fmt.Sprintf("%05x", h.Sum64()&0xFFFFF)

	if client == expected {
		return CCHStatusMatch, client, expected
	}
	return CCHStatusMismatch, client, expected
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

