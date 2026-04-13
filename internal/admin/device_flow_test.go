package admin

import (
	"testing"
)

func TestGenerateDeviceCode(t *testing.T) {
	code, err := generateDeviceCode()
	if err != nil {
		t.Fatalf("generateDeviceCode: %v", err)
	}
	if len(code) != 64 {
		t.Errorf("device code length = %d, want 64", len(code))
	}
	// Verify hex-only characters.
	for _, c := range code {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("device code contains non-hex char: %c", c)
		}
	}
	// Verify uniqueness (two calls should produce different codes).
	code2, _ := generateDeviceCode()
	if code == code2 {
		t.Error("two consecutive generateDeviceCode calls produced identical codes")
	}
}

func TestGenerateUserCode(t *testing.T) {
	code, err := generateUserCode()
	if err != nil {
		t.Fatalf("generateUserCode: %v", err)
	}
	if len(code) != 8 {
		t.Errorf("user code length = %d, want 8", len(code))
	}
	// Verify all characters are in the charset.
	for _, c := range code {
		found := false
		for _, allowed := range userCodeCharset {
			if c == allowed {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("user code contains invalid char: %c", c)
		}
	}
}

func TestGenerateNonce(t *testing.T) {
	nonce, err := generateNonce()
	if err != nil {
		t.Fatalf("generateNonce: %v", err)
	}
	if len(nonce) != 32 {
		t.Errorf("nonce length = %d, want 32", len(nonce))
	}
}

func TestFormatUserCode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"BCDFGHJK", "BCDF-GHJK"},
		{"ABCDEFGH", "ABCD-EFGH"},
		{"SHORT", "SHORT"},
		{"", ""},
	}
	for _, tt := range tests {
		got := formatUserCode(tt.input)
		if got != tt.want {
			t.Errorf("formatUserCode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeUserCode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"bcdf-ghjk", "BCDFGHJK"},
		{"BCDF-GHJK", "BCDFGHJK"},
		{"bcdf ghjk", "BCDFGHJK"},
		{"  BCDF - GHJK  ", "BCDFGHJK"},
		{"", ""},
	}
	for _, tt := range tests {
		got := normalizeUserCode(tt.input)
		if got != tt.want {
			t.Errorf("normalizeUserCode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
