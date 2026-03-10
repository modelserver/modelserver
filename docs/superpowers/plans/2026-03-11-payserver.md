# Payserver Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an independent payment microservice (`payserver`) that translates modelserver's payment requests into WeChat Native Pay and Alipay Page Pay API calls, handles async callbacks, and pushes delivery notifications back to modelserver.

**Architecture:** Thin gateway pattern — payserver receives standardized `PaymentRequest` from modelserver over HTTP, routes to the correct payment channel (WeChat/Alipay), persists a payment record in its own PostgreSQL database, and on callback from the payment platform, verifies signatures, updates the record, and POSTs a `DeliveryPayload` back to modelserver with HMAC signing.

**Tech Stack:** Go 1.26, chi router, PostgreSQL, `wechatpay-apiv3/wechatpay-go` (WeChat), hand-written RSA2+HTTP (Alipay), `lib/pq` (Postgres driver)

**Spec:** `docs/superpowers/specs/2026-03-11-payserver-design.md`

---

## File Structure

### New files (payserver microservice)

| File | Responsibility |
|------|---------------|
| `services/payserver/go.mod` | Independent Go module |
| `services/payserver/cmd/payserver/main.go` | Entry point: load config, init DB, init gateways, start HTTP server, graceful shutdown |
| `services/payserver/config.example.yml` | Example configuration |
| `services/payserver/internal/config/config.go` | Config structs + YAML loading + env overrides |
| `services/payserver/internal/store/store.go` | DB connection, migration runner, embed migrations FS |
| `services/payserver/internal/store/migrations/001_payments.sql` | Create payments table |
| `services/payserver/internal/store/payments.go` | Payment record CRUD |
| `services/payserver/internal/gateway/gateway.go` | `Gateway` interface + `PaymentRequest` / `PaymentResult` types |
| `services/payserver/internal/gateway/wechat.go` | WeChat Native Pay via official SDK |
| `services/payserver/internal/gateway/alipay.go` | Alipay Page Pay, hand-written: RSA2 signing, URL construction, amount conversion |
| `services/payserver/internal/gateway/alipay_test.go` | Tests for Alipay signing, amount formatting, URL construction |
| `services/payserver/internal/notify/callback.go` | Unified callback to modelserver: build `DeliveryPayload`, HMAC sign, POST |
| `services/payserver/internal/notify/callback_test.go` | Tests for HMAC signing and payload construction |
| `services/payserver/internal/notify/wechat.go` | WeChat callback handler: SDK-based verify+decrypt, update DB, call callback |
| `services/payserver/internal/notify/alipay.go` | Alipay callback handler: form parse, RSA2 verify, update DB, call callback |
| `services/payserver/internal/notify/alipay_test.go` | Tests for Alipay callback signature verification |
| `services/payserver/internal/server/handler.go` | `POST /payments` handler: auth, parse, idempotency check, route to gateway, persist |
| `services/payserver/internal/server/handler_test.go` | Tests for handler: auth, channel routing, idempotency |
| `services/payserver/internal/server/routes.go` | Mount all routes on chi router |
| `services/payserver/internal/compensate/compensate.go` | Background goroutine: retry failed modelserver callbacks |
| `services/payserver/internal/compensate/compensate_test.go` | Tests for compensation retry logic |

### Modified files (modelserver)

| File | Change |
|------|--------|
| `internal/billing/client.go` | Add `Channel string` field to `PaymentRequest` |
| `internal/admin/handle_orders.go` | Accept `channel` from request body, pass to `PaymentRequest.Channel`; change currency `"USD"` → `"CNY"` |

---

## Chunk 1: Foundation — Config, Store, Migration

### Task 1: Initialize Go module and config

**Files:**
- Create: `services/payserver/go.mod`
- Create: `services/payserver/internal/config/config.go`
- Create: `services/payserver/config.example.yml`

- [ ] **Step 1: Create go.mod**

```bash
mkdir -p services/payserver
cd services/payserver && go mod init github.com/modelserver/modelserver/services/payserver
```

- [ ] **Step 2: Write config.go**

Create `services/payserver/internal/config/config.go`:

```go
package config

import (
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	DB       DBConfig       `yaml:"db"`
	Callback CallbackConfig `yaml:"callback"`
	APIKey   string         `yaml:"api_key"`
	Log      LogConfig      `yaml:"log"`
	WeChat   WeChatConfig   `yaml:"wechat"`
	Alipay   AlipayConfig   `yaml:"alipay"`
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
}

type DBConfig struct {
	URL string `yaml:"url"`
}

type CallbackConfig struct {
	ModelserverURL string        `yaml:"modelserver_url"`
	WebhookSecret  string        `yaml:"webhook_secret"`
	Timeout        time.Duration `yaml:"timeout"`
}

type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type WeChatConfig struct {
	AppID             string `yaml:"app_id"`
	MchID             string `yaml:"mch_id"`
	MchAPIv3Key       string `yaml:"mch_api_v3_key"`
	MchSerialNo       string `yaml:"mch_serial_no"`
	MchPrivateKeyPath string `yaml:"mch_private_key_path"`
	NotifyURL         string `yaml:"notify_url"`
}

type AlipayConfig struct {
	AppID               string `yaml:"app_id"`
	PrivateKeyPath      string `yaml:"private_key_path"`
	AlipayPublicKeyPath string `yaml:"alipay_public_key_path"`
	NotifyURL           string `yaml:"notify_url"`
	ReturnURL           string `yaml:"return_url"`
}

func defaults() Config {
	return Config{
		Server: ServerConfig{Addr: ":8090"},
		Callback: CallbackConfig{
			Timeout: 10 * time.Second,
		},
		Log: LogConfig{Level: "info", Format: "json"},
	}
}

func Load(r io.Reader) (*Config, error) {
	cfg := defaults()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func LoadFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Load(f)
}

func (c *Config) ApplyEnvOverrides() {
	if v := os.Getenv("PAYSERVER_DB_URL"); v != "" {
		c.DB.URL = v
	}
	if v := os.Getenv("PAYSERVER_API_KEY"); v != "" {
		c.APIKey = v
	}
	if v := os.Getenv("PAYSERVER_CALLBACK_WEBHOOK_SECRET"); v != "" {
		c.Callback.WebhookSecret = v
	}
	if v := os.Getenv("PAYSERVER_CALLBACK_MODELSERVER_URL"); v != "" {
		c.Callback.ModelserverURL = v
	}
	if v := os.Getenv("PAYSERVER_WECHAT_APP_ID"); v != "" {
		c.WeChat.AppID = v
	}
	if v := os.Getenv("PAYSERVER_WECHAT_MCH_ID"); v != "" {
		c.WeChat.MchID = v
	}
	if v := os.Getenv("PAYSERVER_WECHAT_MCH_API_V3_KEY"); v != "" {
		c.WeChat.MchAPIv3Key = v
	}
	if v := os.Getenv("PAYSERVER_WECHAT_MCH_SERIAL_NO"); v != "" {
		c.WeChat.MchSerialNo = v
	}
	if v := os.Getenv("PAYSERVER_WECHAT_MCH_PRIVATE_KEY_PATH"); v != "" {
		c.WeChat.MchPrivateKeyPath = v
	}
	if v := os.Getenv("PAYSERVER_WECHAT_NOTIFY_URL"); v != "" {
		c.WeChat.NotifyURL = v
	}
	if v := os.Getenv("PAYSERVER_ALIPAY_APP_ID"); v != "" {
		c.Alipay.AppID = v
	}
	if v := os.Getenv("PAYSERVER_ALIPAY_PRIVATE_KEY_PATH"); v != "" {
		c.Alipay.PrivateKeyPath = v
	}
	if v := os.Getenv("PAYSERVER_ALIPAY_PUBLIC_KEY_PATH"); v != "" {
		c.Alipay.AlipayPublicKeyPath = v
	}
	if v := os.Getenv("PAYSERVER_ALIPAY_NOTIFY_URL"); v != "" {
		c.Alipay.NotifyURL = v
	}
	if v := os.Getenv("PAYSERVER_ALIPAY_RETURN_URL"); v != "" {
		c.Alipay.ReturnURL = v
	}
}
```

