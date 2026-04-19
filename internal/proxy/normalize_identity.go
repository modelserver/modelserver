package proxy

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	ccVersionRe    = regexp.MustCompile(`cc_version=[^;]*;`)
	ccEntrypointRe = regexp.MustCompile(`cc_entrypoint=[^;]*;`)
)

const (
	fixedUserAgent      = "claude-cli/2.1.114 (external, cli)"
	fixedStainlessOS    = "Linux"
	fixedStainlessRtVer = "v22.14.0"
	fixedStainlessPkgV  = "0.81.0"
	fixedStainlessArch  = "x64"
	fixedStainlessLang  = "js"
	fixedStainlessRt    = "node"
	fixedCCVersion      = "2.1.114"
	fixedDeviceID       = "a01b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b"
)

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

func normalizeRequestBody(body []byte) []byte {
	body = normalizeMetadataDeviceID(body)
	body = normalizeAttributionHeader(body)
	return body
}

func normalizeMetadataDeviceID(body []byte) []byte {
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

	uid["device_id"] = fixedDeviceID
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

