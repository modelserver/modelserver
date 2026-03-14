package config

import (
	"os"
	"testing"
	"time"
)

// TestLoadDefaults verifies that an empty config yields all defaults.
func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load nil: %v", err)
	}

	if cfg.Server.ProxyAddr != ":8080" {
		t.Errorf("Server.ProxyAddr = %q, want %q", cfg.Server.ProxyAddr, ":8080")
	}
	if cfg.Server.AdminAddr != ":8081" {
		t.Errorf("Server.AdminAddr = %q, want %q", cfg.Server.AdminAddr, ":8081")
	}
	if cfg.Server.RequestTimeout != 600*time.Second {
		t.Errorf("Server.RequestTimeout = %v, want 600s", cfg.Server.RequestTimeout)
	}
	if cfg.Server.MaxRequestBody != 52428800 {
		t.Errorf("Server.MaxRequestBody = %d, want 52428800", cfg.Server.MaxRequestBody)
	}
	if cfg.Auth.AccessTokenTTL != 15*time.Minute {
		t.Errorf("Auth.AccessTokenTTL = %v, want 15m", cfg.Auth.AccessTokenTTL)
	}
	if cfg.Auth.RefreshTokenTTL != 168*time.Hour {
		t.Errorf("Auth.RefreshTokenTTL = %v, want 168h", cfg.Auth.RefreshTokenTTL)
	}
	if cfg.Trace.TraceHeader != "X-Trace-Id" {
		t.Errorf("Trace.TraceHeader = %q, want %q", cfg.Trace.TraceHeader, "X-Trace-Id")
	}
	if cfg.Collector.BatchSize != 100 {
		t.Errorf("Collector.BatchSize = %d, want 100", cfg.Collector.BatchSize)
	}
	if cfg.Collector.FlushInterval != time.Second {
		t.Errorf("Collector.FlushInterval = %v, want 1s", cfg.Collector.FlushInterval)
	}
	if cfg.Collector.BufferSize != 10000 {
		t.Errorf("Collector.BufferSize = %d, want 10000", cfg.Collector.BufferSize)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "info")
	}
	if cfg.Log.Format != "json" {
		t.Errorf("Log.Format = %q, want %q", cfg.Log.Format, "json")
	}
}

