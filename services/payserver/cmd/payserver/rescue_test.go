package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// buildRescueBinary compiles the binary once per test for use across
// multiple subprocess invocations of `admin rescue ...`.
func buildRescueBinary(t *testing.T) string {
	t.Helper()
	bin := t.TempDir() + "/payserver-test"
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	return bin
}

// TestRescue_HappyPath confirms the subcommand parses args + prints a
// cookie line. The encoded token's verifiability is already tested in
// internal/server.
func TestRescue_HappyPath(t *testing.T) {
	bin := buildRescueBinary(t)
	c := exec.Command(bin, "admin", "rescue", "--email", "test@example.com", "--ttl", "1h")
	c.Env = append(os.Environ(), "PAYSERVER_OIDC_SESSION_SECRET=test-secret-32-bytes-padded-okay!")
	// CombinedOutput captures both streams; the token must be present
	// somewhere (on stderr by design) but TestRescue_TokenNotOnStdout
	// pins the channel.
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("rescue exec: %v: %s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "payserver_admin_session=") {
		t.Errorf("output missing cookie line:\n%s", s)
	}
}

// TestRescue_TokenNotOnStdout pins the security contract: the bearer-
// equivalent admin session token must NOT be emitted on stdout, because
// container log collectors typically scrape only stdout and we don't want
// the token archived to log storage. The audit JSON record on stdout
// must continue to NOT contain the token.
func TestRescue_TokenNotOnStdout(t *testing.T) {
	bin := buildRescueBinary(t)
	c := exec.Command(bin, "admin", "rescue", "--email", "test@example.com", "--ttl", "1h")
	c.Env = append(os.Environ(), "PAYSERVER_OIDC_SESSION_SECRET=test-secret-32-bytes-padded-okay!")
	out, err := c.Output() // stdout only
	if err != nil {
		t.Fatalf("rescue exec: %v", err)
	}
	if strings.Contains(string(out), "payserver_admin_session=") {
		t.Errorf("stdout must not contain the session token (it goes to stderr):\n%s", out)
	}
}

// TestRescue_TTLExceedsMaxRejected guards the auto-review HIGH finding:
// operator-supplied --ttl must be capped to 24h. A 1000h request must
// exit non-zero with a message naming the limit.
func TestRescue_TTLExceedsMaxRejected(t *testing.T) {
	bin := buildRescueBinary(t)
	c := exec.Command(bin, "admin", "rescue", "--email", "test@example.com", "--ttl", "1000h")
	c.Env = append(os.Environ(), "PAYSERVER_OIDC_SESSION_SECRET=test-secret-32-bytes-padded-okay!")
	out, err := c.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit on oversize ttl; stdout:\n%s", out)
	}
	if !strings.Contains(string(out), "--ttl must be") {
		t.Errorf("error message missing TTL hint:\n%s", out)
	}
}

// TestRescue_ZeroTTLRejected confirms --ttl=0 / negative is rejected.
func TestRescue_ZeroTTLRejected(t *testing.T) {
	bin := buildRescueBinary(t)
	c := exec.Command(bin, "admin", "rescue", "--email", "test@example.com", "--ttl", "0")
	c.Env = append(os.Environ(), "PAYSERVER_OIDC_SESSION_SECRET=test-secret-32-bytes-padded-okay!")
	out, err := c.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit on zero ttl; stdout:\n%s", out)
	}
}

// TestRescue_AuditLogOnStdout confirms the rescue audit record is
// emitted on stdout as structured JSON so container log collectors
// (which typically scrape stdout only) capture it.
func TestRescue_AuditLogOnStdout(t *testing.T) {
	bin := buildRescueBinary(t)
	c := exec.Command(bin, "admin", "rescue", "--email", "audit@example.com", "--ttl", "1h")
	c.Env = append(os.Environ(), "PAYSERVER_OIDC_SESSION_SECRET=test-secret-32-bytes-padded-okay!")
	out, err := c.Output() // stdout only
	if err != nil {
		t.Fatalf("rescue exec: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"msg":"admin rescue session issued"`) {
		t.Errorf("stdout missing audit msg field:\n%s", s)
	}
	if !strings.Contains(s, `"email":"audit@example.com"`) {
		t.Errorf("stdout missing audit email field:\n%s", s)
	}
}

// TestRescue_ShortSecretRejected confirms PAYSERVER_OIDC_SESSION_SECRET
// below the 32-char minimum (matching NewOIDCAuth) is rejected.
func TestRescue_ShortSecretRejected(t *testing.T) {
	bin := buildRescueBinary(t)
	c := exec.Command(bin, "admin", "rescue", "--email", "test@example.com", "--ttl", "1h")
	c.Env = append(os.Environ(), "PAYSERVER_OIDC_SESSION_SECRET=too-short")
	out, err := c.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit on short secret; stdout:\n%s", out)
	}
	if !strings.Contains(string(out), "at least") {
		t.Errorf("error message missing length hint:\n%s", out)
	}
}
