package billing

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
)

// HMACAuthMiddleware validates the X-Webhook-Signature header using HMAC-SHA256.
func HMACAuthMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			signature := r.Header.Get("X-Webhook-Signature")
			if signature == "" {
				http.Error(w, `{"error":{"code":"unauthorized","message":"missing webhook signature"}}`, http.StatusUnauthorized)
				return
			}

			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, `{"error":{"code":"bad_request","message":"failed to read body"}}`, http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write(body)
			expected := hex.EncodeToString(mac.Sum(nil))

			if !hmac.Equal([]byte(expected), []byte(signature)) {
				http.Error(w, `{"error":{"code":"unauthorized","message":"invalid webhook signature"}}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
