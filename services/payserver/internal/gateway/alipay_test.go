package gateway

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatAmount(t *testing.T) {
	tests := []struct {
		fen  int64
		want string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{100, "1.00"},
		{2000, "20.00"},
		{12345, "123.45"},
		{999, "9.99"},
	}
	for _, tt := range tests {
		got := formatAmount(tt.fen)
		if got != tt.want {
			t.Errorf("formatAmount(%d) = %q, want %q", tt.fen, got, tt.want)
		}
	}
}

func generateTestRSAKeys(t *testing.T) (privateKeyPath, publicKeyPath string) {
	t.Helper()
	dir := t.TempDir()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	privPath := filepath.Join(dir, "app_private_key.pem")
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})
	if err := os.WriteFile(privPath, privPEM, 0600); err != nil {
		t.Fatalf("write private key: %v", err)
	}

	pubPath := filepath.Join(dir, "alipay_public_key.pem")
	pubDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	})
	if err := os.WriteFile(pubPath, pubPEM, 0644); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	return privPath, pubPath
}

func TestAlipaySign(t *testing.T) {
	privPath, pubPath := generateTestRSAKeys(t)

	gw, err := NewAlipayGateway(AlipayGatewayConfig{
		AppID:               "2021000000000001",
		PrivateKeyPath:      privPath,
		AlipayPublicKeyPath: pubPath,
		NotifyURL:           "https://example.com/notify/alipay",
		ReturnURL:           "https://example.com/return",
	})
	if err != nil {
		t.Fatalf("NewAlipayGateway: %v", err)
	}

	content := "test signing content"
	sig, err := gw.sign([]byte(content))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	hashed := sha256.Sum256([]byte(content))
	err = rsa.VerifyPKCS1v15(&gw.privateKey.PublicKey, crypto.SHA256, hashed[:], sigBytes)
	if err != nil {
		t.Errorf("signature verification failed: %v", err)
	}
}

func TestAlipayPrecreate(t *testing.T) {
	privPath, pubPath := generateTestRSAKeys(t)

	// Mock Alipay gateway that returns a precreate response.
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if got := r.FormValue("method"); got != "alipay.trade.precreate" {
			t.Errorf("expected method alipay.trade.precreate, got %s", got)
		}
		if got := r.FormValue("app_id"); got != "2021000000000001" {
			t.Errorf("expected app_id 2021000000000001, got %s", got)
		}
		if !strings.Contains(r.FormValue("biz_content"), "ORDER123") {
			t.Errorf("biz_content missing order number")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"alipay_trade_precreate_response":{"code":"10000","msg":"Success","qr_code":"https://qr.alipay.com/test123"}}`)
	}))
	defer mockServer.Close()

	// Override gateway URL for test.
	origURL := alipayGatewayURL
	alipayGatewayURL = mockServer.URL
	defer func() { alipayGatewayURL = origURL }()

	gw, err := NewAlipayGateway(AlipayGatewayConfig{
		AppID:               "2021000000000001",
		PrivateKeyPath:      privPath,
		AlipayPublicKeyPath: pubPath,
		NotifyURL:           "https://example.com/notify/alipay",
	})
	if err != nil {
		t.Fatalf("NewAlipayGateway: %v", err)
	}

	result, err := gw.CreatePayment(context.Background(), &PaymentRequest{
		OutTradeNo:  "ORDER123",
		Description: "Test Product",
		Amount:      2000,
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.PaymentURL != "https://qr.alipay.com/test123" {
		t.Errorf("expected QR code URL, got %s", result.PaymentURL)
	}
}
