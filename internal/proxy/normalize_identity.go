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

	"github.com/OneOfOne/xxhash"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	ccVersionRe    = regexp.MustCompile(`cc_version=[^;]*;`)
	ccEntrypointRe = regexp.MustCompile(`cc_entrypoint=[^;]*;`)
	// cchRe scopes the cch=<5-hex>; match to the x-anthropic-billing-header
	// text block so user-message content containing a literal "cch=..." string
	// is never touched. Capture groups: $1 = prefix up to and including "cch=",
	// $2 = the 5 hex chars, $3 = the trailing ";".
	cchRe = regexp.MustCompile(`(x-anthropic-billing-header:[^"]*?\bcch=)([0-9a-fA-F]{5})(;)`)
)

const (
	cchSeed         uint64 = 0x4d659218e32a3268
	fingerprintSalt        = "59cf53e54c78"
)

const (
	fixedUserAgent      = "claude-cli/2.1.114 (external, cli)"
	fixedStainlessOS    = "Linux"
	fixedStainlessRtVer = "v24.3.0"
	fixedStainlessPkgV  = "0.81.0"
	fixedStainlessArch  = "x64"
	fixedStainlessLang  = "js"
	fixedStainlessRt    = "node"
	fixedCCVersion      = "2.1.114"
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

func recomputeCCH(body []byte) []byte {
	if !cchRe.Match(body) {
		return body
	}
	withPlaceholder := cchRe.ReplaceAll(body, []byte("${1}00000${3}"))

	h := xxhash.NewS64(cchSeed)
	h.Write(withPlaceholder)
	cchValue := fmt.Sprintf("%05x", h.Sum64()&0xFFFFF)

	return cchRe.ReplaceAll(withPlaceholder, []byte("${1}"+cchValue+"${3}"))
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
	m := cchRe.FindSubmatchIndex(body)
	if m == nil {
		return CCHStatusAbsent, "", ""
	}
	// Group 2 (indices m[4]:m[5]) holds the 5 hex chars.
	client = string(body[m[4]:m[5]])

	withPlaceholder := cchRe.ReplaceAll(body, []byte("${1}00000${3}"))
	h := xxhash.NewS64(cchSeed)
	h.Write(withPlaceholder)
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
// In the CLI's internal message model the original user input is the first
// content block, followed by attachment-injected <system-reminder> blocks.
// After API serialization the order is reversed: <system-reminder> blocks
// come first and the user's text last. The fingerprint is computed on the
// internal (pre-serialization) order — i.e. the original user input, which
// in the wire body is the first text block NOT wrapped in <system-reminder>.
// Falls back to the first text block if every block is wrapped.
func extractFirstUserMessageText(body []byte) string {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return ""
	}
	for _, msg := range messages.Array() {
		if msg.Get("role").Str != "user" {
			continue
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			return content.Str
		}
		if content.IsArray() {
			var fallback string
			for _, block := range content.Array() {
				if block.Get("type").Str != "text" {
					continue
				}
				text := block.Get("text").Str
				if fallback == "" {
					fallback = text
				}
				if !strings.HasPrefix(text, "<system-reminder>") {
					return text
				}
			}
			return fallback
		}
		break
	}
	return ""
}

// computeFingerprint computes the 3-char hex fingerprint exactly as the CLI
// does: SHA256(SALT + msg[4] + msg[7] + msg[20] + version)[:3].
func computeFingerprint(messageText, version string) string {
	indices := [3]int{4, 7, 20}
	var chars [3]byte
	for i, idx := range indices {
		if idx < len(messageText) {
			chars[i] = messageText[idx]
		} else {
			chars[i] = '0'
		}
	}
	input := fingerprintSalt + string(chars[:]) + version
	hash := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", hash[:])[:3]
}