- [ ] **Step 3: Write config.example.yml**

Create `services/payserver/config.example.yml`:

```yaml
server:
  addr: ":8090"

db:
  url: "postgres://user:password@localhost:5432/payserver?sslmode=disable"

callback:
  modelserver_url: "http://localhost:8081/api/v1/billing/webhook/delivery"
  webhook_secret: ""
  timeout: 10s

api_key: "change-me"

log:
  level: "info"
  format: "json"

wechat:
  app_id: ""
  mch_id: ""
  mch_api_v3_key: ""
  mch_serial_no: ""
  mch_private_key_path: ""
  notify_url: ""

alipay:
  app_id: ""
  private_key_path: ""
  alipay_public_key_path: ""
  notify_url: ""
  return_url: ""
```

- [ ] **Step 4: Install yaml dependency**

```bash
cd services/payserver && go get gopkg.in/yaml.v3
```

- [ ] **Step 5: Verify compilation**

```bash
cd services/payserver && go build ./internal/config/
```

Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add services/payserver/
git commit -m "feat(payserver): add go module and config loading"
```

---

### Task 2: Database store and migration

**Files:**
- Create: `services/payserver/internal/store/store.go`
- Create: `services/payserver/internal/store/migrations/001_payments.sql`
- Create: `services/payserver/internal/store/payments.go`

- [ ] **Step 1: Write migration SQL**

Create `services/payserver/internal/store/migrations/001_payments.sql`:

```sql
CREATE TABLE IF NOT EXISTS payments (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id          TEXT NOT NULL,
    channel           TEXT NOT NULL,
    trade_no          TEXT NOT NULL DEFAULT '',
    amount            BIGINT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending',
    callback_status   TEXT NOT NULL DEFAULT 'pending',
    callback_retries  INT NOT NULL DEFAULT 0,
    raw_notify        JSONB,
    paid_at           TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_order_id ON payments(order_id);
```

- [ ] **Step 2: Write store.go**

Create `services/payserver/internal/store/store.go`:

```go
package store

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"

	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

func New(databaseURL string, logger *slog.Logger) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(3)

	s := &Store{db: db, logger: logger}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) DB() *sql.DB  { return s.db }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		name := entry.Name()
		var applied bool
		if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}

		content, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := s.db.Exec(string(content)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := s.db.Exec(`INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		s.logger.Info("applied migration", "name", name)
	}
	return nil
}
```

- [ ] **Step 3: Write payments.go**

Create `services/payserver/internal/store/payments.go`:

```go
package store

import (
	"database/sql"
	"fmt"
	"time"
)

type Payment struct {
	ID               string     `json:"id"`
	OrderID          string     `json:"order_id"`
	Channel          string     `json:"channel"`
	TradeNo          string     `json:"trade_no"`
	Amount           int64      `json:"amount"`
	Status           string     `json:"status"`
	CallbackStatus   string     `json:"callback_status"`
	CallbackRetries  int        `json:"callback_retries"`
	RawNotify        *string    `json:"raw_notify,omitempty"`
	PaidAt           *time.Time `json:"paid_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func (s *Store) CreatePayment(p *Payment) error {
	return s.db.QueryRow(`
		INSERT INTO payments (order_id, channel, trade_no, amount, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, callback_status, callback_retries, created_at, updated_at`,
		p.OrderID, p.Channel, p.TradeNo, p.Amount, p.Status,
	).Scan(&p.ID, &p.CallbackStatus, &p.CallbackRetries, &p.CreatedAt, &p.UpdatedAt)
}

func (s *Store) GetPaymentByOrderID(orderID string) (*Payment, error) {
	p := &Payment{}
	err := s.db.QueryRow(`
		SELECT id, order_id, channel, trade_no, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments WHERE order_id = $1`, orderID,
	).Scan(&p.ID, &p.OrderID, &p.Channel, &p.TradeNo, &p.Amount, &p.Status,
		&p.CallbackStatus, &p.CallbackRetries, &p.RawNotify, &p.PaidAt,
		&p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get payment by order_id: %w", err)
	}
	return p, nil
}

func (s *Store) GetPaymentByID(id string) (*Payment, error) {
	p := &Payment{}
	err := s.db.QueryRow(`
		SELECT id, order_id, channel, trade_no, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments WHERE id = $1`, id,
	).Scan(&p.ID, &p.OrderID, &p.Channel, &p.TradeNo, &p.Amount, &p.Status,
		&p.CallbackStatus, &p.CallbackRetries, &p.RawNotify, &p.PaidAt,
		&p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get payment by id: %w", err)
	}
	return p, nil
}

func (s *Store) MarkPaymentPaid(orderID string, tradeNo string, rawNotify string, paidAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE payments
		SET status = 'paid', trade_no = $1, raw_notify = $2, paid_at = $3, updated_at = NOW()
		WHERE order_id = $4 AND status = 'pending'`,
		tradeNo, rawNotify, paidAt, orderID)
	return err
}

func (s *Store) MarkCallbackSuccess(orderID string) error {
	_, err := s.db.Exec(`
		UPDATE payments SET callback_status = 'success', updated_at = NOW()
		WHERE order_id = $1`, orderID)
	return err
}

func (s *Store) IncrCallbackRetries(orderID string) error {
	_, err := s.db.Exec(`
		UPDATE payments SET callback_retries = callback_retries + 1, updated_at = NOW()
		WHERE order_id = $1`, orderID)
	return err
}

func (s *Store) MarkCallbackFailed(orderID string) error {
	_, err := s.db.Exec(`
		UPDATE payments SET callback_status = 'failed', updated_at = NOW()
		WHERE order_id = $1`, orderID)
	return err
}

func (s *Store) ListPendingCallbacks(limit int) ([]Payment, error) {
	rows, err := s.db.Query(`
		SELECT id, order_id, channel, trade_no, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments
		WHERE status = 'paid' AND callback_status = 'pending' AND callback_retries < 10
		ORDER BY updated_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending callbacks: %w", err)
	}
	defer rows.Close()

	var payments []Payment
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.OrderID, &p.Channel, &p.TradeNo, &p.Amount, &p.Status,
			&p.CallbackStatus, &p.CallbackRetries, &p.RawNotify, &p.PaidAt,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan payment: %w", err)
		}
		payments = append(payments, p)
	}
	return payments, rows.Err()
}
```

- [ ] **Step 4: Install pq dependency**

```bash
cd services/payserver && go get github.com/lib/pq
```

- [ ] **Step 5: Verify compilation**

```bash
cd services/payserver && go build ./internal/store/
```

Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add services/payserver/
git commit -m "feat(payserver): add database store with payments table migration"
```

