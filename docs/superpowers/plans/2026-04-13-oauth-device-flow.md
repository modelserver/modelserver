# OAuth Device Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OAuth 2.0 Device Authorization Grant (RFC 8628) support, bridging through Ory Hydra so CLI tools can obtain project-scoped tokens.

**Architecture:** modelserver manages device code lifecycle (creation, user verification, polling). When the user approves in the browser, they go through the standard Hydra login + consent flow. modelserver exchanges the resulting auth code for Hydra tokens server-side and stores them for the CLI to poll. Proxy auth middleware is unchanged — tokens are standard Hydra tokens.

**Tech Stack:** Go, PostgreSQL, Ory Hydra Admin+Public APIs, `crypto/rand`, `html/template`, AES-256-GCM encryption

**Spec:** `docs/superpowers/specs/2026-04-13-oauth-device-flow-design.md`

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `internal/store/migrations/014_device_codes.sql` | Create `device_codes` table |
| Create | `internal/store/device_codes.go` | DB CRUD for device codes |
| Create | `internal/store/device_codes_test.go` | Unit tests for store layer (mock-friendly) |
| Modify | `internal/config/config.go` | Add `PublicURL`, `DeviceFlowConfig` to `HydraConfig` |
| Modify | `internal/config/config_test.go` | Test new config fields |
| Create | `internal/admin/device_flow.go` | `DeviceFlowHandler` — all 5 HTTP endpoints |
| Create | `internal/admin/device_flow_test.go` | Unit tests for handler logic |
| Create | `internal/admin/templates/device_verify.html` | User code input page |
| Create | `internal/admin/templates/device_success.html` | "Authorization successful" page |
| Create | `internal/admin/templates/device_error.html` | Error page |
| Modify | `internal/admin/routes.go` | Mount device flow endpoints |
| Create | `scripts/register-device-flow-client.sh` | Register Hydra client for device flow |

---

### Task 1: Database Migration

**Files:**
- Create: `internal/store/migrations/014_device_codes.sql`

- [ ] **Step 1: Write migration SQL**

Create file `internal/store/migrations/014_device_codes.sql`:

```sql
CREATE TABLE device_codes (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    device_code        TEXT NOT NULL UNIQUE,
    user_code          TEXT NOT NULL UNIQUE,
    client_id          TEXT NOT NULL DEFAULT '',
    scopes             TEXT[] NOT NULL DEFAULT '{}',
    status             TEXT NOT NULL DEFAULT 'pending',
    verification_nonce TEXT NOT NULL UNIQUE,
    access_token       BYTEA,
    refresh_token      BYTEA,
    token_type         TEXT NOT NULL DEFAULT '',
    token_expires_in   INT NOT NULL DEFAULT 0,
    expires_at         TIMESTAMPTZ NOT NULL,
    poll_interval      INT NOT NULL DEFAULT 5,
    last_polled_at     TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_device_codes_user_code ON device_codes(user_code) WHERE status = 'pending';
CREATE INDEX idx_device_codes_device_code ON device_codes(device_code);
```

- [ ] **Step 2: Verify migration is embedded**

Run: `go test ./internal/store/ -run TestMigrationsEmbed -v`
Expected: PASS — the `migrationsFS` embed picks up the new file automatically.

- [ ] **Step 3: Commit**

```bash
git add internal/store/migrations/014_device_codes.sql
git commit -m "feat(device-flow): add device_codes migration"
```

---

### Task 2: Config — Add DeviceFlowConfig

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestDeviceFlowConfigDefaults(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.OAuth.Hydra.DeviceFlow.CodeTTL != 600 {
		t.Errorf("DeviceFlow.CodeTTL = %d, want 600", cfg.Auth.OAuth.Hydra.DeviceFlow.CodeTTL)
	}
	if cfg.Auth.OAuth.Hydra.DeviceFlow.PollInterval != 5 {
		t.Errorf("DeviceFlow.PollInterval = %d, want 5", cfg.Auth.OAuth.Hydra.DeviceFlow.PollInterval)
	}
}

func TestDeviceFlowConfigYAML(t *testing.T) {
	yaml := []byte(`
auth:
  oauth:
    hydra:
      admin_url: "http://hydra:4445"
      public_url: "http://hydra:4444"
      device_flow:
        client_id: "device-client"
        client_secret: "device-secret"
        code_ttl: 300
        poll_interval: 10
`)
	cfg, err := Load(yaml)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.OAuth.Hydra.PublicURL != "http://hydra:4444" {
		t.Errorf("Hydra.PublicURL = %q, want %q", cfg.Auth.OAuth.Hydra.PublicURL, "http://hydra:4444")
	}
	if cfg.Auth.OAuth.Hydra.DeviceFlow.ClientID != "device-client" {
		t.Errorf("DeviceFlow.ClientID = %q, want %q", cfg.Auth.OAuth.Hydra.DeviceFlow.ClientID, "device-client")
	}
	if cfg.Auth.OAuth.Hydra.DeviceFlow.ClientSecret != "device-secret" {
		t.Errorf("DeviceFlow.ClientSecret = %q, want %q", cfg.Auth.OAuth.Hydra.DeviceFlow.ClientSecret, "device-secret")
	}
	if cfg.Auth.OAuth.Hydra.DeviceFlow.CodeTTL != 300 {
		t.Errorf("DeviceFlow.CodeTTL = %d, want 300", cfg.Auth.OAuth.Hydra.DeviceFlow.CodeTTL)
	}
	if cfg.Auth.OAuth.Hydra.DeviceFlow.PollInterval != 10 {
		t.Errorf("DeviceFlow.PollInterval = %d, want 10", cfg.Auth.OAuth.Hydra.DeviceFlow.PollInterval)
	}
}

