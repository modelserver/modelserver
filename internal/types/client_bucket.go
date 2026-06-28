package types

// Client bucket constants. Five-bucket projection of the existing
// ClientKind* enum, used for routing and per-client pricing.
//
// claude-code-cli, claude-desktop, codex-cli are derived from today's
// deriveClientKind output. codex-desktop is reserved for a future Codex
// desktop product; MapClientKindToBucket returns "other" for every
// current input. The bucket exists in the schema today so admin tools
// and the dashboard can name it without a follow-up migration.
const (
	ClientBucketClaudeCodeCLI = "claude-code-cli"
	ClientBucketClaudeDesktop = "claude-desktop"
	ClientBucketCodexCLI      = "codex-cli"
	ClientBucketCodexDesktop  = "codex-desktop"
	ClientBucketOther         = "other"
)

// AllClientBuckets enumerates every ClientBucket* constant.
// Used by admin input validation and the dashboard dropdown.
var AllClientBuckets = []string{
	ClientBucketClaudeCodeCLI,
	ClientBucketClaudeDesktop,
	ClientBucketCodexCLI,
	ClientBucketCodexDesktop,
	ClientBucketOther,
}

// IsValidClientBucket reports whether s is one of the five bucket values.
func IsValidClientBucket(s string) bool {
	for _, b := range AllClientBuckets {
		if b == s {
			return true
		}
	}
	return false
}

// MapClientKindToBucket projects the six-value ClientKind* enum onto the
// five-bucket axis used by routing and pricing.
//
// The codex-desktop case is intentionally absent: no current
// deriveClientKind output identifies that product. When Codex ships a
// desktop client with a recognizable signature (UA / header / body),
// add a dedicated case here. Today every codex-desktop request falls
// into "other".
func MapClientKindToBucket(kind string) string {
	switch kind {
	case ClientKindClaudeCode:
		return ClientBucketClaudeCodeCLI
	case ClientKindClaudeDesktop:
		return ClientBucketClaudeDesktop
	case ClientKindCodex:
		return ClientBucketCodexCLI
	default:
		// ClientKindOpenCode, ClientKindOpenClaw, ClientKindUnknown,
		// and any future kind not explicitly mapped above.
		return ClientBucketOther
	}
}