---

## Chunk 2: Gateway Interface + Alipay Implementation (Hand-Written)

### Task 3: Gateway interface

**Files:**
- Create: `services/payserver/internal/gateway/gateway.go`

- [ ] **Step 1: Write gateway.go**

Create `services/payserver/internal/gateway/gateway.go`:

```go
package gateway

import "context"

type Gateway interface {
	CreatePayment(ctx context.Context, req *PaymentRequest) (*PaymentResult, error)
	Channel() string
}

type PaymentRequest struct {
	OutTradeNo  string
	Description string
	Amount      int64
	NotifyURL   string
	ReturnURL   string
}

type PaymentResult struct {
	TradeNo    string
	PaymentURL string
}
```

- [ ] **Step 2: Verify compilation**

```bash
cd services/payserver && go build ./internal/gateway/
```

- [ ] **Step 3: Commit**

```bash
git add services/payserver/internal/gateway/gateway.go
git commit -m "feat(payserver): add gateway interface definition"
```

---

### Task 4: Alipay Page Pay gateway (hand-written)

**Files:**
- Create: `services/payserver/internal/gateway/alipay.go`
- Create: `services/payserver/internal/gateway/alipay_test.go`

- [ ] **Step 1: Write failing tests for Alipay utilities**

Create `services/payserver/internal/gateway/alipay_test.go`:

```go
package gateway

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
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

	// Write private key (PKCS1)
	privPath := filepath.Join(dir, "app_private_key.pem")
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	})
	if err := os.WriteFile(privPath, privPEM, 0600); err != nil {
		t.Fatalf("write private key: %v", err)
	}

	// Write public key
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
	sig := gw.sign([]byte(content))

	// Verify with public key
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

func TestAlipayBuildPagePayURL(t *testing.T) {
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

	result, err := gw.CreatePayment(nil, &PaymentRequest{
		OutTradeNo:  "ORDER123",
		Description: "Test Product",
		Amount:      2000,
		NotifyURL:   "https://example.com/notify/alipay",
		ReturnURL:   "https://example.com/return",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if result.PaymentURL == "" {
		t.Error("PaymentURL is empty")
	}
	// URL should contain the app_id
	if !containsSubstring(result.PaymentURL, "app_id=2021000000000001") {
		t.Errorf("PaymentURL missing app_id: %s", result.PaymentURL)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && searchSubstring(s, sub)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd services/payserver && go test ./internal/gateway/ -v -run 'TestFormatAmount|TestAlipaySign|TestAlipayBuildPagePayURL'
```