func TestDeviceFlowConfigEnv(t *testing.T) {
	t.Setenv("HYDRA_PUBLIC_URL", "http://hydra-env:4444")
	t.Setenv("MODELSERVER_AUTH_OAUTH_HYDRA_DEVICE_FLOW_CLIENT_ID", "env-client")
	t.Setenv("MODELSERVER_AUTH_OAUTH_HYDRA_DEVICE_FLOW_CLIENT_SECRET", "env-secret")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.OAuth.Hydra.PublicURL != "http://hydra-env:4444" {
		t.Errorf("Hydra.PublicURL = %q, want %q", cfg.Auth.OAuth.Hydra.PublicURL, "http://hydra-env:4444")
	}
	if cfg.Auth.OAuth.Hydra.DeviceFlow.ClientID != "env-client" {
		t.Errorf("DeviceFlow.ClientID = %q, want %q", cfg.Auth.OAuth.Hydra.DeviceFlow.ClientID, "env-client")
	}
	if cfg.Auth.OAuth.Hydra.DeviceFlow.ClientSecret != "env-secret" {
		t.Errorf("DeviceFlow.ClientSecret = %q, want %q", cfg.Auth.OAuth.Hydra.DeviceFlow.ClientSecret, "env-secret")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestDeviceFlow -v`
Expected: FAIL — `DeviceFlowConfig` type and fields don't exist yet.

- [ ] **Step 3: Update HydraConfig in config.go**

In `internal/config/config.go`, replace the existing `HydraConfig` struct:

```go
// HydraConfig holds settings for an Ory Hydra OAuth2 server.
type HydraConfig struct {
	AdminURL   string           `yaml:"admin_url"   mapstructure:"admin_url"`
	PublicURL  string           `yaml:"public_url"  mapstructure:"public_url"`
	DeviceFlow DeviceFlowConfig `yaml:"device_flow" mapstructure:"device_flow"`
}

// DeviceFlowConfig holds settings for the OAuth 2.0 Device Authorization Grant (RFC 8628).
type DeviceFlowConfig struct {
	ClientID     string `yaml:"client_id"      mapstructure:"client_id"`
	ClientSecret string `yaml:"client_secret"  mapstructure:"client_secret"`
	CodeTTL      int    `yaml:"code_ttl"       mapstructure:"code_ttl"`
	PollInterval int    `yaml:"poll_interval"  mapstructure:"poll_interval"`
}
```

- [ ] **Step 4: Add defaults and env bindings in setDefaults**

In `internal/config/config.go`, add to the `setDefaults` function, after the existing `_ = v.BindEnv("auth.oauth.hydra.admin_url", "HYDRA_ADMIN_URL")` line:

```go
	_ = v.BindEnv("auth.oauth.hydra.public_url", "HYDRA_PUBLIC_URL")
	_ = v.BindEnv("auth.oauth.hydra.device_flow.client_id")
	_ = v.BindEnv("auth.oauth.hydra.device_flow.client_secret")
	v.SetDefault("auth.oauth.hydra.device_flow.code_ttl", 600)
	v.SetDefault("auth.oauth.hydra.device_flow.poll_interval", 5)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/ -run TestDeviceFlow -v`
Expected: PASS — all three tests pass.

- [ ] **Step 6: Run full config test suite to check for regressions**

Run: `go test ./internal/config/ -v`
Expected: All tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(device-flow): add DeviceFlowConfig to HydraConfig"
```

---

### Task 3: Store Layer — Device Code CRUD

**Files:**
- Create: `internal/store/device_codes.go`

- [ ] **Step 1: Create device_codes.go**

Create file `internal/store/device_codes.go`:

```go
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DeviceCode represents a row in the device_codes table.
type DeviceCode struct {
	ID                string
	DeviceCode        string
	UserCode          string
	ClientID          string
	Scopes            []string
	Status            string // pending, approved, denied, expired
	VerificationNonce string
	AccessToken       []byte // encrypted
	RefreshToken      []byte // encrypted
	TokenType         string
	TokenExpiresIn    int
	ExpiresAt         time.Time
	PollInterval      int
	LastPolledAt      *time.Time
	CreatedAt         time.Time
}

// CreateDeviceCode inserts a new device code record.
func (s *Store) CreateDeviceCode(ctx context.Context, dc *DeviceCode) error {
	return s.pool.QueryRow(ctx, `
		INSERT INTO device_codes (device_code, user_code, client_id, scopes, verification_nonce, expires_at, poll_interval)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at`,
		dc.DeviceCode, dc.UserCode, dc.ClientID, dc.Scopes, dc.VerificationNonce, dc.ExpiresAt, dc.PollInterval,
	).Scan(&dc.ID, &dc.CreatedAt)
}

// GetDeviceCodeByUserCode returns the pending device code matching the user code.
// Returns nil, nil if not found or not pending.
func (s *Store) GetDeviceCodeByUserCode(ctx context.Context, userCode string) (*DeviceCode, error) {
	dc := &DeviceCode{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, device_code, user_code, client_id, scopes, status,
			verification_nonce, expires_at, poll_interval, created_at
		FROM device_codes
		WHERE user_code = $1 AND status = 'pending' AND expires_at > NOW()`,
		userCode,
	).Scan(&dc.ID, &dc.DeviceCode, &dc.UserCode, &dc.ClientID, &dc.Scopes, &dc.Status,
		&dc.VerificationNonce, &dc.ExpiresAt, &dc.PollInterval, &dc.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code by user code: %w", err)
	}
	return dc, nil
}

