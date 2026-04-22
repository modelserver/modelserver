package types

import "testing"

func TestIsValidRequestKind_KnownConstants(t *testing.T) {
	for _, k := range AllRequestKinds {
		if !IsValidRequestKind(k) {
			t.Errorf("IsValidRequestKind(%q) = false, want true", k)
		}
	}
}

func TestIsValidRequestKind_RejectsUnknown(t *testing.T) {
	for _, k := range []string{"", "anthropic_complete", "OPENAI_RESPONSES", "openai-responses"} {
		if IsValidRequestKind(k) {
			t.Errorf("IsValidRequestKind(%q) = true, want false", k)
		}
	}
}

func TestAllRequestKinds_ContainsExactlyFive(t *testing.T) {
	if got := len(AllRequestKinds); got != 5 {
		t.Errorf("len(AllRequestKinds) = %d, want 5", got)
	}
}
