package notify

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
)

func generateTestKeys(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	dir := t.TempDir()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	privPath := filepath.Join(dir, "private.pem")
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})
	os.WriteFile(privPath, privPEM, 0600)

	pubPath := filepath.Join(dir, "public.pem")
	pubDER, _ := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	os.WriteFile(pubPath, pubPEM, 0644)

	return privKey, privPath, pubPath
}

func signParams(t *testing.T, privKey *rsa.PrivateKey, params url.Values) string {
	t.Helper()
	filtered := url.Values{}
	for k, v := range params {
		if k == "sign" || k == "sign_type" {
			continue
		}
		filtered[k] = v
	}
	signStr := gateway.BuildSignString(filtered)
	hashed := sha256.Sum256([]byte(signStr))
	sig, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, hashed[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func TestAlipayNotifyVerification(t *testing.T) {
	privKey, privPath, pubPath := generateTestKeys(t)

	gw, err := gateway.NewAlipayGateway(gateway.AlipayGatewayConfig{
		AppID:               "2021000000000001",
		PrivateKeyPath:      privPath,
		AlipayPublicKeyPath: pubPath,
		NotifyURL:           "https://example.com/notify",
		ReturnURL:           "https://example.com/return",
	})
	if err != nil {
		t.Fatalf("NewAlipayGateway: %v", err)
	}

	params := url.Values{
		"out_trade_no": {"ORDER-001"},
		"trade_no":     {"ALIPAY-TX-001"},
		"trade_status": {"TRADE_SUCCESS"},
		"total_amount": {"20.00"},
		"timestamp":    {time.Now().Format("2006-01-02 15:04:05")},
		"sign_type":    {"RSA2"},
	}
	params.Set("sign", signParams(t, privKey, params))

	// Test signature verification directly
	err = gw.VerifyCallback(params)
	if err != nil {
		t.Errorf("VerifyCallback failed: %v", err)
	}

	// Test with tampered data
	params.Set("total_amount", "999.99")
	err = gw.VerifyCallback(params)
	if err == nil {
		t.Error("expected VerifyCallback to fail with tampered data")
	}
}

func TestParseYuanToFen(t *testing.T) {
	tests := []struct {
		yuan string
		want int64
	}{
		{"20.00", 2000},
		{"0.01", 1},
		{"123.45", 12345},
		{"1", 100},
		{"9.99", 999},
	}
	for _, tt := range tests {
		got := parseYuanToFen(tt.yuan)
		if got != tt.want {
			t.Errorf("parseYuanToFen(%q) = %d, want %d", tt.yuan, got, tt.want)
		}
	}
}
