package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"     yaml:"server"`
	DB       DBConfig       `mapstructure:"db"         yaml:"db"`
	Callback CallbackConfig `mapstructure:"callback"   yaml:"callback"`
	// APIKey is deprecated; kept for env var compatibility during migration.
	// Use per-tenant credentials via the admin UI instead.
	APIKey string `mapstructure:"api_key"    yaml:"api_key"`
	Log    LogConfig `mapstructure:"log"        yaml:"log"`
	WeChat   WeChatConfig   `mapstructure:"wechat"     yaml:"wechat"`
	Alipay   AlipayConfig   `mapstructure:"alipay"     yaml:"alipay"`
	Stripe   StripeConfig   `mapstructure:"stripe"     yaml:"stripe"`
	OIDC     OIDCConfig     `mapstructure:"oidc"       yaml:"oidc"`
}

type ServerConfig struct {
	Addr string `mapstructure:"addr" yaml:"addr"`
}

type DBConfig struct {
	URL string `mapstructure:"url" yaml:"url"`
}

type CallbackConfig struct {
	ModelserverURL string        `mapstructure:"modelserver_url" yaml:"modelserver_url"`
	WebhookSecret  string        `mapstructure:"webhook_secret"  yaml:"webhook_secret"`
	Timeout        time.Duration `mapstructure:"timeout"         yaml:"timeout"`
	// AllowPrivateNetworks: by default callback URLs that resolve to
	// loopback, RFC1918, link-local, or other non-routable addresses are
	// rejected (SSRF guard). Set true in test envs that genuinely target
	// private hosts.
	AllowPrivateNetworks bool `mapstructure:"allow_private_networks" yaml:"allow_private_networks"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"  yaml:"level"`
	Format string `mapstructure:"format" yaml:"format"`
}

type WeChatConfig struct {
	AppID             string `mapstructure:"app_id"               yaml:"app_id"`
	MchID             string `mapstructure:"mch_id"               yaml:"mch_id"`
	MchAPIv3Key       string `mapstructure:"mch_api_v3_key"       yaml:"mch_api_v3_key"`
	MchSerialNo       string `mapstructure:"mch_serial_no"        yaml:"mch_serial_no"`
	MchPrivateKeyPath string `mapstructure:"mch_private_key_path" yaml:"mch_private_key_path"`
	MchPrivateKeyPEM  string `mapstructure:"mch_private_key_pem"  yaml:"mch_private_key_pem"`
	NotifyURL         string `mapstructure:"notify_url"           yaml:"notify_url"`
}

type AlipayConfig struct {
	AppID          string `mapstructure:"app_id"           yaml:"app_id"`
	PrivateKeyPath string `mapstructure:"private_key_path" yaml:"private_key_path"`
	PrivateKeyPEM  string `mapstructure:"private_key_pem"  yaml:"private_key_pem"`
	// PublicKeyPath / PublicKeyPEM hold the Alipay platform public key
	// used to verify callback signatures. Named `public_key_*` (not
	// `alipay_public_key_*`) because we're already in the `alipay`
	// namespace — `alipay.alipay_public_key_path` would just stutter.
	// In Alipay's signing model the merchant's own key is always private
	// (signs outgoing requests), and the public key in this config is
	// always Alipay's (verifies incoming callbacks), so "public" is
	// unambiguous here.
	PublicKeyPath string `mapstructure:"public_key_path" yaml:"public_key_path"`
	PublicKeyPEM  string `mapstructure:"public_key_pem"  yaml:"public_key_pem"`
	NotifyURL     string `mapstructure:"notify_url"      yaml:"notify_url"`
	ReturnURL     string `mapstructure:"return_url"      yaml:"return_url"`
}

type StripeConfig struct {
	SecretKey     string `mapstructure:"secret_key"     yaml:"secret_key"`
	WebhookSecret string `mapstructure:"webhook_secret" yaml:"webhook_secret"`
	SuccessURL    string `mapstructure:"success_url"    yaml:"success_url"`
	CancelURL     string `mapstructure:"cancel_url"     yaml:"cancel_url"`
	DefaultLocale string `mapstructure:"default_locale" yaml:"default_locale"`
}