Expected: compilation errors (functions don't exist yet)

- [ ] **Step 3: Write alipay.go implementation**

Create `services/payserver/internal/gateway/alipay.go`:

```go
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

	// Build sign string: sort keys alphabetically, join with &
	signStr := buildSignString(params)
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
// params should be the parsed form values from the callback POST.
func (g *AlipayGateway) VerifyCallback(params url.Values) error {
	sig := params.Get("sign")
	if sig == "" {
		return fmt.Errorf("missing sign parameter")
	}

	// Remove sign and sign_type before building sign string
	filtered := url.Values{}
	for k, v := range params {
		if k == "sign" || k == "sign_type" {
			continue
		}
		filtered[k] = v
	}

	signStr := buildSignString(filtered)
	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	hashed := sha256.Sum256([]byte(signStr))
	return rsa.VerifyPKCS1v15(g.publicKey, crypto.SHA256, hashed[:], sigBytes)
}

// buildSignString sorts params by key and joins as key=value&key=value.
func buildSignString(params url.Values) string {
	// url.Values.Encode() sorts by key, which is what we need,
	// but it also escapes values. We need raw values for signing.
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

// sortStrings sorts a string slice in place (avoids importing sort for this single use).
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

	// Try PKCS1 first, then PKCS8
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
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd services/payserver && go test ./internal/gateway/ -v -run 'TestFormatAmount|TestAlipaySign|TestAlipayBuildPagePayURL'
```

Expected: all 3 tests PASS

- [ ] **Step 5: Commit**

```bash
git add services/payserver/internal/gateway/
git commit -m "feat(payserver): add hand-written Alipay Page Pay gateway with RSA2 signing"
```

---

## Chunk 3: WeChat Gateway + Notify Handlers

### Task 5: WeChat Native Pay gateway

**Files:**
- Create: `services/payserver/internal/gateway/wechat.go`

- [ ] **Step 1: Install wechatpay-go SDK**

```bash
cd services/payserver && go get github.com/wechatpay-apiv3/wechatpay-go
```

- [ ] **Step 2: Write wechat.go**

Create `services/payserver/internal/gateway/wechat.go`:

```go
package gateway

import (
	"context"
	"fmt"

	"github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"
)

type WeChatGatewayConfig struct {
	AppID             string
	MchID             string
	MchAPIv3Key       string
	MchSerialNo       string
	MchPrivateKeyPath string
	NotifyURL         string
}

type WeChatGateway struct {
	cfg       WeChatGatewayConfig
	nativeSvc *native.NativeApiService
}

func NewWeChatGateway(ctx context.Context, cfg WeChatGatewayConfig) (*WeChatGateway, error) {
	privKey, err := utils.LoadPrivateKeyWithPath(cfg.MchPrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load wechat private key: %w", err)
	}

	client, err := core.NewClient(ctx,
		option.WithWechatPayAutoAuthCipher(cfg.MchID, cfg.MchSerialNo, privKey, cfg.MchAPIv3Key),
	)
	if err != nil {
		return nil, fmt.Errorf("create wechat client: %w", err)
	}

	return &WeChatGateway{
		cfg:       cfg,
		nativeSvc: &native.NativeApiService{Client: client},
	}, nil
}

func (g *WeChatGateway) Channel() string { return "wechat" }

func (g *WeChatGateway) CreatePayment(ctx context.Context, req *PaymentRequest) (*PaymentResult, error) {
	resp, _, err := g.nativeSvc.Prepay(ctx, native.PrepayRequest{
		Appid:       core.String(g.cfg.AppID),
		Mchid:       core.String(g.cfg.MchID),
		Description: core.String(req.Description),
		OutTradeNo:  core.String(req.OutTradeNo),
		NotifyUrl:   core.String(req.NotifyURL),
		Amount: &native.Amount{
			Total:    core.Int64(req.Amount),
			Currency: core.String("CNY"),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("wechat prepay: %w", err)
	}

	return &PaymentResult{
		TradeNo:    "",
		PaymentURL: *resp.CodeUrl,
	}, nil
}
```

- [ ] **Step 3: Verify compilation**

```bash
cd services/payserver && go build ./internal/gateway/
```

Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add services/payserver/
git commit -m "feat(payserver): add WeChat Native Pay gateway via official SDK"
```

---

### Task 6: Modelserver callback client

**Files:**
- Create: `services/payserver/internal/notify/callback.go`
- Create: `services/payserver/internal/notify/callback_test.go`

- [ ] **Step 1: Write failing test for HMAC callback**

Create `services/payserver/internal/notify/callback_test.go`:

```go
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

	// Verify body is correct JSON
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

	// Verify HMAC signature
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd services/payserver && go test ./internal/notify/ -v -run 'TestCallbackModelserver'
```

Expected: compilation errors

- [ ] **Step 3: Write callback.go**

Create `services/payserver/internal/notify/callback.go`:

```go
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type DeliveryPayload struct {
	OrderID    string `json:"order_id"`
	PaymentRef string `json:"payment_ref"`
	Status     string `json:"status"`
	PaidAmount int64  `json:"paid_amount"`
	PaidAt     string `json:"paid_at"`
}

type CallbackClient struct {
	url        string
	secret     string
	httpClient *http.Client
}

func NewCallbackClient(url, secret string, timeout time.Duration) *CallbackClient {
	return &CallbackClient{
		url:    url,
		secret: secret,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *CallbackClient) Send(ctx context.Context, payload DeliveryPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(c.secret))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", signature)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send callback: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("modelserver returned status %d", resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd services/payserver && go test ./internal/notify/ -v -run 'TestCallbackModelserver'
```

Expected: 2 tests PASS

- [ ] **Step 5: Commit**

```bash
git add services/payserver/internal/notify/
git commit -m "feat(payserver): add modelserver callback client with HMAC signing"
```

---

### Task 7: WeChat notify handler

**Files:**
- Create: `services/payserver/internal/notify/wechat.go`

- [ ] **Step 1: Write wechat.go**

Create `services/payserver/internal/notify/wechat.go`:

```go
package notify

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"

	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

type WeChatNotifyHandler struct {
	notifyHandler *notify.Handler
	store         *store.Store
	callback      *CallbackClient
	logger        *slog.Logger
}

func NewWeChatNotifyHandler(handler *notify.Handler, st *store.Store, cb *CallbackClient, logger *slog.Logger) *WeChatNotifyHandler {
	return &WeChatNotifyHandler{
		notifyHandler: handler,
		store:         st,
		callback:      cb,
		logger:        logger,
	}
}

func (h *WeChatNotifyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var tx payments.Transaction
	_, err := h.notifyHandler.ParseNotifyRequest(r.Context(), r, &tx)
	if err != nil {
		h.logger.Error("wechat notify: parse failed", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"code": "FAIL", "message": err.Error()})
		return
	}

	orderID := *tx.OutTradeNo
	tradeNo := *tx.TransactionId
	amount := *tx.Amount.Total
	paidAt := time.Now()
	if tx.SuccessTime != nil {
		paidAt = *tx.SuccessTime
	}

	// Idempotency: check payment status
	payment, err := h.store.GetPaymentByOrderID(orderID)
	if err != nil || payment == nil {
		h.logger.Error("wechat notify: payment not found", "order_id", orderID, "error", err)
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"code": "FAIL", "message": "payment not found"})
		return
	}

	if payment.Status == "paid" && payment.CallbackStatus == "success" {
		// Already fully processed
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"code": "SUCCESS", "message": "OK"})
		return
	}

	// Phase 1: mark as paid (if not already)
	if payment.Status == "pending" {
		rawNotify, _ := json.Marshal(tx)
		if err := h.store.MarkPaymentPaid(orderID, tradeNo, string(rawNotify), paidAt); err != nil {
			h.logger.Error("wechat notify: mark paid failed", "order_id", orderID, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"code": "FAIL", "message": "internal error"})
			return
		}
	}

	// Reply success to WeChat immediately
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"code": "SUCCESS", "message": "OK"})

	// Phase 2: callback modelserver (best-effort, compensated if fails)
	payload := DeliveryPayload{
		OrderID:    orderID,
		PaymentRef: payment.ID,
		Status:     "paid",
		PaidAmount: amount,
		PaidAt:     paidAt.Format(time.RFC3339),
	}

	if err := h.callback.Send(r.Context(), payload); err != nil {
		h.logger.Warn("wechat notify: callback to modelserver failed, will retry", "order_id", orderID, "error", err)
		h.store.IncrCallbackRetries(orderID)
		return
	}
	h.store.MarkCallbackSuccess(orderID)
}
```

- [ ] **Step 2: Verify compilation**

```bash
cd services/payserver && go build ./internal/notify/
```

Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add services/payserver/internal/notify/wechat.go
git commit -m "feat(payserver): add WeChat callback notify handler"
```

