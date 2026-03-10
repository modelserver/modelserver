package config

import (
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration struct.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	DB         DBConfig         `yaml:"db"`
	Auth       AuthConfig       `yaml:"auth"`
	Encryption EncryptionConfig `yaml:"encryption"`
	Trace      TraceConfig      `yaml:"trace"`
	Collector  CollectorConfig  `yaml:"collector"`
	Log        LogConfig        `yaml:"log"`
	CORS       CORSConfig       `yaml:"cors"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	ProxyAddr      string        `yaml:"proxy_addr"`
	AdminAddr      string        `yaml:"admin_addr"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
	MaxRequestBody int64         `yaml:"max_request_body"`
}

// DBConfig holds database connection settings.
type DBConfig struct {
	URL string `yaml:"url"`
}

// AuthConfig holds authentication settings.
type AuthConfig struct {
	JWTSecret         string        `yaml:"jwt_secret"`
	AccessTokenTTL    time.Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL   time.Duration `yaml:"refresh_token_ttl"`
	AllowRegistration bool          `yaml:"allow_registration"`
	OAuth             OAuthConfig   `yaml:"oauth"`
}

// OAuthConfig holds OAuth provider configurations.
type OAuthConfig struct {
	GitHub OAuthProviderConfig `yaml:"github"`
	Google OAuthProviderConfig `yaml:"google"`
	OIDC   OIDCConfig          `yaml:"oidc"`
}

// OAuthProviderConfig holds client credentials for a standard OAuth provider.
type OAuthProviderConfig struct {
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

// OIDCConfig holds settings for an OpenID Connect provider.
type OIDCConfig struct {
	IssuerURL    string `yaml:"issuer_url"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

// EncryptionConfig holds the encryption key used for at-rest data.
type EncryptionConfig struct {
	Key string `yaml:"key"`
}

// TraceConfig holds HTTP header names used for distributed tracing.
type TraceConfig struct {
	TraceHeader  string `yaml:"trace_header"`
	ThreadHeader string `yaml:"thread_header"`
}

// CollectorConfig holds settings for the metrics/event collector.
type CollectorConfig struct {
	BatchSize     int           `yaml:"batch_size"`
	FlushInterval time.Duration `yaml:"flush_interval"`
	BufferSize    int           `yaml:"buffer_size"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// CORSConfig holds Cross-Origin Resource Sharing settings.
type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// defaults returns a Config populated with all default values.
func defaults() Config {
	return Config{
		Server: ServerConfig{
			ProxyAddr:      ":8080",
			AdminAddr:      ":8081",
			RequestTimeout: 600 * time.Second,
			MaxRequestBody: 52428800, // 50 MB
		},
		Auth: AuthConfig{
			AccessTokenTTL:    15 * time.Minute,
			RefreshTokenTTL:   168 * time.Hour,
			AllowRegistration: true,
		},
		Trace: TraceConfig{
			TraceHeader:  "X-Trace-Id",
			ThreadHeader: "X-Thread-Id",
		},
		Collector: CollectorConfig{
			BatchSize:     100,
			FlushInterval: 1 * time.Second,
			BufferSize:    10000,
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
	}
}

// Load reads YAML configuration from r and merges it on top of the defaults.
// Fields not present in the YAML retain their default values.
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

// LoadFile opens the file at path and delegates to Load.
func LoadFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return Load(f)
}

// ApplyEnvOverrides replaces specific config fields with values from
// MODELSERVER_* environment variables when those variables are non-empty.
func (c *Config) ApplyEnvOverrides() {
	if v := os.Getenv("MODELSERVER_SERVER_PROXY_ADDR"); v != "" {
		c.Server.ProxyAddr = v
	}
	if v := os.Getenv("MODELSERVER_SERVER_ADMIN_ADDR"); v != "" {
		c.Server.AdminAddr = v
	}
	if v := os.Getenv("MODELSERVER_DB_URL"); v != "" {
		c.DB.URL = v
	}
	if v := os.Getenv("MODELSERVER_AUTH_JWT_SECRET"); v != "" {
		c.Auth.JWTSecret = v
	}
	if v := os.Getenv("MODELSERVER_ENCRYPTION_KEY"); v != "" {
		c.Encryption.Key = v
	}
}
