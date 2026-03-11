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
	"net/url"
	"os"
	"strings"
	"time"
)

const alipayGatewayURL = "https://openapi.alipay.com/gateway.do"

type AlipayGatewayConfig struct {
	AppID               string
	PrivateKeyPath      string
	AlipayPublicKeyPath string
	NotifyURL           string
	ReturnURL           string
}

type AlipayGateway struct {
	cfg        AlipayGatewayConfig
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
}

func NewAlipayGateway(cfg AlipayGatewayConfig) (*AlipayGateway, error) {
	privKey, err := loadPrivateKey(cfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load private key: %w", err)
	}

	pubKey, err := loadPublicKey(cfg.AlipayPublicKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load alipay public key: %w", err)
	}

	return &AlipayGateway{cfg: cfg, privateKey: privKey, publicKey: pubKey}, nil
}

func (g *AlipayGateway) Channel() string { return "alipay" }

func (g *AlipayGateway) CreatePayment(_ context.Context, req *PaymentRequest) (*PaymentResult, error) {
	bizContent := fmt.Sprintf(
		`{"out_trade_no":"%s","total_amount":"%s","subject":"%s","product_code":"FAST_INSTANT_TRADE_PAY"}`,
		req.OutTradeNo, formatAmount(req.Amount), req.Description,
	)

	params := url.Values{}
	params.Set("app_id", g.cfg.AppID)
	params.Set("method", "alipay.trade.page.pay")
	params.Set("charset", "utf-8")
	params.Set("sign_type", "RSA2")
	params.Set("timestamp", time.Now().Format("2006-01-02 15:04:05"))
	params.Set("version", "1.0")
	params.Set("notify_url", req.NotifyURL)
	params.Set("return_url", req.ReturnURL)
	params.Set("biz_content", bizContent)

	signStr := BuildSignString(params)
	sig := g.sign([]byte(signStr))
	params.Set("sign", sig)

	payURL := alipayGatewayURL + "?" + params.Encode()

	return &PaymentResult{
		TradeNo:    "",
		PaymentURL: payURL,
	}, nil
}

// sign performs SHA256WithRSA signing and returns base64-encoded signature.
func (g *AlipayGateway) sign(content []byte) string {
	hashed := sha256.Sum256(content)
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.privateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// VerifyCallback verifies an Alipay async notification signature.
func (g *AlipayGateway) VerifyCallback(params url.Values) error {
	sig := params.Get("sign")
	if sig == "" {
		return fmt.Errorf("missing sign parameter")
	}

	filtered := url.Values{}
	for k, v := range params {
		if k == "sign" || k == "sign_type" {
			continue
		}
		filtered[k] = v
	}

	signStr := BuildSignString(filtered)
	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	hashed := sha256.Sum256([]byte(signStr))
	return rsa.VerifyPKCS1v15(g.publicKey, crypto.SHA256, hashed[:], sigBytes)
}

// BuildSignString sorts params by key and joins as key=value&key=value.
func BuildSignString(params url.Values) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sortStrings(keys)

	var pairs []string
	for _, k := range keys {
		v := params.Get(k)
		if v == "" {
			continue
		}
		pairs = append(pairs, k+"="+v)
	}
	return strings.Join(pairs, "&")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// formatAmount converts fen (int64) to yuan string with 2 decimal places.
func formatAmount(fen int64) string {
	yuan := fen / 100
	cents := fen % 100
	if cents < 0 {
		cents = -cents
	}
	return fmt.Sprintf("%d.%02d", yuan, cents)
}

func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA private key")
	}
	return rsaKey, nil
}

func loadPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA public key")
	}
	return rsaKey, nil
}