// GetDeviceCodeByNonce returns the device code matching the verification nonce.
// Returns nil, nil if not found.
func (s *Store) GetDeviceCodeByNonce(ctx context.Context, nonce string) (*DeviceCode, error) {
	dc := &DeviceCode{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, device_code, user_code, client_id, scopes, status,
			verification_nonce, expires_at, poll_interval, created_at
		FROM device_codes
		WHERE verification_nonce = $1`,
		nonce,
	).Scan(&dc.ID, &dc.DeviceCode, &dc.UserCode, &dc.ClientID, &dc.Scopes, &dc.Status,
		&dc.VerificationNonce, &dc.ExpiresAt, &dc.PollInterval, &dc.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code by nonce: %w", err)
	}
	return dc, nil
}

// GetDeviceCodeByCode returns the device code matching the device code string.
// Returns nil, nil if not found.
func (s *Store) GetDeviceCodeByCode(ctx context.Context, deviceCode string) (*DeviceCode, error) {
	dc := &DeviceCode{}
	var lastPolledAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT id, device_code, user_code, client_id, scopes, status,
			verification_nonce, access_token, refresh_token, token_type, token_expires_in,
			expires_at, poll_interval, last_polled_at, created_at
		FROM device_codes
		WHERE device_code = $1`,
		deviceCode,
	).Scan(&dc.ID, &dc.DeviceCode, &dc.UserCode, &dc.ClientID, &dc.Scopes, &dc.Status,
		&dc.VerificationNonce, &dc.AccessToken, &dc.RefreshToken, &dc.TokenType, &dc.TokenExpiresIn,
		&dc.ExpiresAt, &dc.PollInterval, &lastPolledAt, &dc.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get device code by code: %w", err)
	}
	dc.LastPolledAt = lastPolledAt
	return dc, nil
}

// ApproveDeviceCode sets the device code status to approved and stores the encrypted tokens.
func (s *Store) ApproveDeviceCode(ctx context.Context, id string, accessToken, refreshToken []byte, tokenType string, tokenExpiresIn int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE device_codes
		SET status = 'approved', access_token = $2, refresh_token = $3,
			token_type = $4, token_expires_in = $5
		WHERE id = $1`,
		id, accessToken, refreshToken, tokenType, tokenExpiresIn)
	return err
}

// DenyDeviceCode sets the device code status to denied.
func (s *Store) DenyDeviceCode(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE device_codes SET status = 'denied' WHERE id = $1`, id)
	return err
}

// UpdateDeviceCodePoll updates last_polled_at and optionally increments poll_interval (for slow_down).
func (s *Store) UpdateDeviceCodePoll(ctx context.Context, id string, slowDown bool) error {
	if slowDown {
		_, err := s.pool.Exec(ctx, `
			UPDATE device_codes SET last_polled_at = NOW(), poll_interval = poll_interval + 5 WHERE id = $1`, id)
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE device_codes SET last_polled_at = NOW() WHERE id = $1`, id)
	return err
}

// DeleteDeviceCode removes a device code record by ID.
func (s *Store) DeleteDeviceCode(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM device_codes WHERE id = $1`, id)
	return err
}

