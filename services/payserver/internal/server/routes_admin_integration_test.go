package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/config"
)

// newTestOIDCAuth returns a minimal *OIDCAuth wired with a known session
// secret so the test can mint a valid AdminSession cookie directly. It
// skips OIDC provider discovery by constructing the struct in-package
// (we're whitebox), avoiding any network dependency.
func newTestOIDCAuth(t *testing.T, secret string) *OIDCAuth {
	t.Helper()
	return &OIDCAuth{
		sessionSecret: []byte(secret),
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func mintSessionCookie(t *testing.T, secret string) *http.Cookie {
	t.Helper()
	sess := AdminSession{
		Email:     "test@example.com",
		Name:      "Test",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	val, err := EncodeSession(sess, []byte(secret))
	if err != nil {
		t.Fatalf("encode session: %v", err)
	}
	return &http.Cookie{Name: adminSessionCookieName, Value: val}
}

// buildRouter wires NewRouter with a real store + OIDC + the supplied
// dist FS. Returns an httptest.Server the caller must Close.
func buildRouter(t *testing.T, distFS fstest.MapFS) (*httptest.Server, string) {
	t.Helper()
	st := openTestStoreServer(t) // skips if PAYSERVER_TEST_DB_URL not set
	const secret = "test-session-secret-32-bytes-padded-ok!!"
	auth := newTestOIDCAuth(t, secret)
	cfg := Config{
		Store:       st,
		OIDCAuth:    auth,
		AdminDistFS: distFS,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv := httptest.NewServer(NewRouter(cfg))
	t.Cleanup(srv.Close)
	return srv, secret
}

func do(t *testing.T, srv *httptest.Server, method, path string, cookie *http.Cookie, accept string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	// Don't follow redirects (login redirects would mask real assertions).
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

// TestAdminRoutes_JSONEndpointsNotShadowedBySPA is the load-bearing
// regression test for the SPA-shadowing bug: GET /admin/tenants must
// return JSON, not the SPA HTML shell.
func TestAdminRoutes_JSONEndpointsNotShadowedBySPA(t *testing.T) {
	distFS := fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte("<html>SPA SHELL</html>")},
		"assets/test.css": &fstest.MapFile{Data: []byte("body{}"), Mode: 0o644},
	}
	srv, secret := buildRouter(t, distFS)
	cookie := mintSessionCookie(t, secret)

	t.Run("tenants list returns JSON not SPA", func(t *testing.T) {
		resp := do(t, srv, "GET", "/admin/tenants", cookie, "application/json")
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, want application/json; body = %s", ct, body)
		}
		if strings.Contains(string(body), "SPA SHELL") {
			t.Errorf("body contains SPA HTML — JSON route was shadowed; body = %s", body)
		}
		// Security headers (Fix 12).
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
		}
		if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("X-Frame-Options = %q, want DENY", got)
		}
	})

	t.Run("tenants/{id} returns JSON 404 not SPA", func(t *testing.T) {
		resp := do(t, srv, "GET", "/admin/tenants/00000000-0000-0000-0000-000000000000", cookie, "application/json")
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, want application/json; body = %s", ct, body)
		}
		if strings.Contains(string(body), "SPA SHELL") {
			t.Errorf("body contains SPA HTML; body = %s", body)
		}
	})

	t.Run("payments list returns JSON", func(t *testing.T) {
		resp := do(t, srv, "GET", "/admin/payments", cookie, "application/json")
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q, want application/json; body = %s", ct, body)
		}
	})

	t.Run("unknown SPA path falls back to index.html", func(t *testing.T) {
		resp := do(t, srv, "GET", "/admin/some/deep/spa/path", cookie, "text/html")
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", ct)
		}
		if !strings.Contains(string(body), "SPA SHELL") {
			t.Errorf("expected SPA HTML body, got: %s", body)
		}
	})

	t.Run("assets reachable without session", func(t *testing.T) {
		resp := do(t, srv, "GET", "/admin/assets/test.css", nil, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "body{}" {
			t.Errorf("body = %q", body)
		}
	})
}

// TestAdminRoutes_NoOIDC_NoAdminMount confirms /admin/* 404s when
// OIDCAuth is nil even if AdminDistFS is set.
func TestAdminRoutes_NoOIDC_NoAdminMount(t *testing.T) {
	st := openTestStoreServer(t)
	distFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>SPA</html>")},
	}
	cfg := Config{
		Store:       st,
		OIDCAuth:    nil,
		AdminDistFS: distFS,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv := httptest.NewServer(NewRouter(cfg))
	defer srv.Close()

	resp := do(t, srv, "GET", "/admin/tenants", nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (no admin without OIDC)", resp.StatusCode)
	}
}

// Compile-time guard: silence unused-import on config (kept for future
// table-driven cases).
var _ = config.Config{}
