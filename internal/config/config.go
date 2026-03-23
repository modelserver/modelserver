package config

import (
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// Config is the top-level configuration struct.
type Config struct {
	Server     ServerConfig     `yaml:"server"     mapstructure:"server"`
	DB         DBConfig         `yaml:"db"         mapstructure:"db"`
	Auth       AuthConfig       `yaml:"auth"       mapstructure:"auth"`
	Encryption EncryptionConfig `yaml:"encryption" mapstructure:"encryption"`
	Trace      TraceConfig      `yaml:"trace"      mapstructure:"trace"`
	Collector  CollectorConfig  `yaml:"collector"  mapstructure:"collector"`
	Log        LogConfig        `yaml:"log"        mapstructure:"log"`
	CORS       CORSConfig       `yaml:"cors"       mapstructure:"cors"`
	Billing    BillingConfig    `yaml:"billing"    mapstructure:"billing"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	ProxyAddr      string        `yaml:"proxy_addr"       mapstructure:"proxy_addr"`
	AdminAddr      string        `yaml:"admin_addr"       mapstructure:"admin_addr"`
	RequestTimeout time.Duration `yaml:"request_timeout"  mapstructure:"request_timeout"`
	MaxRequestBody int64         `yaml:"max_request_body" mapstructure:"max_request_body"`
}

// DBConfig holds database connection settings.
type DBConfig struct {
	URL string `yaml:"url" mapstructure:"url"`
}

// AuthConfig holds authentication settings.
type AuthConfig struct {
	JWTSecret        string        `yaml:"jwt_secret"          mapstructure:"jwt_secret"`
	AccessTokenTTL   time.Duration `yaml:"access_token_ttl"    mapstructure:"access_token_ttl"`
	RefreshTokenTTL  time.Duration `yaml:"refresh_token_ttl"   mapstructure:"refresh_token_ttl"`
	OAuth            OAuthConfig   `yaml:"oauth"               mapstructure:"oauth"`
	LoginDescription string        `yaml:"login_description"   mapstructure:"login_description"`
	LoginFooterHTML  string        `yaml:"login_footer_html"   mapstructure:"login_footer_html"`
	GitHubURL        string        `yaml:"github_url"          mapstructure:"github_url"`
}

// OAuthConfig holds OAuth provider configurations.
type OAuthConfig struct {
	GitHub OAuthProviderConfig `yaml:"github" mapstructure:"github"`
	Google OAuthProviderConfig `yaml:"google" mapstructure:"google"`
	OIDC   OIDCConfig          `yaml:"oidc"   mapstructure:"oidc"`
	Hydra  HydraConfig         `yaml:"hydra"  mapstructure:"hydra"`
}

// OAuthProviderConfig holds client credentials for a standard OAuth provider.
type OAuthProviderConfig struct {
	ClientID     string `yaml:"client_id"     mapstructure:"client_id"`
	ClientSecret string `yaml:"client_secret" mapstructure:"client_secret"`
}

// OIDCConfig holds settings for an OpenID Connect provider.
type OIDCConfig struct {
	IssuerURL    string `yaml:"issuer_url"    mapstructure:"issuer_url"`
	ClientID     string `yaml:"client_id"     mapstructure:"client_id"`
	ClientSecret string `yaml:"client_secret" mapstructure:"client_secret"`
	RedirectURI  string `yaml:"redirect_uri"  mapstructure:"redirect_uri"`
	DisplayName  string `yaml:"display_name"  mapstructure:"display_name"`
}

// HydraConfig holds settings for an Ory Hydra OAuth2 server.
type HydraConfig struct {
	AdminURL string `yaml:"admin_url" mapstructure:"admin_url"`
}

// EncryptionConfig holds the encryption key used for at-rest data.
type EncryptionConfig struct {
	Key string `yaml:"key" mapstructure:"key"`
}

// TraceConfig holds HTTP header names used for distributed tracing.
type TraceConfig struct {
	TraceHeader            string        `yaml:"trace_header"              mapstructure:"trace_header"`
	ExtraTraceHeaders      []string      `yaml:"extra_trace_headers"       mapstructure:"extra_trace_headers"`
	ExtraTraceBodyFields   []string      `yaml:"extra_trace_body_fields"   mapstructure:"extra_trace_body_fields"`
	ClaudeCodeTraceEnabled bool          `yaml:"claude_code_trace_enabled" mapstructure:"claude_code_trace_enabled"`
	CodexTraceEnabled      bool          `yaml:"codex_trace_enabled"       mapstructure:"codex_trace_enabled"`
	OpenClawTraceEnabled   bool          `yaml:"openclaw_trace_enabled"    mapstructure:"openclaw_trace_enabled"`
	RequireSession         bool          `yaml:"require_session"           mapstructure:"require_session"`
	SessionTTL             time.Duration `yaml:"session_ttl"               mapstructure:"session_ttl"`
}

// CollectorConfig holds settings for the metrics/event collector.
type CollectorConfig struct {
	BatchSize     int           `yaml:"batch_size"      mapstructure:"batch_size"`
	FlushInterval time.Duration `yaml:"flush_interval"  mapstructure:"flush_interval"`
	BufferSize    int           `yaml:"buffer_size"     mapstructure:"buffer_size"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string `yaml:"level"  mapstructure:"level"`
	Format string `yaml:"format" mapstructure:"format"`
}

// CORSConfig holds Cross-Origin Resource Sharing settings.
type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins" mapstructure:"allowed_origins"`
}

