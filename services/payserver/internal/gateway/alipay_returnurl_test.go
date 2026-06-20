package gateway

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

func TestAlipayCreatePayment_ReturnURLPrecedence(t *testing.T) {
	g := newTestAlipayGateway(t, "https://config-default.example/return")

	// (a) request supplies its own return_url → use it.
	res, err := g.CreatePayment(context.Background(), &PaymentRequest{
		OutTradeNo:  "order-abc",
		Description: "Pro",
		Amount:      11999,
		ReturnURL:   "https://from-request.example/done",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	must, _ := url.Parse(res.PaymentURL)
	if got := must.Query().Get("return_url"); got != "https://from-request.example/done" {
		t.Fatalf("request URL not honored: got %q", got)
	}

	// (b) request omits return_url → fall back to config.
	res, err = g.CreatePayment(context.Background(), &PaymentRequest{
		OutTradeNo:  "order-xyz",
		Description: "Pro",
		Amount:      11999,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	must, _ = url.Parse(res.PaymentURL)
	if got := must.Query().Get("return_url"); got != "https://config-default.example/return" {
		t.Fatalf("config fallback not used: got %q", got)
	}

	if !strings.Contains(res.PaymentURL, "alipay") {
		t.Fatalf("expected alipay URL, got %s", res.PaymentURL)
	}
}

// newTestAlipayGateway builds an AlipayGateway with a throwaway in-memory
// keypair, reusing generateTestRSAKeys from alipay_test.go.
func newTestAlipayGateway(t *testing.T, configReturnURL string) *AlipayGateway {
	t.Helper()
	privPath, pubPath := generateTestRSAKeys(t)
	gw, err := NewAlipayGateway(AlipayGatewayConfig{
		AppID:               "2021000000000001",
		PrivateKeyPath:      privPath,
		PublicKeyPath:       pubPath,
		NotifyURL:           "https://config-default.example/notify",
		ReturnURL:           configReturnURL,
	})
	if err != nil {
		t.Fatalf("NewAlipayGateway: %v", err)
	}
	return gw
}
