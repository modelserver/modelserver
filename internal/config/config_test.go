package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestLoadDefaults verifies that an empty YAML document yields all defaults.
func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(strings.NewReader(""))
	if err != nil {
		t.Fatalf("Load empty reader: %v", err)
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
	if !cfg.Auth.AllowRegistration {
		t.Error("Auth.AllowRegistration = false, want true")
	}
	if cfg.Trace.TraceHeader != "X-Trace-Id" {
		t.Errorf("Trace.TraceHeader = %q, want %q", cfg.Trace.TraceHeader, "X-Trace-Id")
	}
	if cfg.Trace.ThreadHeader != "X-Thread-Id" {
		t.Errorf("Trace.ThreadHeader = %q, want %q", cfg.Trace.ThreadHeader, "X-Thread-Id")
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
	yaml := `
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
  allow_registration: false
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
  thread_header: "X-Custom-Thread"
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
`

	cfg, err := Load(strings.NewReader(yaml))
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
	if cfg.Auth.AllowRegistration {
		t.Error("Auth.AllowRegistration = true, want false")
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
	if cfg.Trace.ThreadHeader != "X-Custom-Thread" {
		t.Errorf("Trace.ThreadHeader = %q, want %q", cfg.Trace.ThreadHeader, "X-Custom-Thread")
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

// TestApplyEnvOverrides verifies that environment variables override config values.
func TestApplyEnvOverrides(t *testing.T) {
	cfg, err := Load(strings.NewReader(""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Set env vars for this test only; clean up afterward.
	envVars := map[string]string{
		"MODELSERVER_SERVER_PROXY_ADDR": ":5050",
		"MODELSERVER_SERVER_ADMIN_ADDR": ":5051",
		"MODELSERVER_DB_URL":            "postgres://env-override/db",
		"MODELSERVER_AUTH_JWT_SECRET":   "env-jwt-secret",
		"MODELSERVER_ENCRYPTION_KEY":    "env-enc-key",
	}
	for k, v := range envVars {
		t.Setenv(k, v)
	}

	cfg.ApplyEnvOverrides()

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

// TestApplyEnvOverridesPartial verifies that unset env vars do not overwrite existing values.
func TestApplyEnvOverridesPartial(t *testing.T) {
	yaml := `
server:
  proxy_addr: ":6060"
auth:
  jwt_secret: "yaml-secret"
`
	cfg, err := Load(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Only set one env var; the others should remain as loaded from YAML.
	t.Setenv("MODELSERVER_SERVER_ADMIN_ADDR", ":6061")

	cfg.ApplyEnvOverrides()

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