// DeleteExpiredDeviceCodes removes all device codes that have expired.
func (s *Store) DeleteExpiredDeviceCodes(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM device_codes WHERE expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/store/`
Expected: Build succeeds.

- [ ] **Step 3: Commit**

```bash
git add internal/store/device_codes.go
git commit -m "feat(device-flow): add device_codes store CRUD"
```

---

### Task 4: HTML Templates

**Files:**
- Create: `internal/admin/templates/device_verify.html`
- Create: `internal/admin/templates/device_success.html`
- Create: `internal/admin/templates/device_error.html`

- [ ] **Step 1: Create device_verify.html**

Create file `internal/admin/templates/device_verify.html`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Device Authorization</title>
  <style>
    *, *::before, *::after { box-sizing: border-box; }
    body {
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: #f5f5f5;
      margin: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
    }
    .card {
      background: #fff;
      border-radius: 8px;
      box-shadow: 0 2px 8px rgba(0,0,0,0.12);
      padding: 2rem;
      width: 100%;
      max-width: 400px;
    }
    h1 {
      font-size: 1.4rem;
      font-weight: 600;
      margin: 0 0 0.5rem;
      color: #111;
    }
    p.subtitle {
      color: #555;
      font-size: 0.95rem;
      margin: 0 0 1.5rem;
    }
    .error {
      background: #fff0f0;
      border: 1px solid #f5c6c6;
      border-radius: 6px;
      color: #c0392b;
      font-size: 0.9rem;
      margin-bottom: 1.25rem;
      padding: 0.75rem 1rem;
    }
    .code-input {
      width: 100%;
      padding: 0.75rem 1rem;
      border: 1px solid #ddd;
      border-radius: 6px;
      font-size: 1.4rem;
      font-family: monospace;
      letter-spacing: 0.15em;
      text-align: center;
      text-transform: uppercase;
      margin-bottom: 1rem;
    }
    .code-input:focus {
      outline: none;
      border-color: #0066cc;
      box-shadow: 0 0 0 2px rgba(0,102,204,0.2);
    }
    .submit-btn {
      display: block;
      width: 100%;
      padding: 0.7rem 1rem;
      border: none;
      border-radius: 6px;
      background: #0066cc;
      color: #fff;
      font-size: 0.95rem;
      font-weight: 500;
      cursor: pointer;
      transition: background 0.15s;
    }
    .submit-btn:hover { background: #0052a3; }
    @media (prefers-color-scheme: dark) {
      body { background: #1a1a1a; }
      .card { background: #2a2a2a; box-shadow: 0 2px 8px rgba(0,0,0,0.4); }
      h1 { color: #f0f0f0; }
      p.subtitle { color: #aaa; }
      .error { background: #3a1515; border-color: #6b2a2a; color: #f5a0a0; }
      .code-input { background: #333; color: #f0f0f0; border-color: #555; }
      .code-input:focus { border-color: #6bb5ff; box-shadow: 0 0 0 2px rgba(107,181,255,0.2); }
    }
  </style>
</head>
<body>
  <div class="card">
    <h1>Device Authorization</h1>
    <p class="subtitle">Enter the code displayed on your device to continue.</p>

    {{if .Error}}
    <div class="error">{{.Error}}</div>
    {{end}}

    <form method="POST" action="/oauth/device">
      <input type="text" name="user_code" class="code-input"
             placeholder="XXXX-XXXX" maxlength="9"
             value="{{.UserCode}}" autocomplete="off" autofocus>
      <button type="submit" class="submit-btn">Continue</button>
    </form>
  </div>
</body>
</html>
```

- [ ] **Step 2: Create device_success.html**

Create file `internal/admin/templates/device_success.html`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Authorization Successful</title>
  <style>
    *, *::before, *::after { box-sizing: border-box; }
    body {
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: #f5f5f5;
      margin: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
    }
    .card {
      background: #fff;
      border-radius: 8px;
      box-shadow: 0 2px 8px rgba(0,0,0,0.12);
      padding: 2rem;
      width: 100%;
      max-width: 400px;
      text-align: center;
    }
    .icon { font-size: 3rem; margin-bottom: 1rem; }
    h1 { font-size: 1.4rem; font-weight: 600; margin: 0 0 0.5rem; color: #111; }
    p { color: #555; font-size: 0.95rem; margin: 0; }
    @media (prefers-color-scheme: dark) {
      body { background: #1a1a1a; }
      .card { background: #2a2a2a; box-shadow: 0 2px 8px rgba(0,0,0,0.4); }
      h1 { color: #f0f0f0; }
      p { color: #aaa; }
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="icon">&#x2705;</div>
    <h1>Authorization Successful</h1>
    <p>Your device has been authorized. You may close this window.</p>
  </div>
</body>
</html>
```

- [ ] **Step 3: Create device_error.html**

Create file `internal/admin/templates/device_error.html`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Authorization Failed</title>
  <style>
    *, *::before, *::after { box-sizing: border-box; }
    body {
      font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: #f5f5f5;
      margin: 0;
      display: flex;
      align-items: center;
      justify-content: center;
      min-height: 100vh;
    }
    .card {
      background: #fff;
      border-radius: 8px;
      box-shadow: 0 2px 8px rgba(0,0,0,0.12);
      padding: 2rem;
      width: 100%;
      max-width: 400px;
      text-align: center;
    }
    .icon { font-size: 3rem; margin-bottom: 1rem; }
    h1 { font-size: 1.4rem; font-weight: 600; margin: 0 0 0.5rem; color: #111; }
    p { color: #555; font-size: 0.95rem; margin: 0 0 1rem; }
    a { color: #0066cc; text-decoration: none; }
    a:hover { text-decoration: underline; }
    @media (prefers-color-scheme: dark) {
      body { background: #1a1a1a; }
      .card { background: #2a2a2a; box-shadow: 0 2px 8px rgba(0,0,0,0.4); }
      h1 { color: #f0f0f0; }
      p { color: #aaa; }
      a { color: #6bb5ff; }
    }
  </style>
</head>
<body>
  <div class="card">
    <div class="icon">&#x274C;</div>
    <h1>Authorization Failed</h1>
    <p>{{.Error}}</p>
    <a href="/oauth/device">Try again</a>
  </div>
</body>
</html>
```

- [ ] **Step 4: Commit**

```bash
git add internal/admin/templates/device_verify.html internal/admin/templates/device_success.html internal/admin/templates/device_error.html
git commit -m "feat(device-flow): add HTML templates for device verification pages"
```

---

### Task 5: DeviceFlowHandler — Core Logic

**Files:**
- Create: `internal/admin/device_flow.go`

This is the main implementation file with all 5 HTTP endpoints. It depends on the store, config, and templates from prior tasks.

- [ ] **Step 1: Create device_flow.go**

Create file `internal/admin/device_flow.go`:

```go
package admin

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/store"
)

//go:embed templates/device_verify.html templates/device_success.html templates/device_error.html
var deviceTemplateFS embed.FS

// userCodeCharset contains only consonants with ambiguous characters removed.
const userCodeCharset = "BCDFGHJKLMNPQRSTVWXZ"

// DeviceFlowHandler handles OAuth 2.0 Device Authorization Grant (RFC 8628) endpoints.
type DeviceFlowHandler struct {
	hydra     *HydraClient
	store     *store.Store
	encKey    []byte
	cfg       *config.Config
	templates *template.Template
}

// deviceVerifyData is passed to the device_verify.html template.
type deviceVerifyData struct {
	UserCode string
	Error    string
}

// deviceErrorData is passed to the device_error.html template.
type deviceErrorData struct {
	Error string
}

// NewDeviceFlowHandler constructs a DeviceFlowHandler and parses templates.
func NewDeviceFlowHandler(hydra *HydraClient, st *store.Store, encKey []byte, cfg *config.Config) (*DeviceFlowHandler, error) {
	tmpl, err := template.ParseFS(deviceTemplateFS,
		"templates/device_verify.html",
		"templates/device_success.html",
		"templates/device_error.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse device flow templates: %w", err)
	}
	return &DeviceFlowHandler{
		hydra:     hydra,
		store:     st,
		encKey:    encKey,
		cfg:       cfg,
		templates: tmpl,
	}, nil
}

// HandleDeviceAuthorize handles POST /oauth/device/code.
// Creates a new device_code + user_code pair and returns them per RFC 8628.
func (h *DeviceFlowHandler) HandleDeviceAuthorize(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID string `json:"client_id"`
		Scope    string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	scopes := strings.Fields(body.Scope)
	if len(scopes) == 0 {
		scopes = []string{"project:inference", "offline_access"}
	}

	deviceCode, err := generateDeviceCode()
	if err != nil {
		log.Printf("ERROR device_flow: generate device code: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}

	userCode, err := generateUserCode()
	if err != nil {
		log.Printf("ERROR device_flow: generate user code: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}

	nonce, err := generateNonce()
	if err != nil {
		log.Printf("ERROR device_flow: generate nonce: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}

	codeTTL := h.cfg.Auth.OAuth.Hydra.DeviceFlow.CodeTTL
	if codeTTL <= 0 {
		codeTTL = 600
	}
	pollInterval := h.cfg.Auth.OAuth.Hydra.DeviceFlow.PollInterval
	if pollInterval <= 0 {
		pollInterval = 5
	}

	dc := &store.DeviceCode{
		DeviceCode:        deviceCode,
		UserCode:          userCode,
		ClientID:          body.ClientID,
		Scopes:            scopes,
		VerificationNonce: nonce,
		ExpiresAt:         time.Now().Add(time.Duration(codeTTL) * time.Second),
		PollInterval:      pollInterval,
	}

	if err := h.store.CreateDeviceCode(r.Context(), dc); err != nil {
		log.Printf("ERROR device_flow: create device code: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}

	verificationURI := baseURL(r) + "/oauth/device"
	formattedCode := formatUserCode(userCode)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"device_code":               deviceCode,
		"user_code":                 formattedCode,
		"verification_uri":          verificationURI,
		"verification_uri_complete": verificationURI + "?user_code=" + url.QueryEscape(formattedCode),
		"expires_in":                codeTTL,
		"interval":                  pollInterval,
	})
}

// HandleVerificationPage handles GET /oauth/device.
// Renders the user code input form.
func (h *DeviceFlowHandler) HandleVerificationPage(w http.ResponseWriter, r *http.Request) {
	userCode := r.URL.Query().Get("user_code")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.templates.ExecuteTemplate(w, "device_verify.html", deviceVerifyData{
		UserCode: userCode,
	})
}

// HandleVerifyUserCode handles POST /oauth/device.
// Validates the user code and redirects to Hydra's authorization endpoint.
func (h *DeviceFlowHandler) HandleVerifyUserCode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderVerifyError(w, "", "Invalid form data.")
		return
	}

	rawCode := r.FormValue("user_code")
	normalized := normalizeUserCode(rawCode)

	if normalized == "" {
		h.renderVerifyError(w, rawCode, "Please enter a code.")
		return
	}

	dc, err := h.store.GetDeviceCodeByUserCode(r.Context(), normalized)
	if err != nil {
		log.Printf("ERROR device_flow: lookup user code: %v", err)
		h.renderVerifyError(w, rawCode, "Something went wrong. Please try again.")
		return
	}
	if dc == nil {
		h.renderVerifyError(w, rawCode, "Invalid or expired code. Please check and try again.")
		return
	}

	// Build Hydra authorization URL.
	dfCfg := h.cfg.Auth.OAuth.Hydra.DeviceFlow
	hydraPublicURL := strings.TrimRight(h.cfg.Auth.OAuth.Hydra.PublicURL, "/")

	redirectURI := baseURL(r) + "/oauth/device/callback"
	scope := strings.Join(dc.Scopes, " ")

	authURL := fmt.Sprintf("%s/oauth2/auth?response_type=code&client_id=%s&redirect_uri=%s&scope=%s&state=%s",
		hydraPublicURL,
		url.QueryEscape(dfCfg.ClientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape(scope),
		url.QueryEscape(dc.VerificationNonce),
	)

	http.Redirect(w, r, authURL, http.StatusFound)
}

// HandleCallback handles GET /oauth/device/callback.
// Receives the auth code from Hydra, exchanges it for tokens, and stores them.
func (h *DeviceFlowHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		h.renderErrorPage(w, "Missing state parameter.")
		return
	}

	ctx := r.Context()

	dc, err := h.store.GetDeviceCodeByNonce(ctx, state)
	if err != nil {
		log.Printf("ERROR device_flow: lookup nonce: %v", err)
		h.renderErrorPage(w, "Something went wrong. Please try again.")
		return
	}
	if dc == nil {
		h.renderErrorPage(w, "Invalid or expired authorization request.")
		return
	}

	// Check for error from Hydra (user denied consent).
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		_ = h.store.DenyDeviceCode(ctx, dc.ID)
		desc := r.URL.Query().Get("error_description")
		if desc == "" {
			desc = "Authorization was denied."
		}
		h.renderErrorPage(w, desc)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		h.renderErrorPage(w, "Missing authorization code.")
		return
	}

	// Exchange auth code for tokens via Hydra's token endpoint.
	dfCfg := h.cfg.Auth.OAuth.Hydra.DeviceFlow
	hydraPublicURL := strings.TrimRight(h.cfg.Auth.OAuth.Hydra.PublicURL, "/")
	redirectURI := baseURL(r) + "/oauth/device/callback"

	tokenResp, err := exchangeAuthCode(ctx, hydraPublicURL, dfCfg.ClientID, dfCfg.ClientSecret, code, redirectURI)
	if err != nil {
		log.Printf("ERROR device_flow: exchange auth code: %v", err)
		h.renderErrorPage(w, "Failed to complete authorization. Please try again.")
		return
	}

	// Encrypt tokens before storage.
	encAccessToken, err := crypto.Encrypt(h.encKey, []byte(tokenResp.AccessToken))
	if err != nil {
		log.Printf("ERROR device_flow: encrypt access token: %v", err)
		h.renderErrorPage(w, "Internal error. Please try again.")
		return
	}
	encRefreshToken, err := crypto.Encrypt(h.encKey, []byte(tokenResp.RefreshToken))
	if err != nil {
		log.Printf("ERROR device_flow: encrypt refresh token: %v", err)
		h.renderErrorPage(w, "Internal error. Please try again.")
		return
	}

	if err := h.store.ApproveDeviceCode(ctx, dc.ID, encAccessToken, encRefreshToken, tokenResp.TokenType, tokenResp.ExpiresIn); err != nil {
		log.Printf("ERROR device_flow: approve device code: %v", err)
		h.renderErrorPage(w, "Failed to save authorization. Please try again.")
		return
	}

	// Render success page.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.templates.ExecuteTemplate(w, "device_success.html", nil)
}

// HandleTokenPoll handles POST /oauth/device/token.
// CLI clients poll this endpoint to retrieve tokens after user approval.
func (h *DeviceFlowHandler) HandleTokenPoll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		GrantType  string `json:"grant_type"`
		DeviceCode string `json:"device_code"`
		ClientID   string `json:"client_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	if body.GrantType != "urn:ietf:params:oauth:grant-type:device_code" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}

	if body.DeviceCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}

	ctx := r.Context()

	dc, err := h.store.GetDeviceCodeByCode(ctx, body.DeviceCode)
	if err != nil {
		log.Printf("ERROR device_flow: lookup device code: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	if dc == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}

	// Check expiry.
	if time.Now().After(dc.ExpiresAt) {
		_ = h.store.DeleteDeviceCode(ctx, dc.ID)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expired_token"})
		return
	}

	switch dc.Status {
	case "denied":
		_ = h.store.DeleteDeviceCode(ctx, dc.ID)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "access_denied"})
		return

	case "pending":
		// Check slow_down: if polled faster than the interval.
		slowDown := false
		if dc.LastPolledAt != nil {
			elapsed := time.Since(*dc.LastPolledAt)
			if elapsed < time.Duration(dc.PollInterval)*time.Second {
				slowDown = true
			}
		}
		_ = h.store.UpdateDeviceCodePoll(ctx, dc.ID, slowDown)
		if slowDown {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slow_down"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
		return

	case "approved":
		// Decrypt and return tokens.
		accessToken, err := crypto.Decrypt(h.encKey, dc.AccessToken)
		if err != nil {
			log.Printf("ERROR device_flow: decrypt access token: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return
		}
		refreshToken, err := crypto.Decrypt(h.encKey, dc.RefreshToken)
		if err != nil {
			log.Printf("ERROR device_flow: decrypt refresh token: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return
		}

		// Delete after successful retrieval to prevent replay.
		_ = h.store.DeleteDeviceCode(ctx, dc.ID)

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"access_token":  string(accessToken),
			"refresh_token": string(refreshToken),
			"token_type":    dc.TokenType,
			"expires_in":    dc.TokenExpiresIn,
		})
		return

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
	}
}

// --- Helpers ---

// generateDeviceCode returns a 64-char hex string (32 bytes of randomness).
func generateDeviceCode() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// generateUserCode returns an 8-char string from the consonant charset.
func generateUserCode() (string, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	code := make([]byte, 8)
	for i := range code {
		code[i] = userCodeCharset[int(b[i])%len(userCodeCharset)]
	}
	return string(code), nil
}

// generateNonce returns a 32-char hex string (16 bytes of randomness).
func generateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// formatUserCode inserts a dash in the middle: "ABCDEFGH" -> "ABCD-EFGH".
func formatUserCode(code string) string {
	if len(code) != 8 {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// normalizeUserCode strips dashes/spaces and uppercases the input.
func normalizeUserCode(raw string) string {
	s := strings.ToUpper(raw)
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// hydraTokenResponse represents the JSON response from Hydra's token endpoint.
type hydraTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// exchangeAuthCode exchanges an authorization code for tokens via Hydra's token endpoint.
func exchangeAuthCode(ctx context.Context, hydraPublicURL, clientID, clientSecret, code, redirectURI string) (*hydraTokenResponse, error) {
	endpoint := hydraPublicURL + "/oauth2/token"

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp hydraTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return &tokenResp, nil
}

// renderVerifyError re-renders the verification form with an error message.
func (h *DeviceFlowHandler) renderVerifyError(w http.ResponseWriter, userCode, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.templates.ExecuteTemplate(w, "device_verify.html", deviceVerifyData{
		UserCode: userCode,
		Error:    errMsg,
	})
}

// renderErrorPage renders the device_error.html template.
func (h *DeviceFlowHandler) renderErrorPage(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	h.templates.ExecuteTemplate(w, "device_error.html", deviceErrorData{
		Error: errMsg,
	})
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/admin/`
Expected: Build succeeds.

- [ ] **Step 3: Commit**

```bash
git add internal/admin/device_flow.go
git commit -m "feat(device-flow): add DeviceFlowHandler with all 5 endpoints"
```

---

### Task 6: Unit Tests for DeviceFlowHandler

**Files:**
- Create: `internal/admin/device_flow_test.go`

- [ ] **Step 1: Write unit tests**

Create file `internal/admin/device_flow_test.go`:

```go
package admin

import (
	"testing"
)

func TestGenerateDeviceCode(t *testing.T) {
	code, err := generateDeviceCode()
	if err != nil {
		t.Fatalf("generateDeviceCode: %v", err)
	}
	if len(code) != 64 {
		t.Errorf("device code length = %d, want 64", len(code))
	}
	// Verify hex-only characters.
	for _, c := range code {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("device code contains non-hex char: %c", c)
		}
	}
	// Verify uniqueness (two calls should produce different codes).
	code2, _ := generateDeviceCode()
	if code == code2 {
		t.Error("two consecutive generateDeviceCode calls produced identical codes")
	}
}

func TestGenerateUserCode(t *testing.T) {
	code, err := generateUserCode()
	if err != nil {
		t.Fatalf("generateUserCode: %v", err)
	}
	if len(code) != 8 {
		t.Errorf("user code length = %d, want 8", len(code))
	}
	// Verify all characters are in the charset.
	for _, c := range code {
		found := false
		for _, allowed := range userCodeCharset {
			if c == allowed {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("user code contains invalid char: %c", c)
		}
	}
}

func TestGenerateNonce(t *testing.T) {
	nonce, err := generateNonce()
	if err != nil {
		t.Fatalf("generateNonce: %v", err)
	}
	if len(nonce) != 32 {
		t.Errorf("nonce length = %d, want 32", len(nonce))
	}
}

func TestFormatUserCode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"BCDFGHJK", "BCDF-GHJK"},
		{"ABCDEFGH", "ABCD-EFGH"},
		{"SHORT", "SHORT"},   // less than 8 chars: returned as-is
		{"", ""},
	}
	for _, tt := range tests {
		got := formatUserCode(tt.input)
		if got != tt.want {
			t.Errorf("formatUserCode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeUserCode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"bcdf-ghjk", "BCDFGHJK"},
		{"BCDF-GHJK", "BCDFGHJK"},
		{"bcdf ghjk", "BCDFGHJK"},
		{"  BCDF - GHJK  ", "BCDFGHJK"},
		{"", ""},
	}
	for _, tt := range tests {
		got := normalizeUserCode(tt.input)
		if got != tt.want {
			t.Errorf("normalizeUserCode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run tests**

Run: `go test ./internal/admin/ -run TestGenerate -v && go test ./internal/admin/ -run TestFormatUserCode -v && go test ./internal/admin/ -run TestNormalizeUserCode -v`
Expected: All PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/admin/device_flow_test.go
git commit -m "test(device-flow): add unit tests for code generation and formatting"
```

---

### Task 7: Mount Routes

**Files:**
- Modify: `internal/admin/routes.go`

- [ ] **Step 1: Add device flow routes**

In `internal/admin/routes.go`, find the closing `}` of the `if hydraClient != nil {` block (after the consent handler routes) and add the device flow routes inside the same block, before the closing `}`:

```go
		// Device Flow (RFC 8628) endpoints (public — no JWT auth required).
		if cfg.Auth.OAuth.Hydra.DeviceFlow.ClientID != "" {
			deviceHandler, err := NewDeviceFlowHandler(hydraClient, st, encKey, cfg)
			if err != nil {
				panic("admin: failed to create device flow handler: " + err.Error())
			}
			r.Post("/oauth/device/code", deviceHandler.HandleDeviceAuthorize)
			r.Get("/oauth/device", deviceHandler.HandleVerificationPage)
			r.Post("/oauth/device", deviceHandler.HandleVerifyUserCode)
			r.Get("/oauth/device/callback", deviceHandler.HandleCallback)
			r.Post("/oauth/device/token", deviceHandler.HandleTokenPoll)
		}
```

This should go right after:
```go
		r.Get("/oauth/consent", consentHandler.ServeHTTP)
		r.Post("/oauth/consent", consentHandler.ServeHTTP)
```

And before the closing `}` of `if hydraClient != nil`.

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/admin/`
Expected: Build succeeds.

- [ ] **Step 3: Run full test suite**

Run: `go test ./... 2>&1 | tail -20`
Expected: All tests PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/admin/routes.go
git commit -m "feat(device-flow): mount device flow endpoints in admin routes"
```

---

### Task 8: Hydra Client Registration Script

**Files:**
- Create: `scripts/register-device-flow-client.sh`

- [ ] **Step 1: Create script**

Create file `scripts/register-device-flow-client.sh`:

```bash
#!/bin/bash
# Register a dedicated device flow OAuth client in Hydra.
#
# Usage:
#   HYDRA_ADMIN_URL=http://localhost:4445 \
#   MODELSERVER_BASE_URL=https://codeapi.example.com \
#   DEVICE_FLOW_CLIENT_ID=device-flow-client \
#   DEVICE_FLOW_CLIENT_SECRET=change-me \
#   ./scripts/register-device-flow-client.sh

set -euo pipefail

HYDRA_ADMIN_URL="${HYDRA_ADMIN_URL:-http://127.0.0.1:4445}"
DEVICE_FLOW_CLIENT_ID="${DEVICE_FLOW_CLIENT_ID:-device-flow-client}"
DEVICE_FLOW_CLIENT_SECRET="${DEVICE_FLOW_CLIENT_SECRET:-device-flow-secret-change-me}"
MODELSERVER_BASE_URL="${MODELSERVER_BASE_URL:-https://localhost:8081}"

REDIRECT_URI="${MODELSERVER_BASE_URL}/oauth/device/callback"

echo "Registering device flow OAuth client '${DEVICE_FLOW_CLIENT_ID}' in Hydra..."

# Delete existing client if present (idempotent)
curl -s -o /dev/null -w "" -X DELETE \
  "${HYDRA_ADMIN_URL}/admin/clients/${DEVICE_FLOW_CLIENT_ID}" 2>/dev/null || true

# Create client
curl -s -X POST "${HYDRA_ADMIN_URL}/admin/clients" \
  -H "Content-Type: application/json" \
  -d "{
    \"client_id\": \"${DEVICE_FLOW_CLIENT_ID}\",
    \"client_name\": \"Device Flow\",
    \"client_secret\": \"${DEVICE_FLOW_CLIENT_SECRET}\",
    \"redirect_uris\": [\"${REDIRECT_URI}\"],
    \"grant_types\": [\"authorization_code\", \"refresh_token\"],
    \"response_types\": [\"code\"],
    \"scope\": \"project:inference offline_access\",
    \"token_endpoint_auth_method\": \"client_secret_post\"
  }" | python3 -m json.tool 2>/dev/null || cat

echo ""
echo "Done. Device flow client '${DEVICE_FLOW_CLIENT_ID}' registered."
echo "  Redirect URI: ${REDIRECT_URI}"
echo "  Secret:       ${DEVICE_FLOW_CLIENT_SECRET}"
```

- [ ] **Step 2: Make executable**

```bash
chmod +x scripts/register-device-flow-client.sh
```

- [ ] **Step 3: Commit**

```bash
git add scripts/register-device-flow-client.sh
git commit -m "feat(device-flow): add Hydra client registration script"
```

---

### Task 9: Integration Verification

This task ties everything together and verifies the build.

- [ ] **Step 1: Run all tests**

Run: `go test ./... 2>&1 | tail -30`
Expected: All tests PASS (no regressions).

- [ ] **Step 2: Build the full binary**

Run: `go build ./cmd/...`
Expected: Build succeeds.

- [ ] **Step 3: Verify migration count**

Run: `ls -1 internal/store/migrations/*.sql | wc -l`
Expected: 14 files (the original 13 plus `014_device_codes.sql`).

- [ ] **Step 4: Final commit (if any unstaged changes)**

If any fixups were needed during verification, commit them:

```bash
git add -A
git commit -m "fix(device-flow): address integration verification findings"
```
