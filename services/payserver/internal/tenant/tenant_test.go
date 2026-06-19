package tenant

import (
	"strings"
	"testing"
)

func TestGenerateSecret(t *testing.T) {
	s1, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	// 32 bytes -> base64 RawURL is 43 chars (no padding).
	if len(s1) != 43 {
		t.Errorf("len(secret) = %d, want 43", len(s1))
	}
	// URL-safe base64: no +/= characters.
	if strings.ContainsAny(s1, "+/=") {
		t.Errorf("secret contains non-RawURL chars: %q", s1)
	}

	// Non-determinism: two consecutive calls must differ.
	s2, _ := GenerateSecret()
	if s1 == s2 {
		t.Errorf("GenerateSecret produced equal values back-to-back")
	}
}

func TestHashAndVerifySecret_Roundtrip(t *testing.T) {
	secret := "test-secret-123"
	hash, err := HashSecret(secret)
	if err != nil {
		t.Fatalf("HashSecret: %v", err)
	}
	if hash == "" {
		t.Fatal("HashSecret returned empty hash")
	}
	if hash == secret {
		t.Fatal("HashSecret returned cleartext (no bcrypt applied)")
	}
	if !VerifySecret(hash, secret) {
		t.Error("VerifySecret rejected correct secret")
	}
	if VerifySecret(hash, "wrong-secret") {
		t.Error("VerifySecret accepted wrong secret")
	}
}

func TestVerifySecret_BadHashRejects(t *testing.T) {
	if VerifySecret("not-a-bcrypt-hash", "anything") {
		t.Error("VerifySecret accepted a non-bcrypt hash")
	}
}
