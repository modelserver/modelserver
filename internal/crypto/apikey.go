package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
)

// apiKeyHMACLabel is the domain separator for API key checksum derivation.
var apiKeyHMACLabel = []byte("apikey-checksum")

// APIKeyRandomLen is the number of random bytes in a generated API key.
const APIKeyRandomLen = 32

// APIKeyChecksumLen is the number of HMAC bytes embedded in the key.
const APIKeyChecksumLen = 4

// APIKeyBodyLen is the length of the base62-encoded key body (after "ms-").
const APIKeyBodyLen = 49

// ComputeAPIKeyChecksum computes a truncated HMAC-SHA256 checksum for an API key's random bytes.
// Returns 4 bytes raw.
func ComputeAPIKeyChecksum(encKey, randomBytes []byte) []byte {
	mac := hmac.New(sha256.New, deriveSubkey(encKey))
	mac.Write(randomBytes)
	return mac.Sum(nil)[:APIKeyChecksumLen]
}

// ValidateAPIKeyChecksum verifies the embedded checksum of a base62-encoded API key body.
// keyBody is the part after "ms-" and must decode to exactly 36 bytes (32 random + 4 checksum).
// Returns true if the checksum is valid.
func ValidateAPIKeyChecksum(encKey []byte, keyBody string) bool {
	raw, err := Base62Decode(keyBody, APIKeyRandomLen+APIKeyChecksumLen)
	if err != nil {
		return false
	}
	randomBytes := raw[:APIKeyRandomLen]
	checksum := raw[APIKeyRandomLen:]
	expected := ComputeAPIKeyChecksum(encKey, randomBytes)
	return hmac.Equal(expected, checksum)
}

// deriveSubkey derives a purpose-specific subkey from the encryption key using HMAC.
func deriveSubkey(encKey []byte) []byte {
	mac := hmac.New(sha256.New, encKey)
	mac.Write(apiKeyHMACLabel)
	return mac.Sum(nil)
}