type OIDCConfig struct {
	IssuerURL    string   `mapstructure:"issuer_url"    yaml:"issuer_url"`
	ClientID     string   `mapstructure:"client_id"     yaml:"client_id"`
	ClientSecret string   `mapstructure:"client_secret" yaml:"client_secret"`
	RedirectURL  string   `mapstructure:"redirect_url"  yaml:"redirect_url"`
	Scopes       []string `mapstructure:"scopes"        yaml:"scopes"`
	// AllowedEmails restricts admin access to the listed email addresses.
	// Either AllowedEmails must be non-empty OR AllowAnyAuthenticated must
	// be true — Validate() rejects an OIDC config with neither set. Without
	// the explicit opt-in, an operator who forgets to populate the list
	// would silently expose admin to every user the IdP can authenticate.
	AllowedEmails []string `mapstructure:"allowed_emails" yaml:"allowed_emails"`
	// AllowAnyAuthenticated opts out of the AllowedEmails requirement.
	// Set this to true ONLY when the IdP itself is already restricted to
	// the same population that should have admin access (e.g. a dedicated
	// Okta group). Default false.
	AllowAnyAuthenticated bool   `mapstructure:"allow_any_authenticated" yaml:"allow_any_authenticated"`
	SessionSecret         string `mapstructure:"session_secret"          yaml:"session_secret"`
}

// Validate performs fast, in-process checks of required fields so the
// operator gets a clear single error early rather than a partial boot
// that later fatals deep inside a gateway constructor. Should be called
// immediately after Load.
func (c *Config) Validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr is required")
	}
	if c.DB.URL == "" {
		return fmt.Errorf("db.url is required (or PAYSERVER_DB_URL)")
	}

	// Per-gateway companion checks. Detect "gateway enabled" by the same
	// trigger field main.go uses.
	if c.WeChat.AppID != "" {
		if c.WeChat.MchID == "" {
			return fmt.Errorf("wechat.mch_id is required when wechat.app_id is set")
		}
		if c.WeChat.MchAPIv3Key == "" {
			return fmt.Errorf("wechat.mch_api_v3_key is required when wechat.app_id is set")
		}
		if c.WeChat.MchSerialNo == "" {
			return fmt.Errorf("wechat.mch_serial_no is required when wechat.app_id is set")
		}
		if c.WeChat.MchPrivateKeyPath == "" && c.WeChat.MchPrivateKeyPEM == "" {
			return fmt.Errorf("wechat.mch_private_key_path or wechat.mch_private_key_pem is required when wechat.app_id is set")
		}
	}
	if c.Alipay.AppID != "" {
		if c.Alipay.PrivateKeyPath == "" && c.Alipay.PrivateKeyPEM == "" {
			return fmt.Errorf("alipay.private_key_path or alipay.private_key_pem is required when alipay.app_id is set")
		}
		if c.Alipay.PublicKeyPath == "" && c.Alipay.PublicKeyPEM == "" {
			return fmt.Errorf("alipay.public_key_path or alipay.public_key_pem is required when alipay.app_id is set")
		}
	}
	if c.Stripe.SecretKey != "" {
		if c.Stripe.WebhookSecret == "" {
			return fmt.Errorf("stripe.webhook_secret is required when stripe.secret_key is set")
		}
	}

	// OIDC enabled = issuer_url set. NewOIDCAuth re-checks these (defense
	// in depth) but surfacing the error here gives a clean message before
	// any provider discovery network call.
	if c.OIDC.IssuerURL != "" {
		if c.OIDC.ClientID == "" {
			return fmt.Errorf("oidc.client_id is required when oidc.issuer_url is set")
		}
		if c.OIDC.ClientSecret == "" {
			return fmt.Errorf("oidc.client_secret is required when oidc.issuer_url is set")
		}
		if c.OIDC.RedirectURL == "" {
			return fmt.Errorf("oidc.redirect_url is required when oidc.issuer_url is set")
		}
		const minSessionSecretChars = 32
		if len(c.OIDC.SessionSecret) < minSessionSecretChars {
			return fmt.Errorf("oidc.session_secret must be at least %d characters", minSessionSecretChars)
		}
		// Empty allowed_emails + no explicit opt-in = misconfiguration
		// footgun: every IdP-authenticated user gets admin. Force the
		// operator to either list emails or acknowledge the broader
		// access surface by setting allow_any_authenticated=true.
		if len(c.OIDC.AllowedEmails) == 0 && !c.OIDC.AllowAnyAuthenticated {
			return fmt.Errorf("oidc.allowed_emails is empty; either list emails or set oidc.allow_any_authenticated=true to explicitly grant admin to every IdP-validated user")
		}
	}
	return nil
}