// TestLoadCustomValues verifies that YAML values override defaults.
func TestLoadCustomValues(t *testing.T) {
	yaml := []byte(`
server:
  proxy_addr: ":9090"
  admin_addr: ":9091"
  request_timeout: 30s
  max_request_body: 1048576
db:
  url: "postgres://user:pass@localhost/mydb"
auth:
  jwt_secret: "supersecret"
  access_token_ttl: 5m
  refresh_token_ttl: 24h
  oauth:
    github:
      client_id: "gh-id"
      client_secret: "gh-secret"
    google:
      client_id: "goog-id"
      client_secret: "goog-secret"
    oidc:
      issuer_url: "https://accounts.example.com"
      client_id: "oidc-id"
      client_secret: "oidc-secret"
encryption:
  key: "my-enc-key"
trace:
  trace_header: "X-Custom-Trace"
collector:
  batch_size: 50
  flush_interval: 500ms
  buffer_size: 5000
log:
  level: "debug"
  format: "text"
cors:
  allowed_origins:
    - "https://app.example.com"
    - "https://admin.example.com"
`)

	cfg, err := Load(yaml)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.ProxyAddr != ":9090" {
		t.Errorf("Server.ProxyAddr = %q, want %q", cfg.Server.ProxyAddr, ":9090")
	}
	if cfg.Server.AdminAddr != ":9091" {
		t.Errorf("Server.AdminAddr = %q, want %q", cfg.Server.AdminAddr, ":9091")
	}
	if cfg.Server.RequestTimeout != 30*time.Second {
		t.Errorf("Server.RequestTimeout = %v, want 30s", cfg.Server.RequestTimeout)
	}
	if cfg.Server.MaxRequestBody != 1048576 {
		t.Errorf("Server.MaxRequestBody = %d, want 1048576", cfg.Server.MaxRequestBody)
	}
	if cfg.DB.URL != "postgres://user:pass@localhost/mydb" {
		t.Errorf("DB.URL = %q", cfg.DB.URL)
	}
	if cfg.Auth.JWTSecret != "supersecret" {
		t.Errorf("Auth.JWTSecret = %q", cfg.Auth.JWTSecret)
	}
	if cfg.Auth.AccessTokenTTL != 5*time.Minute {
		t.Errorf("Auth.AccessTokenTTL = %v, want 5m", cfg.Auth.AccessTokenTTL)
	}
	if cfg.Auth.RefreshTokenTTL != 24*time.Hour {
		t.Errorf("Auth.RefreshTokenTTL = %v, want 24h", cfg.Auth.RefreshTokenTTL)
	}
	if cfg.Auth.OAuth.GitHub.ClientID != "gh-id" {
		t.Errorf("OAuth.GitHub.ClientID = %q, want %q", cfg.Auth.OAuth.GitHub.ClientID, "gh-id")
	}
	if cfg.Auth.OAuth.GitHub.ClientSecret != "gh-secret" {
		t.Errorf("OAuth.GitHub.ClientSecret = %q, want %q", cfg.Auth.OAuth.GitHub.ClientSecret, "gh-secret")
	}
	if cfg.Auth.OAuth.Google.ClientID != "goog-id" {
		t.Errorf("OAuth.Google.ClientID = %q", cfg.Auth.OAuth.Google.ClientID)
	}
	if cfg.Auth.OAuth.OIDC.IssuerURL != "https://accounts.example.com" {
		t.Errorf("OAuth.OIDC.IssuerURL = %q", cfg.Auth.OAuth.OIDC.IssuerURL)
	}
	if cfg.Auth.OAuth.OIDC.ClientID != "oidc-id" {
		t.Errorf("OAuth.OIDC.ClientID = %q", cfg.Auth.OAuth.OIDC.ClientID)
	}
	if cfg.Encryption.Key != "my-enc-key" {
		t.Errorf("Encryption.Key = %q", cfg.Encryption.Key)
	}
	if cfg.Trace.TraceHeader != "X-Custom-Trace" {
		t.Errorf("Trace.TraceHeader = %q, want %q", cfg.Trace.TraceHeader, "X-Custom-Trace")
	}
	if cfg.Collector.BatchSize != 50 {
		t.Errorf("Collector.BatchSize = %d, want 50", cfg.Collector.BatchSize)
	}
	if cfg.Collector.FlushInterval != 500*time.Millisecond {
		t.Errorf("Collector.FlushInterval = %v, want 500ms", cfg.Collector.FlushInterval)
	}
	if cfg.Collector.BufferSize != 5000 {
		t.Errorf("Collector.BufferSize = %d, want 5000", cfg.Collector.BufferSize)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "debug")
	}
	if cfg.Log.Format != "text" {
		t.Errorf("Log.Format = %q, want %q", cfg.Log.Format, "text")
	}
	if len(cfg.CORS.AllowedOrigins) != 2 {
		t.Errorf("CORS.AllowedOrigins len = %d, want 2", len(cfg.CORS.AllowedOrigins))
	} else {
		if cfg.CORS.AllowedOrigins[0] != "https://app.example.com" {
			t.Errorf("CORS.AllowedOrigins[0] = %q", cfg.CORS.AllowedOrigins[0])
		}
		if cfg.CORS.AllowedOrigins[1] != "https://admin.example.com" {
			t.Errorf("CORS.AllowedOrigins[1] = %q", cfg.CORS.AllowedOrigins[1])
		}
	}
}

// TestLoadFile verifies LoadFile reads and parses a YAML file correctly.
func TestLoadFile(t *testing.T) {
	content := `
server:
  proxy_addr: ":7070"
db:
  url: "postgres://file-test/db"
log:
  level: "warn"
`
	f, err := os.CreateTemp(t.TempDir(), "config-*.yml")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()

	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("WriteString: %v", err)
	}

	cfg, err := LoadFile(f.Name())
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	if cfg.Server.ProxyAddr != ":7070" {
		t.Errorf("Server.ProxyAddr = %q, want %q", cfg.Server.ProxyAddr, ":7070")
	}
	// AdminAddr should retain its default since it wasn't set in the file.
	if cfg.Server.AdminAddr != ":8081" {
		t.Errorf("Server.AdminAddr = %q, want %q (default)", cfg.Server.AdminAddr, ":8081")
	}
	if cfg.DB.URL != "postgres://file-test/db" {
		t.Errorf("DB.URL = %q", cfg.DB.URL)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "warn")
	}
}

