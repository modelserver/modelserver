package crypto

import (
	"crypto/rand"
	"testing"
)

func TestValidateAPIKeyChecksum(t *testing.T) {
	encKey := make([]byte, 32)
	if _, err := rand.Read(encKey); err != nil {
		t.Fatal(err)
	}

	// Generate a valid key body.
	randomBytes := make([]byte, APIKeyRandomLen)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatal(err)
	}
	checksum := ComputeAPIKeyChecksum(encKey, randomBytes)
	combined := append(randomBytes, checksum...)
	keyBody := Base62Encode(combined, APIKeyBodyLen)

	if !ValidateAPIKeyChecksum(encKey, keyBody) {
		t.Fatal("expected valid checksum")
	}
}

func TestValidateAPIKeyChecksumTampered(t *testing.T) {
	encKey := make([]byte, 32)
	if _, err := rand.Read(encKey); err != nil {
		t.Fatal(err)
	}

	randomBytes := make([]byte, APIKeyRandomLen)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatal(err)
	}
	checksum := ComputeAPIKeyChecksum(encKey, randomBytes)
	combined := append(randomBytes, checksum...)
	keyBody := Base62Encode(combined, APIKeyBodyLen)

	// Tamper with one character.
	tampered := []byte(keyBody)
	if tampered[10] == 'A' {
		tampered[10] = 'B'
	} else {
		tampered[10] = 'A'
	}
	if ValidateAPIKeyChecksum(encKey, string(tampered)) {
		t.Fatal("expected invalid checksum after tampering")
	}
}

func TestValidateAPIKeyChecksumInvalidChars(t *testing.T) {
	encKey := make([]byte, 32)
	if _, err := rand.Read(encKey); err != nil {
		t.Fatal(err)
	}

	// Invalid base62 characters should return false.
	if ValidateAPIKeyChecksum(encKey, "ms-!!!invalidbase62chars!!!") {
		t.Fatal("expected false for invalid characters")
	}
}

func TestValidateAPIKeyChecksumWrongKey(t *testing.T) {
	encKey1 := make([]byte, 32)
	encKey2 := make([]byte, 32)
	if _, err := rand.Read(encKey1); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(encKey2); err != nil {
		t.Fatal(err)
	}

	randomBytes := make([]byte, APIKeyRandomLen)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatal(err)
	}
	checksum := ComputeAPIKeyChecksum(encKey1, randomBytes)
	combined := append(randomBytes, checksum...)
	keyBody := Base62Encode(combined, APIKeyBodyLen)

	// Validating with a different key should fail.
	if ValidateAPIKeyChecksum(encKey2, keyBody) {
		t.Fatal("expected invalid checksum with wrong key")
	}
}
