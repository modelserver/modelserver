package config

import (
	"io"
	"os"
	"strings"
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
	MchPrivateKeyPEM  string `yaml:"mch_private_key_pem"`
	NotifyURL         string `yaml:"notify_url"`
}

type AlipayConfig struct {
	AppID               string `yaml:"app_id"`
	PrivateKeyPath      string `yaml:"private_key_path"`
	PrivateKeyPEM       string `yaml:"private_key_pem"`
	AlipayPublicKeyPath string `yaml:"alipay_public_key_path"`
	AlipayPublicKeyPEM  string `yaml:"alipay_public_key_pem"`
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
	if v := os.Getenv("PAYSERVER_WECHAT_MCH_PRIVATE_KEY_PEM"); v != "" {
		c.WeChat.MchPrivateKeyPEM = v
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
	if v := os.Getenv("PAYSERVER_ALIPAY_PRIVATE_KEY_PEM"); v != "" {
		c.Alipay.PrivateKeyPEM = v
	}
	if v := os.Getenv("PAYSERVER_ALIPAY_PUBLIC_KEY_PATH"); v != "" {
		c.Alipay.AlipayPublicKeyPath = v
	}
	if v := os.Getenv("PAYSERVER_ALIPAY_PUBLIC_KEY_PEM"); v != "" {
		c.Alipay.AlipayPublicKeyPEM = v
	}
	if v := os.Getenv("PAYSERVER_ALIPAY_NOTIFY_URL"); v != "" {
		c.Alipay.NotifyURL = v
	}
	if v := os.Getenv("PAYSERVER_ALIPAY_RETURN_URL"); v != "" {
		c.Alipay.ReturnURL = v
	}

	// Normalize PEM strings — detect raw base64 and wrap with proper headers.
	c.WeChat.MchPrivateKeyPEM = normalizePEM(c.WeChat.MchPrivateKeyPEM, "PRIVATE KEY")
	c.Alipay.PrivateKeyPEM = normalizePEM(c.Alipay.PrivateKeyPEM, "PRIVATE KEY")
	c.Alipay.AlipayPublicKeyPEM = normalizePEM(c.Alipay.AlipayPublicKeyPEM, "PUBLIC KEY")
}

// normalizePEM accepts PEM content in any of these forms:
//   - Standard multi-line PEM with headers
//   - Single-line PEM with literal \n separators
//   - Raw base64 without headers (output of scripts/pem-encode.sh)
//
// It always returns a valid multi-line PEM string.
func normalizePEM(s, label string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.TrimSpace(s)

	if strings.HasPrefix(s, "-----") {
		return s
	}

	// Raw base64 — fold to 64-char lines and wrap with headers.
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