---

### Task 8: Alipay notify handler

**Files:**
- Create: `services/payserver/internal/notify/alipay.go`
- Create: `services/payserver/internal/notify/alipay_test.go`

- [ ] **Step 1: Write failing test for Alipay callback verification**

Create `services/payserver/internal/notify/alipay_test.go`:

```go
package notify

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
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
	// Build sign string same way as Alipay gateway
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

func TestAlipayNotifyHandler(t *testing.T) {
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

	// Mock store: we can't use real DB here, just test signature verification
	// by verifying that the handler correctly validates the signature
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
```

- [ ] **Step 2: Export BuildSignString from alipay.go**

In `services/payserver/internal/gateway/alipay.go`, rename `buildSignString` to `BuildSignString` (exported):

Replace all occurrences of `buildSignString` with `BuildSignString` in `alipay.go`.

- [ ] **Step 3: Run test to verify it passes**

```bash
cd services/payserver && go test ./internal/notify/ -v -run 'TestAlipayNotifyHandler'
```

Expected: PASS

- [ ] **Step 4: Write alipay.go notify handler**

Create `services/payserver/internal/notify/alipay.go`:

```go
package notify

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

type AlipayNotifyHandler struct {
	gateway  *gateway.AlipayGateway
	store    *store.Store
	callback *CallbackClient
	logger   *slog.Logger
}

func NewAlipayNotifyHandler(gw *gateway.AlipayGateway, st *store.Store, cb *CallbackClient, logger *slog.Logger) *AlipayNotifyHandler {
	return &AlipayNotifyHandler{
		gateway:  gw,
		store:    st,
		callback: cb,
		logger:   logger,
	}
}

func (h *AlipayNotifyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.logger.Error("alipay notify: parse form failed", "error", err)
		http.Error(w, "fail", http.StatusBadRequest)
		return
	}

	// Verify signature
	if err := h.gateway.VerifyCallback(r.Form); err != nil {
		h.logger.Error("alipay notify: signature verification failed", "error", err)
		http.Error(w, "fail", http.StatusUnauthorized)
		return
	}

	tradeStatus := r.FormValue("trade_status")
	if tradeStatus != "TRADE_SUCCESS" && tradeStatus != "TRADE_FINISHED" {
		// Not a successful payment, just acknowledge
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
		return
	}

	orderID := r.FormValue("out_trade_no")
	tradeNo := r.FormValue("trade_no")
	totalAmountStr := r.FormValue("total_amount")
	paidAmount := parseYuanToFen(totalAmountStr)
	paidAt := time.Now()

	// Idempotency check
	payment, err := h.store.GetPaymentByOrderID(orderID)
	if err != nil || payment == nil {
		h.logger.Error("alipay notify: payment not found", "order_id", orderID, "error", err)
		http.Error(w, "fail", http.StatusNotFound)
		return
	}

	if payment.Status == "paid" && payment.CallbackStatus == "success" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
		return
	}

	// Phase 1: mark as paid
	if payment.Status == "pending" {
		rawNotify, _ := json.Marshal(r.Form)
		if err := h.store.MarkPaymentPaid(orderID, tradeNo, string(rawNotify), paidAt); err != nil {
			h.logger.Error("alipay notify: mark paid failed", "order_id", orderID, "error", err)
			http.Error(w, "fail", http.StatusInternalServerError)
			return
		}
	}

	// Reply success to Alipay immediately
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("success"))

	// Phase 2: callback modelserver
	payload := DeliveryPayload{
		OrderID:    orderID,
		PaymentRef: payment.ID,
		Status:     "paid",
		PaidAmount: paidAmount,
		PaidAt:     paidAt.Format(time.RFC3339),
	}

	if err := h.callback.Send(r.Context(), payload); err != nil {
		h.logger.Warn("alipay notify: callback to modelserver failed, will retry", "order_id", orderID, "error", err)
		h.store.IncrCallbackRetries(orderID)
		return
	}
	h.store.MarkCallbackSuccess(orderID)
}

// parseYuanToFen converts "20.00" to 2000.
func parseYuanToFen(yuan string) int64 {
	var result int64
	var decimal int64
	var decimalPlaces int
	inDecimal := false

	for _, c := range yuan {
		if c == '.' {
			inDecimal = true
			continue
		}
		if c >= '0' && c <= '9' {
			if inDecimal {
				if decimalPlaces < 2 {
					decimal = decimal*10 + int64(c-'0')
					decimalPlaces++
				}
			} else {
				result = result*10 + int64(c-'0')
			}
		}
	}
	// Pad missing decimal places
	for decimalPlaces < 2 {
		decimal *= 10
		decimalPlaces++
	}
	return result*100 + decimal
}
```

