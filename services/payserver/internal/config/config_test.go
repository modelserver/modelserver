package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad_YAMLBaseline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(`
server:
  addr: ":9090"
db:
  url: "postgres://test/test"
callback:
  modelserver_url: "https://ms.example/webhook"
  webhook_secret: "wh-secret"
  timeout: 7s
api_key: "from-yaml"
log:
  level: "debug"
  format: "console"
stripe:
  secret_key: "sk_test_yaml"
  webhook_secret: "whsec_yaml"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Addr != ":9090" {
		t.Errorf("Server.Addr = %q", cfg.Server.Addr)
	}
	if cfg.DB.URL != "postgres://test/test" {
		t.Errorf("DB.URL = %q", cfg.DB.URL)
	}
	if cfg.Callback.ModelserverURL != "https://ms.example/webhook" {
		t.Errorf("Callback.ModelserverURL = %q", cfg.Callback.ModelserverURL)
	}
	if cfg.Callback.Timeout != 7*time.Second {
		t.Errorf("Callback.Timeout = %v", cfg.Callback.Timeout)
	}
	if cfg.APIKey != "from-yaml" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
	if cfg.Log.Format != "console" {
		t.Errorf("Log.Format = %q", cfg.Log.Format)
	}
	if cfg.Stripe.SecretKey != "sk_test_yaml" {
		t.Errorf("Stripe.SecretKey = %q", cfg.Stripe.SecretKey)
	}
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(`
api_key: "from-yaml"
db:
  url: "postgres://yaml/yaml"
stripe:
  secret_key: "sk_yaml"
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("PAYSERVER_API_KEY", "from-env")
	t.Setenv("PAYSERVER_DB_URL", "postgres://env/env")
	t.Setenv("PAYSERVER_STRIPE_SECRET_KEY", "sk_env")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKey != "from-env" {
		t.Errorf("APIKey: env should win, got %q", cfg.APIKey)
	}
	if cfg.DB.URL != "postgres://env/env" {
		t.Errorf("DB.URL: env should win, got %q", cfg.DB.URL)
	}
	if cfg.Stripe.SecretKey != "sk_env" {
		t.Errorf("Stripe.SecretKey: env should win, got %q", cfg.Stripe.SecretKey)
	}
}

func TestLoad_NoFile_EnvOnly(t *testing.T) {
	t.Setenv("PAYSERVER_DB_URL", "postgres://env-only/db")
	t.Setenv("PAYSERVER_API_KEY", "env-only")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DB.URL != "postgres://env-only/db" {
		t.Errorf("DB.URL = %q", cfg.DB.URL)
	}
	if cfg.APIKey != "env-only" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
	// Defaults still apply
	if cfg.Server.Addr != ":8090" {
		t.Errorf("Server.Addr default = %q, want :8090", cfg.Server.Addr)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level default = %q, want info", cfg.Log.Level)
	}
}

func TestLoad_NormalizesPEM(t *testing.T) {
	// Raw base64 (no -----BEGIN----- prefix) — normalizePEM should wrap it.
	rawB64 := "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQDXXXXXXXXXX"
	t.Setenv("PAYSERVER_WECHAT_MCH_PRIVATE_KEY_PEM", rawB64)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !contains(cfg.WeChat.MchPrivateKeyPEM, "-----BEGIN PRIVATE KEY-----") {
		t.Errorf("PEM was not wrapped: %q", cfg.WeChat.MchPrivateKeyPEM)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func validBase() Config {
	return Config{
		Server: ServerConfig{Addr: ":8090"},
		DB:     DBConfig{URL: "postgres://x/y"},
	}
}

func TestConfig_Validate_HappyPath(t *testing.T) {
	c := validBase()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestConfig_Validate_MissingDBURL(t *testing.T) {
	c := validBase()
	c.DB.URL = ""
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error on missing db.url")
	}
	if !contains(err.Error(), "db.url") {
		t.Errorf("err = %q, want mention of db.url", err)
	}
}

func TestConfig_Validate_MissingServerAddr(t *testing.T) {
	c := validBase()
	c.Server.Addr = ""
	if err := c.Validate(); err == nil {
		t.Fatal("expected error on missing server.addr")
	}
}

func TestConfig_Validate_StripeWithoutWebhookSecret(t *testing.T) {
	c := validBase()
	c.Stripe.SecretKey = "sk_test_x"
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error: stripe without webhook secret")
	}
	if !contains(err.Error(), "webhook_secret") {
		t.Errorf("err = %q, want mention of webhook_secret", err)
	}
}

func TestConfig_Validate_StripeWithWebhookSecret_OK(t *testing.T) {
	c := validBase()
	c.Stripe.SecretKey = "sk_test_x"
	c.Stripe.WebhookSecret = "whsec_x"
	if err := c.Validate(); err != nil {
		t.Errorf("unexpected: %v", err)
	}
}

func TestConfig_Validate_OIDCIssuerWithoutRedirect(t *testing.T) {
	c := validBase()
	c.OIDC.IssuerURL = "https://idp.example"
	c.OIDC.ClientID = "cid"
	c.OIDC.ClientSecret = "csec"
	c.OIDC.SessionSecret = "thirty-two-char-session-secret--!" // 32 chars
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error: oidc without redirect_url")
	}
	if !contains(err.Error(), "redirect_url") {
		t.Errorf("err = %q, want mention of redirect_url", err)
	}
}

