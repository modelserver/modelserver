package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBearerAuth(t *testing.T) {
	mw := bearerAuthMiddleware("test-api-key")
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No auth header
	req := httptest.NewRequest("POST", "/payments", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no auth: got %d, want %d", w.Code, http.StatusUnauthorized)
	}

	// Wrong token
	req = httptest.NewRequest("POST", "/payments", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want %d", w.Code, http.StatusUnauthorized)
	}

	// Correct token
	req = httptest.NewRequest("POST", "/payments", nil)
	req.Header.Set("Authorization", "Bearer test-api-key")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("correct token: got %d, want %d", w.Code, http.StatusOK)
	}
}

func TestParsePaymentRequest(t *testing.T) {
	body := map[string]interface{}{
		"order_id":     "order-001",
		"product_name": "Pro Plan",
		"channel":      "wechat",
		"currency":     "CNY",
		"amount":       2000,
		"notify_url":   "http://localhost:8081/webhook",
		"return_url":   "http://localhost/success",
	}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest("POST", "/payments", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")

	var pr paymentAPIRequest
	err := json.NewDecoder(req.Body).Decode(&pr)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pr.OrderID != "order-001" {
		t.Errorf("OrderID = %q, want %q", pr.OrderID, "order-001")
	}
	if pr.Channel != "wechat" {
		t.Errorf("Channel = %q, want %q", pr.Channel, "wechat")
	}
	if pr.Amount != 2000 {
		t.Errorf("Amount = %d, want %d", pr.Amount, 2000)
	}
}

func TestValidateReturnURL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty", "", false},
		{"https", "https://app.example.com/payment/done", false},
		{"http", "http://app.example.com/done", false},
		{"https with path and query", "https://app.example.com/done?o=abc&t=1", false},
		{"missing scheme", "//app.example.com/done", true},
		{"javascript scheme", "javascript:alert(1)", true},
		{"file scheme", "file:///etc/passwd", true},
		{"ftp scheme", "ftp://app.example.com/done", true},
		{"empty host", "https:///done", true},
		{"userinfo present", "https://attacker@app.example.com/done", true},
		{"control char triggers parse error", "https://example.com/done\x00", true},
		{"too long", "https://example.com/" + strings.Repeat("a", 2048), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReturnURL(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateReturnURL(%q) err=%v, wantErr=%v", tc.raw, err, tc.wantErr)
			}
		})
	}
}