- [ ] **Step 5: Verify compilation**

```bash
cd services/payserver && go build ./internal/notify/
```

Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add services/payserver/internal/notify/ services/payserver/internal/gateway/alipay.go
git commit -m "feat(payserver): add Alipay callback notify handler with RSA2 verification"
```

---

## Chunk 4: HTTP Server, Compensation, Entry Point

### Task 9: HTTP handler and routes

**Files:**
- Create: `services/payserver/internal/server/handler.go`
- Create: `services/payserver/internal/server/handler_test.go`
- Create: `services/payserver/internal/server/routes.go`

- [ ] **Step 1: Write failing test for payment handler**

Create `services/payserver/internal/server/handler_test.go`:

```go
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd services/payserver && go test ./internal/server/ -v
```

Expected: compilation errors

- [ ] **Step 3: Write handler.go**

Create `services/payserver/internal/server/handler.go`:

```go
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

type paymentAPIRequest struct {
	OrderID     string            `json:"order_id"`
	ProductName string            `json:"product_name"`
	Channel     string            `json:"channel"`
	Currency    string            `json:"currency"`
	Amount      int64             `json:"amount"`
	NotifyURL   string            `json:"notify_url"`
	ReturnURL   string            `json:"return_url"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type paymentAPIResponse struct {
	PaymentRef string `json:"payment_ref"`
	PaymentURL string `json:"payment_url"`
	Status     string `json:"status"`
}

func handleCreatePayment(st *store.Store, gateways map[string]gateway.Gateway, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req paymentAPIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		if req.OrderID == "" || req.Channel == "" || req.Amount <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "order_id, channel, and amount are required"})
			return
		}

		gw, ok := gateways[req.Channel]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported channel: " + req.Channel})
			return
		}

		// Idempotency: check if payment already exists for this order_id
		existing, err := st.GetPaymentByOrderID(req.OrderID)
		if err != nil {
			logger.Error("check existing payment", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		if existing != nil {
			if existing.Status == "paid" {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "order already paid"})
				return
			}
			// Return existing pending payment
			writeJSON(w, http.StatusOK, paymentAPIResponse{
				PaymentRef: existing.ID,
				PaymentURL: existing.TradeNo, // We store payment_url in trade_no temporarily... no.
				Status:     "pending",
			})
			return
		}

		// Call payment gateway
		result, err := gw.CreatePayment(r.Context(), &gateway.PaymentRequest{
			OutTradeNo:  req.OrderID,
			Description: req.ProductName,
			Amount:      req.Amount,
			NotifyURL:   req.NotifyURL,
			ReturnURL:   req.ReturnURL,
		})
		if err != nil {
			logger.Error("create payment", "channel", req.Channel, "order_id", req.OrderID, "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "payment gateway error"})
			return
		}

		// Persist payment record
		payment := &store.Payment{
			OrderID: req.OrderID,
			Channel: req.Channel,
			TradeNo: result.TradeNo,
			Amount:  req.Amount,
			Status:  "pending",
		}
		if err := st.CreatePayment(payment); err != nil {
			logger.Error("persist payment", "order_id", req.OrderID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create payment record"})
			return
		}

		writeJSON(w, http.StatusOK, paymentAPIResponse{
			PaymentRef: payment.ID,
			PaymentURL: result.PaymentURL,
			Status:     "pending",
		})
	}
}

func bearerAuthMiddleware(apiKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || auth[7:] != apiKey {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
```

Note: The idempotency for existing pending payments needs a `payment_url` column. We should add this to the payments table and store model. Update migration and store:

Add `payment_url TEXT NOT NULL DEFAULT ''` column to `001_payments.sql`, add `PaymentURL string` field to `store.Payment`, update `CreatePayment` and all scan methods to include it. Then the idempotency check returns the stored `PaymentURL`.

- [ ] **Step 4: Update migration to include payment_url column**

In `services/payserver/internal/store/migrations/001_payments.sql`, add `payment_url TEXT NOT NULL DEFAULT ''` after `trade_no`.

- [ ] **Step 5: Update store/payments.go to include PaymentURL field**

Add `PaymentURL string` to `Payment` struct. Update `CreatePayment` INSERT to include `payment_url`. Update all `Scan` calls to include `&p.PaymentURL`.

- [ ] **Step 6: Update handler.go idempotency to use PaymentURL**

In `handleCreatePayment`, for the existing pending payment case:
```go
writeJSON(w, http.StatusOK, paymentAPIResponse{
    PaymentRef: existing.ID,
    PaymentURL: existing.PaymentURL,
    Status:     "pending",
})
```

And when creating the payment record, set `PaymentURL: result.PaymentURL`.

- [ ] **Step 7: Write routes.go**

Create `services/payserver/internal/server/routes.go`:

```go
package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	"github.com/modelserver/modelserver/services/payserver/internal/notify"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

type Config struct {
	APIKey          string
	Store           *store.Store
	Gateways        map[string]gateway.Gateway
	WeChatNotify    *notify.WeChatNotifyHandler
	AlipayNotify    *notify.AlipayNotifyHandler
	Logger          *slog.Logger
}

func NewRouter(cfg Config) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Authenticated endpoint for modelserver
	r.Group(func(r chi.Router) {
		r.Use(bearerAuthMiddleware(cfg.APIKey))
		r.Post("/payments", handleCreatePayment(cfg.Store, cfg.Gateways, cfg.Logger))
	})

	// Payment platform callbacks (no bearer auth, platform-native verification)
	r.Route("/notify", func(r chi.Router) {
		if cfg.WeChatNotify != nil {
			r.Post("/wechat", cfg.WeChatNotify.ServeHTTP)
		}
		if cfg.AlipayNotify != nil {
			r.Post("/alipay", cfg.AlipayNotify.ServeHTTP)
		}
	})

	return r
}
```

- [ ] **Step 8: Install chi dependency**

```bash
cd services/payserver && go get github.com/go-chi/chi/v5
```

- [ ] **Step 9: Run tests**

```bash
cd services/payserver && go test ./internal/server/ -v
```

Expected: PASS

- [ ] **Step 10: Verify full compilation**

```bash
cd services/payserver && go build ./...
```

Expected: no errors (except main.go not yet written)

- [ ] **Step 11: Commit**

```bash
git add services/payserver/
git commit -m "feat(payserver): add HTTP handler, routes, and bearer auth middleware"
```

---

### Task 10: Compensation worker

**Files:**
- Create: `services/payserver/internal/compensate/compensate.go`
- Create: `services/payserver/internal/compensate/compensate_test.go`

- [ ] **Step 1: Write failing test**

Create `services/payserver/internal/compensate/compensate_test.go`:

```go
package compensate

import (
	"testing"
	"time"
)

func TestBackoffDuration(t *testing.T) {
	tests := []struct {
		retries int
		minWait time.Duration
	}{
		{0, 30 * time.Second},
		{1, 60 * time.Second},
		{2, 120 * time.Second},
		{5, 960 * time.Second},
	}
	for _, tt := range tests {
		got := backoffDuration(tt.retries)
		if got < tt.minWait {
			t.Errorf("backoffDuration(%d) = %v, want >= %v", tt.retries, got, tt.minWait)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd services/payserver && go test ./internal/compensate/ -v
```

Expected: compilation error

- [ ] **Step 3: Write compensate.go**

Create `services/payserver/internal/compensate/compensate.go`:

```go
package compensate

import (
	"context"
	"log/slog"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/notify"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

const (
	pollInterval = 30 * time.Second
	maxRetries   = 10
	batchSize    = 20
)

type Worker struct {
	store    *store.Store
	callback *notify.CallbackClient
	logger   *slog.Logger
	stop     chan struct{}
}

func NewWorker(st *store.Store, cb *notify.CallbackClient, logger *slog.Logger) *Worker {
	return &Worker{
		store:    st,
		callback: cb,
		logger:   logger,
		stop:     make(chan struct{}),
	}
}

func (w *Worker) Start() {
	go w.run()
}

func (w *Worker) Stop() {
	close(w.stop)
}

func (w *Worker) run() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.processPending()
		}
	}
}

func (w *Worker) processPending() {
	payments, err := w.store.ListPendingCallbacks(batchSize)
	if err != nil {
		w.logger.Error("compensate: list pending", "error", err)
		return
	}

	for _, p := range payments {
		if !w.shouldRetry(p) {
			continue
		}

		if p.CallbackRetries >= maxRetries {
			w.logger.Error("compensate: max retries reached", "order_id", p.OrderID)
			w.store.MarkCallbackFailed(p.OrderID)
			continue
		}

		payload := notify.DeliveryPayload{
			OrderID:    p.OrderID,
			PaymentRef: p.ID,
			Status:     "paid",
			PaidAmount: p.Amount,
		}
		if p.PaidAt != nil {
			payload.PaidAt = p.PaidAt.Format(time.RFC3339)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := w.callback.Send(ctx, payload)
		cancel()

		if err != nil {
			w.logger.Warn("compensate: callback failed", "order_id", p.OrderID, "retries", p.CallbackRetries, "error", err)
			w.store.IncrCallbackRetries(p.OrderID)
			continue
		}

		w.logger.Info("compensate: callback succeeded", "order_id", p.OrderID)
		w.store.MarkCallbackSuccess(p.OrderID)
	}
}

func (w *Worker) shouldRetry(p store.Payment) bool {
	wait := backoffDuration(p.CallbackRetries)
	return time.Since(p.UpdatedAt) >= wait
}

// backoffDuration returns exponential backoff: 30s * 2^retries.
func backoffDuration(retries int) time.Duration {
	d := pollInterval
	for i := 0; i < retries; i++ {
		d *= 2
	}
	return d
}
```

- [ ] **Step 4: Run tests**

```bash
cd services/payserver && go test ./internal/compensate/ -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add services/payserver/internal/compensate/
git commit -m "feat(payserver): add compensation worker for failed modelserver callbacks"
```

---

### Task 11: Entry point (main.go)

**Files:**
- Create: `services/payserver/cmd/payserver/main.go`

- [ ] **Step 1: Write main.go**

Create `services/payserver/cmd/payserver/main.go`:

```go
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"

	"github.com/modelserver/modelserver/services/payserver/internal/compensate"
	"github.com/modelserver/modelserver/services/payserver/internal/config"
	"github.com/modelserver/modelserver/services/payserver/internal/gateway"
	notifyPkg "github.com/modelserver/modelserver/services/payserver/internal/notify"
	"github.com/modelserver/modelserver/services/payserver/internal/server"
	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	// Load config.
	var cfg *config.Config
	var err error
	if *configPath != "" {
		cfg, err = config.LoadFile(*configPath)
	} else if _, statErr := os.Stat("config.yml"); statErr == nil {
		cfg, err = config.LoadFile("config.yml")
	} else {
		cfg, err = config.Load(strings.NewReader(""))
	}
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	cfg.ApplyEnvOverrides()

	// Logger.
	var logLevel slog.Level
	switch cfg.Log.Level {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	var handler slog.Handler
	if cfg.Log.Format == "console" {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	} else {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})
	}
	logger := slog.New(handler).With("component", "payserver")

	// Database.
	if cfg.DB.URL == "" {
		log.Fatal("database URL is required (db.url or PAYSERVER_DB_URL)")
	}
	st, err := store.New(cfg.DB.URL, logger)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer st.Close()
	logger.Info("connected to database")

	// Callback client.
	callbackClient := notifyPkg.NewCallbackClient(
		cfg.Callback.ModelserverURL,
		cfg.Callback.WebhookSecret,
		cfg.Callback.Timeout,
	)

	// Initialize gateways.
	gateways := make(map[string]gateway.Gateway)
	var wechatNotify *notifyPkg.WeChatNotifyHandler
	var alipayNotify *notifyPkg.AlipayNotifyHandler

	ctx := context.Background()

	// WeChat gateway.
	if cfg.WeChat.AppID != "" && cfg.WeChat.MchID != "" {
		wg, err := gateway.NewWeChatGateway(ctx, gateway.WeChatGatewayConfig{
			AppID:             cfg.WeChat.AppID,
			MchID:             cfg.WeChat.MchID,
			MchAPIv3Key:       cfg.WeChat.MchAPIv3Key,
			MchSerialNo:       cfg.WeChat.MchSerialNo,
			MchPrivateKeyPath: cfg.WeChat.MchPrivateKeyPath,
			NotifyURL:         cfg.WeChat.NotifyURL,
		})
		if err != nil {
			log.Fatalf("failed to init wechat gateway: %v", err)
		}
		gateways["wechat"] = wg

		// WeChat notify handler needs the SDK's notify handler
		privKey, err := utils.LoadPrivateKeyWithPath(cfg.WeChat.MchPrivateKeyPath)
		if err != nil {
			log.Fatalf("failed to load wechat private key for notify: %v", err)
		}
		certVisitor, err := notify.NewCertificateMapWithClient(ctx,
			option.WithWechatPayAutoAuthCipher(cfg.WeChat.MchID, cfg.WeChat.MchSerialNo, privKey, cfg.WeChat.MchAPIv3Key),
		)
		if err != nil {
			log.Fatalf("failed to init wechat cert visitor: %v", err)
		}
		notifyHandler := notify.NewNotifyHandler(cfg.WeChat.MchAPIv3Key, certVisitor)
		wechatNotify = notifyPkg.NewWeChatNotifyHandler(notifyHandler, st, callbackClient, logger)
		logger.Info("wechat gateway initialized")
	}

	// Alipay gateway.
	if cfg.Alipay.AppID != "" {
		ag, err := gateway.NewAlipayGateway(gateway.AlipayGatewayConfig{
			AppID:               cfg.Alipay.AppID,
			PrivateKeyPath:      cfg.Alipay.PrivateKeyPath,
			AlipayPublicKeyPath: cfg.Alipay.AlipayPublicKeyPath,
			NotifyURL:           cfg.Alipay.NotifyURL,
			ReturnURL:           cfg.Alipay.ReturnURL,
		})
		if err != nil {
			log.Fatalf("failed to init alipay gateway: %v", err)
		}
		gateways["alipay"] = ag
		alipayNotify = notifyPkg.NewAlipayNotifyHandler(ag, st, callbackClient, logger)
		logger.Info("alipay gateway initialized")
	}

	if len(gateways) == 0 {
		logger.Warn("no payment gateways configured")
	}

	// Compensation worker.
	compWorker := compensate.NewWorker(st, callbackClient, logger)
	compWorker.Start()
	defer compWorker.Stop()

	// HTTP server.
	router := server.NewRouter(server.Config{
		APIKey:       cfg.APIKey,
		Store:        st,
		Gateways:     gateways,
		WeChatNotify: wechatNotify,
		AlipayNotify: alipayNotify,
		Logger:       logger,
	})

	srv := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: router,
	}

	// Graceful shutdown.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	logger.Info("starting payserver", "addr", cfg.Server.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
```

- [ ] **Step 2: Run go mod tidy and verify compilation**

```bash
cd services/payserver && go mod tidy && go build ./cmd/payserver/
```

Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add services/payserver/
git commit -m "feat(payserver): add main entry point with gateway initialization and graceful shutdown"
```

---

## Chunk 5: Modelserver Changes

### Task 12: Update modelserver PaymentRequest and order handler

**Files:**
- Modify: `internal/billing/client.go:6-14` — add `Channel` field
- Modify: `internal/admin/handle_orders.go:42-46` — add `Channel` to request body
- Modify: `internal/admin/handle_orders.go:148` — change currency to CNY
- Modify: `internal/admin/handle_orders.go:159` — pass channel to PaymentRequest

- [ ] **Step 1: Add Channel field to PaymentRequest**

In `internal/billing/client.go`, add `Channel` field after `ProductName`:

```go
type PaymentRequest struct {
	OrderID     string            `json:"order_id"`
	ProductName string            `json:"product_name"`
	Channel     string            `json:"channel"`
	Currency    string            `json:"currency"`
	Amount      int64             `json:"amount"`
	NotifyURL   string            `json:"notify_url"`
	ReturnURL   string            `json:"return_url"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}
```

- [ ] **Step 2: Update handleCreateOrder to accept and pass channel**

In `internal/admin/handle_orders.go`, update the request body struct:

```go
var body struct {
    PlanSlug string `json:"plan_slug"`
    Periods  int    `json:"periods"`
    Channel  string `json:"channel"`
}
```

Update the `PaymentRequest` construction to include `Channel`:

```go
payResp, err := payClient.CreatePayment(r.Context(), billing.PaymentRequest{
    OrderID:     order.ID,
    ProductName: plan.DisplayName,
    Channel:     body.Channel,
    Currency:    order.Currency,
    Amount:      order.Amount,
    NotifyURL:   billingCfg.NotifyURL,
    ReturnURL:   billingCfg.ReturnURL,
})
```

- [ ] **Step 3: Change hardcoded currency from USD to CNY**

In `internal/admin/handle_orders.go`, change:

```go
Currency: "USD",
```

to:

```go
Currency: "CNY",
```

- [ ] **Step 4: Verify modelserver compilation**

```bash
cd /root/coding/modelserver && go build ./...
```

Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add internal/billing/client.go internal/admin/handle_orders.go
git commit -m "feat: add channel field to PaymentRequest and switch currency to CNY"
```

---

### Task 13: Final integration verification

- [ ] **Step 1: Run all payserver tests**

```bash
cd services/payserver && go test ./... -v
```

Expected: all tests PASS

- [ ] **Step 2: Run modelserver tests**

```bash
cd /root/coding/modelserver && go test ./... -v
```

Expected: all tests PASS (note: modelserver's `TestMigrationsEmbed` expects 14 files — we haven't added any modelserver migrations, so this should still pass)

- [ ] **Step 3: Run go vet on both**

```bash
cd /root/coding/modelserver && go vet ./...
cd services/payserver && go vet ./...
```

Expected: no issues

- [ ] **Step 4: Final commit if any fixes needed**

```bash
git add -A
git commit -m "fix: address any issues found in integration verification"
```
