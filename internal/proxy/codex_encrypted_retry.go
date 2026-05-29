package proxy

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// isInvalidEncryptedContentError reports whether an upstream Responses-API
// error body indicates that the request's reasoning.encrypted_content blob
// could not be decrypted or verified — i.e. it was produced by a different
// ChatGPT account / signing key than the one this request is now hitting.
//
// Triggered when a session migrates upstreams between turns: the prior
// turn's reasoning blob was signed by upstream A, the next turn replays it
// against upstream B (different account), and the backend refuses to
// decrypt. Causes include ops draining/removing the prior upstream, the
// session-affinity TTL expiring, a multi-replica deploy without shared
// binding storage, or a proxy restart wiping the in-memory binding map.
func isInvalidEncryptedContentError(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	return gjson.GetBytes(body, "error.code").String() == "invalid_encrypted_content"
}

// stripEncryptedReasoningContent removes every account-bound encrypted
// blob from a Responses-API request body so the request can be replayed
// against an unrelated upstream. Returns the new body and a stripped flag
// (false → no change, caller skips the retry).
//
// Two field shapes carry these blobs (see OpenAI Responses API + codex-rs
// protocol models.rs: ResponseItem::Reasoning and
// FunctionCallOutputContentItem::EncryptedContent):
//
//  1. `input[i].encrypted_content` on items where `type == "reasoning"` —
//     Option<String> on the wire, safe to delete outright.
//  2. `input[i].output[j]` items where `type == "encrypted_content"` —
//     a typed variant of FunctionCallOutputContentItem; the whole array
//     element is dropped (an empty encrypted_content variant would still
//     be rejected). Walked in reverse so deletions don't shift the
//     indices of yet-to-be-processed items.
//
// On non-array `input`, parse failure, or no matching fields, the
// original body is returned unchanged with stripped=false. The model
// loses one turn of cached internal reasoning but the request succeeds;
// subsequent turns rebuild fresh encrypted_content bound to the new
// upstream and benefit from session affinity again.
func stripEncryptedReasoningContent(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false
	}

	out := body
	stripped := false

	for i, item := range input.Array() {
		if item.Get("type").String() == "reasoning" && item.Get("encrypted_content").Exists() {
			if next, err := sjson.DeleteBytes(out, fmt.Sprintf("input.%d.encrypted_content", i)); err == nil {
				out = next
				stripped = true
			}
		}

		output := item.Get("output")
		if !output.IsArray() {
			continue
		}
		outputItems := output.Array()
		for j := len(outputItems) - 1; j >= 0; j-- {
			if outputItems[j].Get("type").String() != "encrypted_content" {
				continue
			}
			if next, err := sjson.DeleteBytes(out, fmt.Sprintf("input.%d.output.%d", i, j)); err == nil {
				out = next
				stripped = true
			}
		}
	}

	return out, stripped
}