// TestLoadFileNotFound verifies LoadFile returns an error for missing files.
func TestLoadFileNotFound(t *testing.T) {
	_, err := LoadFile("/nonexistent/path/config.yml")
	if err == nil {
		t.Error("LoadFile with missing file: expected error, got nil")
	}
}

// TestEnvOverrides verifies that environment variables take priority over YAML and defaults.
func TestEnvOverrides(t *testing.T) {
	t.Setenv("MODELSERVER_SERVER_PROXY_ADDR", ":5050")
	t.Setenv("MODELSERVER_SERVER_ADMIN_ADDR", ":5051")
	t.Setenv("MODELSERVER_DB_URL", "postgres://env-override/db")
	t.Setenv("MODELSERVER_AUTH_JWT_SECRET", "env-jwt-secret")
	t.Setenv("MODELSERVER_ENCRYPTION_KEY", "env-enc-key")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Server.ProxyAddr != ":5050" {
		t.Errorf("Server.ProxyAddr = %q, want %q", cfg.Server.ProxyAddr, ":5050")
	}
	if cfg.Server.AdminAddr != ":5051" {
		t.Errorf("Server.AdminAddr = %q, want %q", cfg.Server.AdminAddr, ":5051")
	}
	if cfg.DB.URL != "postgres://env-override/db" {
		t.Errorf("DB.URL = %q", cfg.DB.URL)
	}
	if cfg.Auth.JWTSecret != "env-jwt-secret" {
		t.Errorf("Auth.JWTSecret = %q, want %q", cfg.Auth.JWTSecret, "env-jwt-secret")
	}
	if cfg.Encryption.Key != "env-enc-key" {
		t.Errorf("Encryption.Key = %q, want %q", cfg.Encryption.Key, "env-enc-key")
	}
}

// TestEnvOverridesPartial verifies that unset env vars do not overwrite YAML values.
func TestEnvOverridesPartial(t *testing.T) {
	yaml := []byte(`
server:
  proxy_addr: ":6060"
auth:
  jwt_secret: "yaml-secret"
`)

	// Only set one env var; the others should remain as loaded from YAML.
	t.Setenv("MODELSERVER_SERVER_ADMIN_ADDR", ":6061")

	cfg, err := Load(yaml)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Was set by env.
	if cfg.Server.AdminAddr != ":6061" {
		t.Errorf("Server.AdminAddr = %q, want %q", cfg.Server.AdminAddr, ":6061")
	}
	// Was set by YAML and not overridden.
	if cfg.Server.ProxyAddr != ":6060" {
		t.Errorf("Server.ProxyAddr = %q, want %q", cfg.Server.ProxyAddr, ":6060")
	}
	// Was set by YAML and not overridden.
	if cfg.Auth.JWTSecret != "yaml-secret" {
		t.Errorf("Auth.JWTSecret = %q, want %q", cfg.Auth.JWTSecret, "yaml-secret")
	}
}

// TestEnvOIDC verifies that OIDC settings can be provided via the short-form env vars.
func TestEnvOIDC(t *testing.T) {
	t.Setenv("MODELSERVER_AUTH_OIDC_ISSUER_URL", "https://idp.example.com")
	t.Setenv("MODELSERVER_AUTH_OIDC_CLIENT_ID", "my-client")
	t.Setenv("MODELSERVER_AUTH_OIDC_CLIENT_SECRET", "my-secret")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Auth.OAuth.OIDC.IssuerURL != "https://idp.example.com" {
		t.Errorf("OIDC.IssuerURL = %q", cfg.Auth.OAuth.OIDC.IssuerURL)
	}
	if cfg.Auth.OAuth.OIDC.ClientID != "my-client" {
		t.Errorf("OIDC.ClientID = %q", cfg.Auth.OAuth.OIDC.ClientID)
	}
	if cfg.Auth.OAuth.OIDC.ClientSecret != "my-secret" {
		t.Errorf("OIDC.ClientSecret = %q", cfg.Auth.OAuth.OIDC.ClientSecret)
	}
}

