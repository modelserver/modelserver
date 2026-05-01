package types

import "testing"

func TestIsValidProvider_KnownConstants(t *testing.T) {
	for _, p := range AllProviders {
		if !IsValidProvider(p) {
			t.Errorf("IsValidProvider(%q) = false, want true", p)
		}
	}
}

func TestIsValidProvider_RejectsUnknown(t *testing.T) {
	for _, p := range []string{"", "bedrock", "unknown", "ANTHROPIC", "anthropic ", " openai"} {
		if IsValidProvider(p) {
			t.Errorf("IsValidProvider(%q) = true, want false", p)
		}
	}
}

func TestAllProviders_ContainsExactlyTen(t *testing.T) {
	if got := len(AllProviders); got != 10 {
		t.Errorf("len(AllProviders) = %d, want 10", got)
	}
}
