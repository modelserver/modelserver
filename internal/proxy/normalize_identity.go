package proxy

import (
	"bytes"
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
	cchRe          = regexp.MustCompile(`cch=[0-9a-fA-F]{5};`)
)

const cchSeed uint64 = 0x6E52736AC806831E

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
	withPlaceholder := cchRe.ReplaceAll(body, []byte("cch=00000;"))

	h := xxhash.NewS64(cchSeed)
	h.Write(withPlaceholder)
	hash := h.Sum64() & 0xFFFFF
	cchValue := fmt.Sprintf("%05x", hash)

	return bytes.Replace(withPlaceholder, []byte("cch=00000"), []byte("cch="+cchValue), 1)
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
func ValidateCCH(body []byte) (status CCHStatus, client, expected string) {
	loc := cchRe.FindIndex(body)
	if loc == nil {
		return CCHStatusAbsent, "", ""
	}
	match := body[loc[0]:loc[1]]
	client = string(match[len("cch=") : len(match)-1])

	withPlaceholder := cchRe.ReplaceAll(body, []byte("cch=00000;"))
	h := xxhash.NewS64(cchSeed)
	h.Write(withPlaceholder)
	expected = fmt.Sprintf("%05x", h.Sum64()&0xFFFFF)

	if strings.EqualFold(client, expected) {
		return CCHStatusMatch, client, expected
	}
	return CCHStatusMismatch, client, expected
}

