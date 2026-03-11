package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCallbackModelserver(t *testing.T) {
	secret := "test-webhook-secret"

	var receivedBody []byte
	var receivedSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Webhook-Signature")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":{"order_id":"test","status":"delivered"}}`))
	}))
	defer srv.Close()

	client := NewCallbackClient(srv.URL, secret, 5*time.Second)
	payload := DeliveryPayload{
		OrderID:    "order-123",
		PaymentRef: "pay-456",
		Status:     "paid",
		PaidAmount: 2000,
		PaidAt:     "2026-03-11T12:00:00Z",
	}

	err := client.Send(t.Context(), payload)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	var got DeliveryPayload
	if err := json.Unmarshal(receivedBody, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.OrderID != "order-123" {
		t.Errorf("OrderID = %q, want %q", got.OrderID, "order-123")
	}
	if got.PaidAmount != 2000 {
		t.Errorf("PaidAmount = %d, want %d", got.PaidAmount, 2000)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(receivedBody)
	expected := hex.EncodeToString(mac.Sum(nil))
	if receivedSig != expected {
		t.Errorf("signature = %q, want %q", receivedSig, expected)
	}
}

func TestCallbackModelserverError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewCallbackClient(srv.URL, "secret", 5*time.Second)
	err := client.Send(t.Context(), DeliveryPayload{OrderID: "test"})
	if err == nil {
		t.Error("expected error on 500 response")
	}
}
