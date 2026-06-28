package types

import "testing"

func TestMapClientKindToBucket(t *testing.T) {
	cases := []struct {
		kind, want string
	}{
		{ClientKindClaudeCode, ClientBucketClaudeCodeCLI},
		{ClientKindClaudeDesktop, ClientBucketClaudeDesktop},
		{ClientKindCodex, ClientBucketCodexCLI},
		{ClientKindOpenCode, ClientBucketOther},
		{ClientKindOpenClaw, ClientBucketOther},
		{ClientKindUnknown, ClientBucketOther},
		{"", ClientBucketOther},
		{"some-future-thing", ClientBucketOther},
	}
	for _, c := range cases {
		if got := MapClientKindToBucket(c.kind); got != c.want {
			t.Errorf("MapClientKindToBucket(%q) = %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestIsValidClientBucket(t *testing.T) {
	for _, b := range AllClientBuckets {
		if !IsValidClientBucket(b) {
			t.Errorf("IsValidClientBucket(%q) = false, want true", b)
		}
	}
	for _, b := range []string{"", "claude-code", "anything-else"} {
		if IsValidClientBucket(b) {
			t.Errorf("IsValidClientBucket(%q) = true, want false", b)
		}
	}
}

func TestAllClientBuckets_ContainsFive(t *testing.T) {
	if got := len(AllClientBuckets); got != 5 {
		t.Errorf("len(AllClientBuckets) = %d, want 5", got)
	}
}

func TestClientBucketCodexDesktop_ReservedReturnsOther(t *testing.T) {
	for _, k := range []string{ClientKindClaudeCode, ClientKindClaudeDesktop,
		ClientKindCodex, ClientKindOpenCode, ClientKindOpenClaw, ClientKindUnknown} {
		if got := MapClientKindToBucket(k); got == ClientBucketCodexDesktop {
			t.Errorf("ClientKind %q unexpectedly maps to codex-desktop", k)
		}
	}
}