// BillingConfig holds settings for the billing and payment integration.
type BillingConfig struct {
	WebhookSecret string `yaml:"webhook_secret"  mapstructure:"webhook_secret"`
	PaymentAPIURL string `yaml:"payment_api_url"  mapstructure:"payment_api_url"`
	PaymentAPIKey string `yaml:"payment_api_key"  mapstructure:"payment_api_key"`
	NotifyURL     string `yaml:"notify_url"       mapstructure:"notify_url"`
	ReturnURL     string `yaml:"return_url"       mapstructure:"return_url"`
}

// setDefaults registers all default values with the viper instance.
// Keys without meaningful defaults are bound via BindEnv so they can
// still be populated from MODELSERVER_* environment variables.
func setDefaults(v *viper.Viper) {
	// Server
	v.SetDefault("server.proxy_addr", ":8080")
	v.SetDefault("server.admin_addr", ":8081")
	v.SetDefault("server.request_timeout", 600*time.Second)
	v.SetDefault("server.max_request_body", 52428800)

	// DB
	_ = v.BindEnv("db.url")

	// Auth
	_ = v.BindEnv("auth.jwt_secret")
	v.SetDefault("auth.access_token_ttl", 15*time.Minute)
	v.SetDefault("auth.refresh_token_ttl", 168*time.Hour)

	// OAuth / OIDC
	// BindEnv with explicit env var names so both the canonical form
	// (MODELSERVER_AUTH_OAUTH_OIDC_*) and the short form (MODELSERVER_AUTH_OIDC_*)
	// work. The short form is more user-friendly for docker-compose.
	_ = v.BindEnv("auth.oauth.github.client_id")
	_ = v.BindEnv("auth.oauth.github.client_secret")
	_ = v.BindEnv("auth.oauth.google.client_id")
	_ = v.BindEnv("auth.oauth.google.client_secret")
	_ = v.BindEnv("auth.oauth.oidc.issuer_url", "MODELSERVER_AUTH_OIDC_ISSUER_URL")
	_ = v.BindEnv("auth.oauth.oidc.client_id", "MODELSERVER_AUTH_OIDC_CLIENT_ID")
	_ = v.BindEnv("auth.oauth.oidc.client_secret", "MODELSERVER_AUTH_OIDC_CLIENT_SECRET")
	_ = v.BindEnv("auth.oauth.oidc.redirect_uri", "MODELSERVER_AUTH_OIDC_REDIRECT_URI")
	_ = v.BindEnv("auth.oauth.oidc.display_name", "MODELSERVER_AUTH_OIDC_DISPLAY_NAME")
	_ = v.BindEnv("auth.oauth.hydra.admin_url", "HYDRA_ADMIN_URL")
	_ = v.BindEnv("auth.login_description")
	_ = v.BindEnv("auth.login_footer_html")
	_ = v.BindEnv("auth.github_url")

	// Encryption
	_ = v.BindEnv("encryption.key")

	// Trace
	v.SetDefault("trace.trace_header", "X-Trace-Id")
	_ = v.BindEnv("trace.extra_trace_headers")
	_ = v.BindEnv("trace.extra_trace_body_fields")
	v.SetDefault("trace.claude_code_trace_enabled", true)
	v.SetDefault("trace.codex_trace_enabled", true)
	v.SetDefault("trace.openclaw_trace_enabled", true)
	v.SetDefault("trace.require_session", false)
	v.SetDefault("trace.session_ttl", 24*time.Hour)

	// Collector
	v.SetDefault("collector.batch_size", 100)
	v.SetDefault("collector.flush_interval", 1*time.Second)
	v.SetDefault("collector.buffer_size", 10000)

	// Log
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

	// CORS
	_ = v.BindEnv("cors.allowed_origins")

	// Billing
	_ = v.BindEnv("billing.webhook_secret")
	_ = v.BindEnv("billing.payment_api_url")
	_ = v.BindEnv("billing.payment_api_key")
	_ = v.BindEnv("billing.notify_url")
	_ = v.BindEnv("billing.return_url")
}

// newViper creates a pre-configured viper instance with defaults, env binding,
// and the MODELSERVER_ prefix. Environment variables are automatically mapped:
//
//	MODELSERVER_DB_URL           -> db.url
//	MODELSERVER_AUTH_JWT_SECRET  -> auth.jwt_secret
//	MODELSERVER_AUTH_OAUTH_OIDC_ISSUER_URL -> auth.oauth.oidc.issuer_url
func newViper() *viper.Viper {
	v := viper.New()
	v.SetConfigType("yaml")
	v.SetEnvPrefix("MODELSERVER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	setDefaults(v)
	return v
}

func unmarshal(v *viper.Viper) (*Config, error) {
	var cfg Config
	if err := v.Unmarshal(&cfg, viper.DecodeHook(
		mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	)); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Load reads YAML configuration from a byte slice and merges it with defaults
// and environment variables. Env vars always take highest priority.
func Load(data []byte) (*Config, error) {
	v := newViper()
	if len(data) > 0 {
		v.SetConfigType("yaml")
		if err := v.ReadConfig(strings.NewReader(string(data))); err != nil {
			return nil, err
		}
	}
	return unmarshal(v)
}

// LoadFile reads YAML configuration from the file at path and merges it with
// defaults and environment variables. Env vars always take highest priority.
func LoadFile(path string) (*Config, error) {
	v := newViper()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}
	return unmarshal(v)
}
