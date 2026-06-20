package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxCallbackURLLen = 2048

// validateCallbackURL is a light-weight defense-in-depth check applied at
// Send time. The admin write path (handle_admin.go) is the primary
// gatekeeper; this is a second wall in case a tenant row was inserted by
// a path that didn't validate (e.g. migration 002's default-tenant
// bootstrap from legacy config). Rejects:
//   - non-http(s) schemes
//   - missing host
//   - embedded userinfo
//   - oversize URLs (>2048 chars)
//   - literal IP that is loopback/private/link-local/multicast/unspecified
//     (the SSRF guard — protects against tenant rows pointing at
//     169.254.169.254 cloud metadata, 127.0.0.1 admin interfaces, etc.)
//
// DNS resolution of hostnames is NOT performed here — a v2 hardening
// item is to add a custom dialer that re-validates the resolved IP on
// every request to defeat DNS rebinding. Today we accept that risk;
// tenants are vetted internal products.
//
// allowPrivate=true skips the IP-class check (for test environments
// that genuinely target private hosts).
func validateCallbackURL(raw string, allowPrivate bool) error {
	if len(raw) > maxCallbackURLLen {
		return fmt.Errorf("callback url too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse callback url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("callback url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("callback url missing host")
	}
	if u.User != nil {
		return fmt.Errorf("callback url must not contain userinfo")
	}
	if !allowPrivate {
		host := u.Hostname()
		// Strip trailing dot so "127.0.0.1." doesn't escape ParseIP.
		host = strings.TrimSuffix(host, ".")
		if ip := net.ParseIP(host); ip != nil {
			if isNonRoutable(ip) {
				return fmt.Errorf("callback URL resolves to a non-routable address")
			}
		} else if looksNumeric(host) {
			// Numeric-but-not-canonical-IP forms (e.g. "0x7f000001",
			// "2130706433", "127.1") parse as IPs in some resolvers /
			// kernels but not net.ParseIP. Refuse them outright — a
			// callback URL legitimately needing one of those forms is
			// not a real production target. Catches the classic SSRF
			// bypass via alternate IPv4 encodings.
			return fmt.Errorf("callback URL host appears numeric but is not a canonical IP; reject")
		}
	}
	return nil
}

// looksNumeric returns true when the host has the surface form of a
// numeric IP encoding (decimal/octal/hex/dotted variants) that some
// resolvers accept but net.ParseIP rejects. Catches "0x7f000001",
// "2130706433", "127.1", "0177.0.0.1", etc.
func looksNumeric(host string) bool {
	if host == "" {
		return false
	}
	// "0x..." hex form
	if strings.HasPrefix(strings.ToLower(host), "0x") {
		return true
	}
	// All chars are digits or dots → numeric encoding the resolver may
	// reinterpret. Real hostnames contain at least one letter.
	for _, r := range host {
		if (r >= '0' && r <= '9') || r == '.' {
			continue
		}
		return false
	}
	return true
}

// isNonRoutable returns true for IPs that must not be reachable from a
// webhook delivery: loopback, RFC1918 (IsPrivate), link-local, multicast,
// unspecified (0.0.0.0). Catches both v4 and v6.
func isNonRoutable(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

type DeliveryPayload struct {
	OrderID    string `json:"order_id"`
	PaymentRef string `json:"payment_ref"`
	Status     string `json:"status"`
	PaidAmount int64  `json:"paid_amount"`
	PaidAt     string `json:"paid_at"`
}

// CallbackTarget identifies where + how to deliver a webhook for a
// specific payment. Resolved per-row from the tenant that owns the
// payment: target.URL = tenant.callback_url, target.Secret =
// tenant.callback_secret.
type CallbackTarget struct {
	URL    string
	Secret string
}

type CallbackClient struct {
	httpClient   *http.Client
	allowPrivate bool
}

// noRedirects refuses to follow redirects on webhook deliveries.
// Webhooks are POST-with-side-effects; following a redirect from an
// attacker-controlled tenant URL to an internal endpoint is a classic
// SSRF bypass (the initial validateCallbackURL guard passes, then 302
// to 169.254.169.254/...). The receiver must accept on the original
// host or fail. ErrUseLastResponse returns the response without
// following — the 3xx then trips the non-2xx branch below.
func noRedirects(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func NewCallbackClient(timeout time.Duration) *CallbackClient {
	return &CallbackClient{
		httpClient: &http.Client{Timeout: timeout, CheckRedirect: noRedirects},
	}
}

// NewCallbackClientWithOpts is the production constructor: pass
// allowPrivate=true only in test environments.
func NewCallbackClientWithOpts(timeout time.Duration, allowPrivate bool) *CallbackClient {
	return &CallbackClient{
		httpClient:   &http.Client{Timeout: timeout, CheckRedirect: noRedirects},
		allowPrivate: allowPrivate,
	}
}

// Send POSTs the payload to target.URL HMAC-SHA256-signed with
// target.Secret. Empty target.URL is treated as a no-op success — a
// tenant that doesn't configure a callback URL is read-only by design
// (e.g. test/sandbox tenant).
//
// An empty target.Secret with a non-empty URL is a hard error: signing
// with an empty key would emit forgeable signatures. Operators configure
// the secret alongside the URL via the admin UI; the bootstrap path
// (default-tenant migration) also pulls both together from legacy config.
func (c *CallbackClient) Send(ctx context.Context, target CallbackTarget, payload DeliveryPayload) error {
	if target.URL == "" {
		return nil
	}
	if err := validateCallbackURL(target.URL, c.allowPrivate); err != nil {
		return fmt.Errorf("invalid callback target: %w", err)
	}
	if target.Secret == "" {
		return fmt.Errorf("callback target has URL but no signing secret")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(target.Secret))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", signature)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send callback: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("callback target returned status %d", resp.StatusCode)
	}
	return nil
}

// uuidFromCompact restores a 32-hex-char compact UUID to the standard
// 8-4-4-4-12 format. If the input is already formatted or has an unexpected
// length it is returned unchanged.
func uuidFromCompact(s string) string {
	if len(s) != 32 {
		return s
	}
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
