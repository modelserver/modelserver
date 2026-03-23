package admin

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/internal/crypto"
)

const oauthSessionCookie = "modelserver-oauth-session"
const oauthSessionTTL = 24 * time.Hour

type oauthSession struct {
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// setOAuthSessionCookie encrypts an oauthSession and writes it as a secure cookie.
func setOAuthSessionCookie(w http.ResponseWriter, encKey []byte, userID string) error {
	sess := oauthSession{
		UserID:    userID,
		ExpiresAt: time.Now().Add(oauthSessionTTL),
	}

	plaintext, err := json.Marshal(sess)
	if err != nil {
		return err
	}

	ciphertext, err := crypto.Encrypt(encKey, plaintext)
	if err != nil {
		return err
	}

	encoded := base64.URLEncoding.EncodeToString(ciphertext)

	http.SetCookie(w, &http.Cookie{
		Name:     oauthSessionCookie,
		Value:    encoded,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
		Path:     "/",
	})

	return nil
}

// getOAuthSession reads, decrypts, and validates the OAuth session cookie.
// Returns (userID, true) on success, ("", false) on any failure or expiry.
func getOAuthSession(r *http.Request, encKey []byte) (string, bool) {
	cookie, err := r.Cookie(oauthSessionCookie)
	if err != nil {
		return "", false
	}

	ciphertext, err := base64.URLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return "", false
	}

	plaintext, err := crypto.Decrypt(encKey, ciphertext)
	if err != nil {
		return "", false
	}

	var sess oauthSession
	if err := json.Unmarshal(plaintext, &sess); err != nil {
		return "", false
	}

	if time.Now().After(sess.ExpiresAt) {
		return "", false
	}

	return sess.UserID, true
}
