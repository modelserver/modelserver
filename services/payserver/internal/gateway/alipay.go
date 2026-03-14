package gateway

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
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
	PrivateKeyPEM       string
	AlipayPublicKeyPath string
	AlipayPublicKeyPEM  string
	NotifyURL           string
	ReturnURL           string
}

type AlipayGateway struct {
	cfg        AlipayGatewayConfig
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
}

func NewAlipayGateway(cfg AlipayGatewayConfig) (*AlipayGateway, error) {
	var privKey *rsa.PrivateKey
	var err error
	if cfg.PrivateKeyPEM != "" {
		privKey, err = parsePrivateKey([]byte(cfg.PrivateKeyPEM))
	} else {
		privKey, err = loadPrivateKey(cfg.PrivateKeyPath)
	}
	if err != nil {
		return nil, fmt.Errorf("load private key: %w", err)
	}

	var pubKey *rsa.PublicKey
	if cfg.AlipayPublicKeyPEM != "" {
		pubKey, err = parsePublicKey([]byte(cfg.AlipayPublicKeyPEM))
	} else {
		pubKey, err = loadPublicKey(cfg.AlipayPublicKeyPath)
	}
	if err != nil {
		return nil, fmt.Errorf("load alipay public key: %w", err)
	}

	return &AlipayGateway{cfg: cfg, privateKey: privKey, publicKey: pubKey}, nil
}

func (g *AlipayGateway) Channel() string { return "alipay" }

type alipayBizContent struct {
	OutTradeNo  string `json:"out_trade_no"`
	TotalAmount string `json:"total_amount"`
	Subject     string `json:"subject"`
	ProductCode string `json:"product_code"`
	QRPayMode   string `json:"qr_pay_mode"`
}

func (g *AlipayGateway) CreatePayment(_ context.Context, req *PaymentRequest) (*PaymentResult, error) {
	bc := alipayBizContent{
		OutTradeNo:  req.OutTradeNo,
		TotalAmount: formatAmount(req.Amount),
		Subject:     req.Description,
		ProductCode: "FAST_INSTANT_TRADE_PAY",
		QRPayMode:   "4",
	}
	bizContentBytes, err := json.Marshal(bc)
	if err != nil {
		return nil, fmt.Errorf("marshal biz_content: %w", err)
	}

	params := url.Values{}
	params.Set("app_id", g.cfg.AppID)
	params.Set("method", "alipay.trade.page.pay")
	params.Set("charset", "utf-8")
	params.Set("sign_type", "RSA2")
	params.Set("timestamp", time.Now().Format("2006-01-02 15:04:05"))
	params.Set("version", "1.0")
	params.Set("notify_url", g.cfg.NotifyURL)
	params.Set("return_url", g.cfg.ReturnURL)
	params.Set("biz_content", string(bizContentBytes))

	signStr := BuildSignString(params)
	sig, err := g.sign([]byte(signStr))
	if err != nil {
		return nil, fmt.Errorf("sign request: %w", err)
	}
	params.Set("sign", sig)

	payURL := alipayGatewayURL + "?" + params.Encode()

	return &PaymentResult{
		TradeNo:    "",
		PaymentURL: payURL,
	}, nil
}

// sign performs SHA256WithRSA signing and returns base64-encoded signature.
func (g *AlipayGateway) sign(content []byte) (string, error) {
	hashed := sha256.Sum256(content)
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.privateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", fmt.Errorf("rsa sign: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sig), nil
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
	return parsePrivateKey(data)
}

func parsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	data = wrapPEM(data, "PRIVATE KEY")
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
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
	return parsePublicKey(data)
}

func parsePublicKey(data []byte) (*rsa.PublicKey, error) {
	data = wrapPEM(data, "PUBLIC KEY")
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
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

// wrapPEM adds PEM headers if the data is raw base64 without -----BEGIN markers.
func wrapPEM(data []byte, label string) []byte {
	s := strings.TrimSpace(string(data))
	if strings.HasPrefix(s, "-----") {
		return data
	}
	return []byte("-----BEGIN " + label + "-----\n" + s + "\n-----END " + label + "-----\n")
}
