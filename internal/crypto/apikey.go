package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// apiKeyHMACLabel is the domain separator for API key checksum derivation.
var apiKeyHMACLabel = []byte("apikey-checksum")

// APIKeyRandomLen is the number of random bytes in a generated API key.
const APIKeyRandomLen = 32

// APIKeyChecksumLen is the number of HMAC bytes embedded in the key.
const APIKeyChecksumLen = 4

// ComputeAPIKeyChecksum computes a truncated HMAC-SHA256 checksum for an API key's random bytes.
// Returns 4 bytes raw.
func ComputeAPIKeyChecksum(encKey, randomBytes []byte) []byte {
	mac := hmac.New(sha256.New, deriveSubkey(encKey))
	mac.Write(randomBytes)
	return mac.Sum(nil)[:APIKeyChecksumLen]
}

// ValidateAPIKeyChecksum verifies the embedded checksum of a base64url-encoded API key body.
// keyBody is the part after "ms-" and must decode to exactly 36 bytes (32 random + 4 checksum).
// Returns true if the checksum is valid.
func ValidateAPIKeyChecksum(encKey []byte, keyBody string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(keyBody)
	if err != nil {
		return false
	}
	if len(raw) != APIKeyRandomLen+APIKeyChecksumLen {
		return false
	}
	randomBytes := raw[:APIKeyRandomLen]
	checksum := raw[APIKeyRandomLen:]
	expected := ComputeAPIKeyChecksum(encKey, randomBytes)
	return hmac.Equal(expected, checksum)
}

// ValidateAPIKeyChecksumHex verifies the embedded checksum of a legacy hex-encoded API key body.
// keyBody is the part after "ms-", expected to be 72 hex chars (64 random hex + 8 checksum hex).
// Returns true if the checksum is valid.
func ValidateAPIKeyChecksumHex(encKey []byte, keyBody string) bool {
	if len(keyBody) != 72 {
		return false
	}
	randomHex := keyBody[:64]
	checksumHex := keyBody[64:]
	mac := hmac.New(sha256.New, deriveSubkey(encKey))
	mac.Write([]byte(randomHex))
	full := mac.Sum(nil)
	expectedHex := hexEncode(full[:APIKeyChecksumLen])
	return hmac.Equal([]byte(expectedHex), []byte(checksumHex))
}

// deriveSubkey derives a purpose-specific subkey from the encryption key using HMAC.
func deriveSubkey(encKey []byte) []byte {
	mac := hmac.New(sha256.New, encKey)
	mac.Write(apiKeyHMACLabel)
	return mac.Sum(nil)
}

func hexEncode(b []byte) string {
	const hextable = "0123456789abcdef"
	dst := make([]byte, len(b)*2)
	for i, v := range b {
		dst[i*2] = hextable[v>>4]
		dst[i*2+1] = hextable[v&0x0f]
	}
	return string(dst)
}
