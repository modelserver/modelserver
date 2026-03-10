package crypto

import (
	"encoding/hex"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	key, _ := hex.DecodeString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	plaintext := []byte("sk-ant-api03-secret-key-here")

	encrypted, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	decrypted, err := Decrypt(key, encrypted)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("Decrypt = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDifferentCiphertexts(t *testing.T) {
	key, _ := hex.DecodeString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	plaintext := []byte("same input")

	enc1, _ := Encrypt(key, plaintext)
	enc2, _ := Encrypt(key, plaintext)

	if string(enc1) == string(enc2) {
		t.Error("two encryptions produced identical ciphertext")
	}
}

func TestDecryptWrongKey(t *testing.T) {
	key1, _ := hex.DecodeString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	key2, _ := hex.DecodeString("abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")

	encrypted, _ := Encrypt(key1, []byte("secret"))
	_, err := Decrypt(key2, encrypted)
	if err == nil {
		t.Error("expected error decrypting with wrong key")
	}
}
