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
	AppID               string `mapstructure:"app_id"                  yaml:"app_id"`
	PrivateKeyPath      string `mapstructure:"private_key_path"        yaml:"private_key_path"`
	PrivateKeyPEM       string `mapstructure:"private_key_pem"         yaml:"private_key_pem"`
	AlipayPublicKeyPath string `mapstructure:"alipay_public_key_path"  yaml:"alipay_public_key_path"`
	AlipayPublicKeyPEM  string `mapstructure:"alipay_public_key_pem"   yaml:"alipay_public_key_pem"`
	NotifyURL           string `mapstructure:"notify_url"              yaml:"notify_url"`
	ReturnURL           string `mapstructure:"return_url"              yaml:"return_url"`
}

type StripeConfig struct {
	SecretKey     string `mapstructure:"secret_key"     yaml:"secret_key"`
	WebhookSecret string `mapstructure:"webhook_secret" yaml:"webhook_secret"`
	SuccessURL    string `mapstructure:"success_url"    yaml:"success_url"`
	CancelURL     string `mapstructure:"cancel_url"     yaml:"cancel_url"`
	DefaultLocale string `mapstructure:"default_locale" yaml:"default_locale"`
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
		"alipay.alipay_public_key_path",
		"alipay.alipay_public_key_pem",
		"alipay.notify_url",
		"alipay.return_url",
		"stripe.secret_key",
		"stripe.webhook_secret",
		"stripe.success_url",
		"stripe.cancel_url",
		"stripe.default_locale",
	} {
		_ = v.BindEnv(key)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	cfg.WeChat.MchPrivateKeyPEM = normalizePEM(cfg.WeChat.MchPrivateKeyPEM, "PRIVATE KEY")
	cfg.Alipay.PrivateKeyPEM = normalizePEM(cfg.Alipay.PrivateKeyPEM, "PRIVATE KEY")
	cfg.Alipay.AlipayPublicKeyPEM = normalizePEM(cfg.Alipay.AlipayPublicKeyPEM, "PUBLIC KEY")

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
