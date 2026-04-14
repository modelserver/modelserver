package proxy

import (
	"fmt"
	"net/http"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const fgtsBeta = "fine-grained-tool-streaming-2025-05-14"

// applyFGTS checks if the anthropic-beta header contains the FGTS beta flag.
// If present, adds eager_input_streaming: true to each tool in the tools array,
// matching the behavior of Claude Code (see /root/cc/source/src/utils/api.ts:194-206).
func applyFGTS(body []byte, headers http.Header) ([]byte, error) {
	if !hasBeta(headers, fgtsBeta) {
		return body, nil
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return body, nil
	}

	var err error
	for i := range tools.Array() {
		body, err = sjson.SetBytes(body, fmt.Sprintf("tools.%d.eager_input_streaming", i), true)
		if err != nil {
			return nil, fmt.Errorf("setting eager_input_streaming on tool %d: %w", i, err)
		}
	}
	return body, nil
}

// hasBeta checks if the given beta string is present in the anthropic-beta header values.
func hasBeta(headers http.Header, beta string) bool {
	for _, v := range splitBetaHeaders(headers.Values("Anthropic-Beta")) {
		if v == beta {
			return true
		}
	}
	return false
}