// TestTraceConfigDefaults verifies that new TraceConfig fields have correct defaults.
func TestTraceConfigDefaults(t *testing.T) {
	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Trace.ExtraTraceHeaders) != 0 {
		t.Errorf("Trace.ExtraTraceHeaders = %v, want empty", cfg.Trace.ExtraTraceHeaders)
	}
	if len(cfg.Trace.ExtraTraceBodyFields) != 0 {
		t.Errorf("Trace.ExtraTraceBodyFields = %v, want empty", cfg.Trace.ExtraTraceBodyFields)
	}
	if !cfg.Trace.ClaudeCodeTraceEnabled {
		t.Error("Trace.ClaudeCodeTraceEnabled = false, want true")
	}
	if !cfg.Trace.CodexTraceEnabled {
		t.Error("Trace.CodexTraceEnabled = false, want true")
	}
}

// TestTraceConfigYAML verifies that TraceConfig fields are populated from YAML.
func TestTraceConfigYAML(t *testing.T) {
	yaml := []byte(`
trace:
  trace_header: "X-Custom-Trace"
  extra_trace_headers:
    - "X-Request-Id"
    - "X-Correlation-Id"
  extra_trace_body_fields:
    - "metadata.trace_id"
    - "context.request_id"
  claude_code_trace_enabled: true
  codex_trace_enabled: true
`)

	cfg, err := Load(yaml)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Trace.TraceHeader != "X-Custom-Trace" {
		t.Errorf("Trace.TraceHeader = %q, want %q", cfg.Trace.TraceHeader, "X-Custom-Trace")
	}
	if len(cfg.Trace.ExtraTraceHeaders) != 2 {
		t.Fatalf("Trace.ExtraTraceHeaders len = %d, want 2", len(cfg.Trace.ExtraTraceHeaders))
	}
	if cfg.Trace.ExtraTraceHeaders[0] != "X-Request-Id" {
		t.Errorf("ExtraTraceHeaders[0] = %q, want %q", cfg.Trace.ExtraTraceHeaders[0], "X-Request-Id")
	}
	if cfg.Trace.ExtraTraceHeaders[1] != "X-Correlation-Id" {
		t.Errorf("ExtraTraceHeaders[1] = %q, want %q", cfg.Trace.ExtraTraceHeaders[1], "X-Correlation-Id")
	}
	if len(cfg.Trace.ExtraTraceBodyFields) != 2 {
		t.Fatalf("Trace.ExtraTraceBodyFields len = %d, want 2", len(cfg.Trace.ExtraTraceBodyFields))
	}
	if cfg.Trace.ExtraTraceBodyFields[0] != "metadata.trace_id" {
		t.Errorf("ExtraTraceBodyFields[0] = %q", cfg.Trace.ExtraTraceBodyFields[0])
	}
	if !cfg.Trace.ClaudeCodeTraceEnabled {
		t.Error("Trace.ClaudeCodeTraceEnabled = false, want true")
	}
	if !cfg.Trace.CodexTraceEnabled {
		t.Error("Trace.CodexTraceEnabled = false, want true")
	}
}

// TestTraceConfigEnv verifies TraceConfig fields can be set via environment variables.
func TestTraceConfigEnv(t *testing.T) {
	t.Setenv("MODELSERVER_TRACE_EXTRA_TRACE_HEADERS", "X-Req-Id,X-Corr-Id")
	t.Setenv("MODELSERVER_TRACE_EXTRA_TRACE_BODY_FIELDS", "meta.trace_id")
	t.Setenv("MODELSERVER_TRACE_CLAUDE_CODE_TRACE_ENABLED", "true")
	t.Setenv("MODELSERVER_TRACE_CODEX_TRACE_ENABLED", "true")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Trace.ExtraTraceHeaders) != 2 {
		t.Fatalf("Trace.ExtraTraceHeaders len = %d, want 2", len(cfg.Trace.ExtraTraceHeaders))
	}
	if cfg.Trace.ExtraTraceHeaders[0] != "X-Req-Id" {
		t.Errorf("ExtraTraceHeaders[0] = %q, want %q", cfg.Trace.ExtraTraceHeaders[0], "X-Req-Id")
	}
	if !cfg.Trace.ClaudeCodeTraceEnabled {
		t.Error("Trace.ClaudeCodeTraceEnabled = false, want true")
	}
	if !cfg.Trace.CodexTraceEnabled {
		t.Error("Trace.CodexTraceEnabled = false, want true")
	}
}
