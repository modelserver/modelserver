// Package tenant defines the Tenant type and secret crypto helpers used by
// payserver to identify upstream callers. Tenants are the unit of
// callback-URL isolation: each tenant owns a callback_url + HMAC secret
// that payserver POSTs DeliveryPayload to after a payment succeeds.
package tenant

import (
	"crypto/rand"
	"encoding/base64"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Tenant struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	SecretHash     string    `json:"-"`
	CallbackURL    string    `json:"callback_url"`
	CallbackSecret string    `json:"-"`
	Description    string    `json:"description"`
	IsActive       bool      `json:"is_active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// GenerateSecret returns 32 bytes of cryptographic randomness encoded as
// URL-safe base64 without padding (43 chars). The cleartext is what
// callers store and send in the Authorization header; it never goes to
// the database — only its bcrypt hash does.
func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func HashSecret(secret string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

func VerifySecret(hash, secret string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret)) == nil
}
