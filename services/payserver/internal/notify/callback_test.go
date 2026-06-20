package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCallback_Send_PerCallTargetSigning(t *testing.T) {
	secret := "test-webhook-secret"

	var receivedBody []byte
	var receivedSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedSig = r.Header.Get("X-Webhook-Signature")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCallbackClientWithOpts(5*time.Second, true)
	payload := DeliveryPayload{
		OrderID: "order-123", PaymentRef: "pay-456", Status: "paid",
		PaidAmount: 2000, PaidAt: "2026-03-11T12:00:00Z",
	}

	target := CallbackTarget{URL: srv.URL, Secret: secret}
	if err := client.Send(t.Context(), target, payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var got DeliveryPayload
	if err := json.Unmarshal(receivedBody, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OrderID != "order-123" {
		t.Errorf("OrderID = %q", got.OrderID)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(receivedBody)
	expected := hex.EncodeToString(mac.Sum(nil))
	if receivedSig != expected {
		t.Errorf("signature = %q, want %q", receivedSig, expected)
	}
}

func TestCallback_Send_EmptyURLIsNoop(t *testing.T) {
	client := NewCallbackClientWithOpts(5*time.Second, true)
	target := CallbackTarget{URL: "", Secret: "anything"}
	err := client.Send(t.Context(), target, DeliveryPayload{OrderID: "x"})
	if err != nil {
		t.Errorf("empty URL should be no-op success, got: %v", err)
	}
}

func TestCallback_Send_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewCallbackClientWithOpts(5*time.Second, true)
	target := CallbackTarget{URL: srv.URL, Secret: "s"}
	err := client.Send(t.Context(), target, DeliveryPayload{OrderID: "x"})
	if err == nil {
		t.Error("expected error on 500 response")
	}
}

func TestCallback_Send_PerCallDifferentSecrets(t *testing.T) {
	var sig1, sig2 string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sig1 == "" {
			sig1 = r.Header.Get("X-Webhook-Signature")
		} else {
			sig2 = r.Header.Get("X-Webhook-Signature")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewCallbackClientWithOpts(5*time.Second, true)
	pl := DeliveryPayload{OrderID: "x"}
	_ = client.Send(t.Context(), CallbackTarget{URL: srv.URL, Secret: "secret-a"}, pl)
	_ = client.Send(t.Context(), CallbackTarget{URL: srv.URL, Secret: "secret-b"}, pl)

	if sig1 == sig2 {
		t.Error("different secrets produced same signature — secret not used per-call")
	}
}

// TestCallback_Send_EmptySecretIsError covers the defense-in-depth check
// added in response to the auto-review: an empty signing secret combined
// with a non-empty URL would silently emit a HMAC over an empty key,
// which is trivially forgeable. Loud failure forces operator notice.
func TestCallback_Send_EmptySecretIsError(t *testing.T) {
	client := NewCallbackClientWithOpts(5*time.Second, true)
	err := client.Send(t.Context(),
		CallbackTarget{URL: "https://x.example/cb", Secret: ""},
		DeliveryPayload{OrderID: "x"})
	if err == nil {
		t.Error("expected error when secret empty + URL non-empty")
	}
}

// TestCallback_Send_InvalidURLSchemeRejected catches non-http(s) schemes
// before the request is built — second wall behind the admin write-path
// validation. file:// would otherwise let an attacker exfiltrate request
// bytes (no scheme check means net/http will try and fail in undefined
// ways).
func TestCallback_Send_InvalidURLSchemeRejected(t *testing.T) {
	client := NewCallbackClientWithOpts(5*time.Second, true)
	for _, raw := range []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ftp://x.example/cb",
		"://no-scheme/cb",
	} {
		err := client.Send(t.Context(),
			CallbackTarget{URL: raw, Secret: "s"},
			DeliveryPayload{OrderID: "x"})
		if err == nil {
			t.Errorf("scheme %q should have been rejected", raw)
		}
	}
}

// TestCallback_Send_UserinfoInURLRejected ensures embedded credentials
// (https://attacker@victim.example) cannot be used to confuse upstream
// auth or to leak attacker-supplied auth tokens to victim logs.
func TestCallback_Send_UserinfoInURLRejected(t *testing.T) {
	client := NewCallbackClientWithOpts(5*time.Second, true)
	err := client.Send(t.Context(),
		CallbackTarget{URL: "https://attacker@victim.example/cb", Secret: "s"},
		DeliveryPayload{OrderID: "x"})
	if err == nil {
		t.Error("userinfo in URL should be rejected")
	}
}

// TestValidateCallbackURL_RejectsNonRoutableLiteralIPs covers the
// SSRF guard added in Fix 7. Literal private/loopback/link-local IPs
// must be rejected unless allowPrivate=true is set.
func TestValidateCallbackURL_RejectsNonRoutableLiteralIPs(t *testing.T) {
	cases := []struct {
		raw    string
		reject bool // expected when allowPrivate=false
	}{
		{"http://127.0.0.1/cb", true},
		{"http://10.0.0.5/cb", true},
		{"http://192.168.1.1/cb", true},
		{"http://172.16.0.1/cb", true},
		{"http://169.254.169.254/latest/meta-data/", true}, // AWS metadata
		{"http://[::1]/cb", true},                          // IPv6 loopback
		{"http://0.0.0.0/cb", true},                        // unspecified
		{"http://8.8.8.8/cb", false},                       // public — accept
	}
	for _, c := range cases {
		err := validateCallbackURL(c.raw, false)
		if c.reject && err == nil {
			t.Errorf("validateCallbackURL(%q, allowPrivate=false) = nil, want error", c.raw)
		}
		if !c.reject && err != nil {
			t.Errorf("validateCallbackURL(%q, allowPrivate=false) = %v, want nil", c.raw, err)
		}
		// allowPrivate=true: all must pass (sanity).
		if err := validateCallbackURL(c.raw, true); err != nil {
			t.Errorf("validateCallbackURL(%q, allowPrivate=true) = %v, want nil", c.raw, err)
		}
	}
}

// TestCallback_Send_RejectsLoopbackByDefault end-to-end: the default
// NewCallbackClient (allowPrivate=false) rejects a literal-IP loopback
// URL before any HTTP attempt.
func TestCallback_Send_RejectsLoopbackByDefault(t *testing.T) {
	client := NewCallbackClient(5 * time.Second)
	err := client.Send(t.Context(),
		CallbackTarget{URL: "http://127.0.0.1:9999/cb", Secret: "s"},
		DeliveryPayload{OrderID: "x"})
	if err == nil {
		t.Fatal("expected SSRF rejection on loopback IP")
	}
}

// TestValidateCallbackURL_RejectsAlternateIPv4Encodings closes the SSRF
// bypass via numeric host forms that net.ParseIP refuses but
// some resolvers / OS stacks happily reinterpret as private IPs
// (e.g. 0x7f000001 → 127.0.0.1; 2130706433 → 127.0.0.1; 127.1 → 127.0.0.1
// in BSD sockets; "127.0.0.1." with trailing dot escapes ParseIP).
func TestValidateCallbackURL_RejectsAlternateIPv4Encodings(t *testing.T) {
	cases := []string{
		"http://0x7f000001/cb", // hex
		"http://2130706433/cb", // dword decimal
		"http://127.1/cb",      // short form
		"http://0177.0.0.1/cb", // octal-looking
		"http://127.0.0.1./cb", // trailing dot
	}
	for _, raw := range cases {
		if err := validateCallbackURL(raw, false); err == nil {
			t.Errorf("validateCallbackURL(%q, false) = nil, want SSRF rejection", raw)
		}
	}
}

// TestCallback_Send_DoesNotFollowRedirects ensures a tenant URL that
// validates clean but 302s into a private/loopback address does NOT
// chase the redirect — webhooks must land on the registered host or
// fail. ErrUseLastResponse trips the non-2xx branch.
func TestCallback_Send_DoesNotFollowRedirects(t *testing.T) {
	// Use a /target that records whether it was hit. If the client
	// followed the redirect, the counter increments; if it correctly
	// refused (ErrUseLastResponse), the counter stays at zero and Send
	// reports the 302 as a non-2xx error.
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/cb", func(w http.ResponseWriter, r *http.Request) {
		// Redirect within the same test server so both URLs are
		// reachable under allowPrivate=true.
		http.Redirect(w, r, "/target", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// allowPrivate=true so the httptest server (binds to 127.0.0.1)
	// is reachable; the assertion is about redirect behavior, not the
	// SSRF guard (which is covered elsewhere).
	client := NewCallbackClientWithOpts(5*time.Second, true)
	err := client.Send(t.Context(),
		CallbackTarget{URL: srv.URL + "/cb", Secret: "s"},
		DeliveryPayload{OrderID: "x"})
	if err == nil {
		t.Fatal("expected non-2xx error after refusing to follow redirect")
	}
	if hits != 0 {
		t.Errorf("redirect target was hit %d times; client must not follow redirects", hits)
	}
}