// Load reads the optional config file (path may be "") and overlays env vars
// with the PAYSERVER_ prefix. Nested keys translate from dots/underscores:
// PAYSERVER_DB_URL → db.url; PAYSERVER_STRIPE_SECRET_KEY → stripe.secret_key.
// Env values always win over file values.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix("PAYSERVER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Defaults
	v.SetDefault("server.addr", ":8090")
	v.SetDefault("callback.timeout", 10*time.Second)
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

	// File (optional)
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
	}

	// Every leaf key must be explicitly bound for AutomaticEnv to find it
	// through nested-struct unmarshal (viper's known limitation).
	for _, key := range []string{
		"server.addr",
		"db.url",
		"callback.modelserver_url",
		"callback.webhook_secret",
		"callback.timeout",
		"callback.allow_private_networks",
		"api_key",
		"log.level",
		"log.format",
		"wechat.app_id",
		"wechat.mch_id",
		"wechat.mch_api_v3_key",
		"wechat.mch_serial_no",
		"wechat.mch_private_key_path",
		"wechat.mch_private_key_pem",
		"wechat.notify_url",
		"alipay.app_id",
		"alipay.private_key_path",
		"alipay.private_key_pem",
		"alipay.public_key_path",
		"alipay.public_key_pem",
		"alipay.notify_url",
		"alipay.return_url",
		"stripe.secret_key",
		"stripe.webhook_secret",
		"stripe.success_url",
		"stripe.cancel_url",
		"stripe.default_locale",
		"oidc.issuer_url",
		"oidc.client_id",
		"oidc.client_secret",
		"oidc.redirect_url",
		"oidc.scopes",
		"oidc.allowed_emails",
		"oidc.session_secret",
	} {
		_ = v.BindEnv(key)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if len(cfg.OIDC.Scopes) == 0 {
		cfg.OIDC.Scopes = []string{"openid", "profile", "email"}
	}

	cfg.WeChat.MchPrivateKeyPEM = normalizePEM(cfg.WeChat.MchPrivateKeyPEM, "PRIVATE KEY")
	cfg.Alipay.PrivateKeyPEM = normalizePEM(cfg.Alipay.PrivateKeyPEM, "PRIVATE KEY")
	cfg.Alipay.PublicKeyPEM = normalizePEM(cfg.Alipay.PublicKeyPEM, "PUBLIC KEY")

	return &cfg, nil
}

// normalizePEM accepts PEM content in any of these forms and returns a
// valid multi-line PEM string:
//   - Standard multi-line PEM with headers
//   - Single-line PEM with literal \n separators
//   - Raw base64 without headers (output of scripts/pem-encode.sh)
func normalizePEM(s, label string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.TrimSpace(s)

	if strings.HasPrefix(s, "-----") {
		return s
	}

	var lines []string
	lines = append(lines, "-----BEGIN "+label+"-----")
	for len(s) > 64 {
		lines = append(lines, s[:64])
		s = s[64:]
	}
	if len(s) > 0 {
		lines = append(lines, s)
	}
	lines = append(lines, "-----END "+label+"-----")
	return strings.Join(lines, "\n")
}