func TestConfig_Validate_OIDCShortSessionSecret(t *testing.T) {
	c := validBase()
	c.OIDC.IssuerURL = "https://idp.example"
	c.OIDC.ClientID = "cid"
	c.OIDC.ClientSecret = "csec"
	c.OIDC.RedirectURL = "https://x/cb"
	c.OIDC.SessionSecret = "too-short"
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error: short session_secret")
	}
	if !contains(err.Error(), "session_secret") {
		t.Errorf("err = %q, want mention of session_secret", err)
	}
}

func TestConfig_Validate_WeChatWithoutMchID(t *testing.T) {
	c := validBase()
	c.WeChat.AppID = "wx-app"
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error: wechat without mch_id")
	}
	if !contains(err.Error(), "mch_id") {
		t.Errorf("err = %q, want mention of mch_id", err)
	}
}

// TestConfig_Validate_OIDCEmptyAllowlistRejected closes the misconfig
// footgun: an OIDC-enabled config with empty allowed_emails and no
// explicit allow_any_authenticated opt-in must fail at startup. Without
// this guard, every IdP-validated user would silently land in admin.
func TestConfig_Validate_OIDCEmptyAllowlistRejected(t *testing.T) {
	c := validBase()
	c.OIDC.IssuerURL = "https://idp.example"
	c.OIDC.ClientID = "cid"
	c.OIDC.ClientSecret = "csec"
	c.OIDC.RedirectURL = "https://x/cb"
	c.OIDC.SessionSecret = "thirty-two-char-session-secret--!"
	// AllowedEmails empty; AllowAnyAuthenticated false → reject.
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error: empty allowed_emails without opt-in")
	}
	if !contains(err.Error(), "allowed_emails") || !contains(err.Error(), "allow_any_authenticated") {
		t.Errorf("err = %q, want mention of both allowed_emails + allow_any_authenticated", err)
	}
}

// TestConfig_Validate_OIDCAllowAnyAuthenticated_OK confirms the
// explicit opt-in path passes Validate.
func TestConfig_Validate_OIDCAllowAnyAuthenticated_OK(t *testing.T) {
	c := validBase()
	c.OIDC.IssuerURL = "https://idp.example"
	c.OIDC.ClientID = "cid"
	c.OIDC.ClientSecret = "csec"
	c.OIDC.RedirectURL = "https://x/cb"
	c.OIDC.SessionSecret = "thirty-two-char-session-secret--!"
	c.OIDC.AllowAnyAuthenticated = true
	if err := c.Validate(); err != nil {
		t.Errorf("unexpected: %v", err)
	}
}

// TestConfig_Validate_OIDCWithAllowedEmails_OK confirms the standard
// allowlist path passes Validate.
func TestConfig_Validate_OIDCWithAllowedEmails_OK(t *testing.T) {
	c := validBase()
	c.OIDC.IssuerURL = "https://idp.example"
	c.OIDC.ClientID = "cid"
	c.OIDC.ClientSecret = "csec"
	c.OIDC.RedirectURL = "https://x/cb"
	c.OIDC.SessionSecret = "thirty-two-char-session-secret--!"
	c.OIDC.AllowedEmails = []string{"ops@example.com"}
	if err := c.Validate(); err != nil {
		t.Errorf("unexpected: %v", err)
	}
}

func TestConfig_Validate_AlipayWithoutPrivateKey(t *testing.T) {
	c := validBase()
	c.Alipay.AppID = "alipay-app"
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error: alipay without private key")
	}
	if !contains(err.Error(), "private_key") {
		t.Errorf("err = %q, want mention of private_key", err)
	}
}

// TestConfig_Validate_AlipayWithoutPublicKey pins the error string for
// the alipay platform-public-key check. The fields are named
// `alipay.public_key_*` (not `alipay.alipay_public_key_*`) — the prefix
// was redundant in the `alipay.` namespace. This test ensures the error
// message stays in sync with the field name an operator would search for.
func TestConfig_Validate_AlipayWithoutPublicKey(t *testing.T) {
	c := validBase()
	c.Alipay.AppID = "alipay-app"
	c.Alipay.PrivateKeyPEM = "fake-private-key"
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error: alipay without public key")
	}
	if !contains(err.Error(), "alipay.public_key_path") || !contains(err.Error(), "alipay.public_key_pem") {
		t.Errorf("err = %q, want mention of alipay.public_key_path AND alipay.public_key_pem", err)
	}
	// Negative: the redundant `alipay.alipay_public_key_*` form must not
	// appear in the message (regression guard for the rename).
	if contains(err.Error(), "alipay_public_key") {
		t.Errorf("err = %q, must not contain the redundant `alipay_public_key` prefix", err)
	}
}
