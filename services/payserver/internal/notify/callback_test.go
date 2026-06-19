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

func TestCallback_Send_PerCallTargetSigning(t *testing.T) {
	secret := "test-webhook-secret"

	var receivedBody []byte
	var receivedSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Webhook-Signature")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCallbackClient(5 * time.Second)
	payload := DeliveryPayload{
		OrderID: "order-123", PaymentRef: "pay-456", Status: "paid",
		PaidAmount: 2000, PaidAt: "2026-03-11T12:00:00Z",
	}

	target := CallbackTarget{URL: srv.URL, Secret: secret}
	if err := client.Send(t.Context(), target, payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var got DeliveryPayload
	if err := json.Unmarshal(receivedBody, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OrderID != "order-123" {
		t.Errorf("OrderID = %q", got.OrderID)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(receivedBody)
	expected := hex.EncodeToString(mac.Sum(nil))
	if receivedSig != expected {
		t.Errorf("signature = %q, want %q", receivedSig, expected)
	}
}

func TestCallback_Send_EmptyURLIsNoop(t *testing.T) {
	client := NewCallbackClient(5 * time.Second)
	target := CallbackTarget{URL: "", Secret: "anything"}
	err := client.Send(t.Context(), target, DeliveryPayload{OrderID: "x"})
	if err != nil {
		t.Errorf("empty URL should be no-op success, got: %v", err)
	}
}

func TestCallback_Send_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewCallbackClient(5 * time.Second)
	target := CallbackTarget{URL: srv.URL, Secret: "s"}
	err := client.Send(t.Context(), target, DeliveryPayload{OrderID: "x"})
	if err == nil {
		t.Error("expected error on 500 response")
	}
}

func TestCallback_Send_PerCallDifferentSecrets(t *testing.T) {
	var sig1, sig2 string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sig1 == "" {
			sig1 = r.Header.Get("X-Webhook-Signature")
		} else {
			sig2 = r.Header.Get("X-Webhook-Signature")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCallbackClient(5 * time.Second)
	pl := DeliveryPayload{OrderID: "x"}
	_ = client.Send(t.Context(), CallbackTarget{URL: srv.URL, Secret: "secret-a"}, pl)
	_ = client.Send(t.Context(), CallbackTarget{URL: srv.URL, Secret: "secret-b"}, pl)

	if sig1 == sig2 {
		t.Error("different secrets produced same signature — secret not used per-call")
	}
}
