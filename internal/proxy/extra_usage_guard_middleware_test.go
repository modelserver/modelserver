package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/types"
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

func TestExtraUsageGuard_CountTokens_BypassesGuard(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	// cfg disabled would normally reject any request carrying intent — but
	// count_tokens must bypass the guard before that check runs.
	mw := ExtraUsageGuardMiddleware(dummyCfg(false), nil, nil)
	h := mw(inner)

	r := httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
	ctx := context.WithValue(r.Context(), ctxExtraUsageIntent, ExtraUsageIntent{Reason: "client_restriction"})
	r = r.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if !called {
		t.Fatalf("count_tokens must bypass guard even with intent set")
	}
	if rr.Header().Get("X-Extra-Usage-Required") != "" {
		t.Errorf("count_tokens must not receive extra-usage headers, got %q", rr.Header().Get("X-Extra-Usage-Required"))
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

// fakeExtraUsageStore satisfies the extraUsageStore interface for tests.
type fakeExtraUsageStore struct {
	settings *types.ExtraUsageSettings
	spent    int64
}

func (f *fakeExtraUsageStore) GetExtraUsageSettings(_ string) (*types.ExtraUsageSettings, error) {
	return f.settings, nil
}

func (f *fakeExtraUsageStore) GetMonthlyExtraSpendFen(_ string) (int64, error) {
	return f.spent, nil
}

// runGuardWithIntent runs the middleware with a rate_limited intent and
// returns (recorder, &innerCalled). Inner sets the flag; tests assert on
// the flag for "allowed" cases (the inner handler never writes a status,
// so rr.Code stays at the httptest default either way — the flag is the
// reliable signal).
func runGuardWithIntent(t *testing.T, cfg config.ExtraUsageConfig, st extraUsageStore, proj *types.Project) (*httptest.ResponseRecorder, *bool) {
	t.Helper()
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := ExtraUsageGuardMiddleware(cfg, st, slog.Default())(inner)

	r := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := context.WithValue(r.Context(), ctxExtraUsageIntent, ExtraUsageIntent{Reason: "rate_limited"})
	// ctxProject is the unexported key in auth_middleware.go; accessible
	// because the test lives in package proxy.
	ctx = context.WithValue(ctx, ctxProject, proj)
	r = r.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr, &called
}

func TestExtraUsageGuard_Bypass_EnabledFalse_BalanceZero_Allowed(t *testing.T) {
	st := &fakeExtraUsageStore{
		settings: &types.ExtraUsageSettings{
			ProjectID:          "p1",
			Enabled:            false,
			BalanceFen:         0,
			BypassBalanceCheck: true,
		},
	}
	rr, called := runGuardWithIntent(t, dummyCfg(true), st, &types.Project{ID: "p1"})
	if !*called {
		t.Errorf("inner handler not called; body=%q", rr.Body.String())
	}
	if rr.Header().Get("X-Extra-Usage-Required") != "" {
		t.Errorf("allowed path must not attach X-Extra-Usage-Required header")
	}
}

func TestExtraUsageGuard_Bypass_MonthlyLimitExceeded_Rejected(t *testing.T) {
	st := &fakeExtraUsageStore{
		settings: &types.ExtraUsageSettings{
			ProjectID:          "p1",
			Enabled:            true,
			BalanceFen:         100000,
			MonthlyLimitFen:    30000,
			BypassBalanceCheck: true,
		},
		spent: 30000,
	}
	rr, called := runGuardWithIntent(t, dummyCfg(true), st, &types.Project{ID: "p1"})
	if *called {
		t.Error("inner must not be called when monthly limit is exceeded")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr.Code)
	}
}

func TestExtraUsageGuard_NoBypass_BalanceZero_Rejected(t *testing.T) {
	st := &fakeExtraUsageStore{
		settings: &types.ExtraUsageSettings{
			ProjectID:  "p1",
			Enabled:    true,
			BalanceFen: 0,
		},
	}
	rr, called := runGuardWithIntent(t, dummyCfg(true), st, &types.Project{ID: "p1"})
	if *called {
		t.Error("inner must not be called with zero balance and no bypass")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr.Code)
	}
}
