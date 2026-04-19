package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelserver/modelserver/internal/config"
)

// Guard needs a project in context and a store. For this pure MW unit test
// we use the no-intent path (which skips the store entirely) and the global
// disabled path (which also skips the store).

func TestExtraUsageGuard_NoIntent_PassThrough(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	mw := ExtraUsageGuardMiddleware(dummyCfg(true), nil, nil)
	h := mw(inner)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/x", nil))
	if !called {
		t.Fatalf("no-intent requests must pass through unchanged")
	}
	if rr.Header().Get("X-Extra-Usage-Required") != "" {
		t.Errorf("no-intent request must not receive X-Extra-Usage-Required, got %q", rr.Header().Get("X-Extra-Usage-Required"))
	}
}

func TestExtraUsageGuard_GlobalDisabled_Rejects(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	mw := ExtraUsageGuardMiddleware(dummyCfg(false), nil, nil)
	h := mw(inner)

	r := httptest.NewRequest("POST", "/x", nil)
	ctx := context.WithValue(r.Context(), ctxExtraUsageIntent, ExtraUsageIntent{Reason: "rate_limited"})
	r = r.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if called {
		t.Fatalf("global disabled must not call through")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("status=%d, want 429", rr.Code)
	}
	if rr.Header().Get("X-Extra-Usage-Required") != "true" {
		t.Errorf("missing X-Extra-Usage-Required header")
	}
	if rr.Header().Get("X-Extra-Usage-Reason") != "rate_limited" {
		t.Errorf("X-Extra-Usage-Reason=%q, want rate_limited", rr.Header().Get("X-Extra-Usage-Reason"))
	}
	// Confirm body is a JSON error envelope (kept compatible with existing
	// rate_limit_error parsing).
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["type"] != "error" {
		t.Errorf("body type=%v, want error", body["type"])
	}
}

func TestRejectedMessage_Mapping(t *testing.T) {
	cases := []struct {
		reason, sub, want string
	}{
		{"client_restriction", "not_enabled", "this client cannot use subscription for anthropic models; enable extra usage"},
		{"client_restriction", "balance_depleted", "extra usage balance depleted for this client restriction"},
		{"client_restriction", "monthly_limit", "extra usage monthly limit reached for this client restriction"},
		{"rate_limited", "not_enabled", "rate limit reached; enable extra usage to continue"},
		{"rate_limited", "balance_depleted", "rate limit reached; extra usage balance depleted"},
		{"rate_limited", "monthly_limit", "rate limit reached; extra usage monthly limit reached"},
	}
	for _, c := range cases {
		got := rejectedMessage(c.reason, c.sub)
		if got != c.want {
			t.Errorf("rejectedMessage(%q,%q)=%q, want %q", c.reason, c.sub, got, c.want)
		}
	}
}

func dummyCfg(enabled bool) config.ExtraUsageConfig {
	return config.ExtraUsageConfig{Enabled: enabled, CreditPriceFen: 5438}
}
