package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

	mu         sync.Mutex
	logged     []*types.Request
}

func (f *fakeExtraUsageStore) GetExtraUsageSettings(_ string) (*types.ExtraUsageSettings, error) {
	return f.settings, nil
}

func (f *fakeExtraUsageStore) GetMonthlyExtraSpendFen(_ string) (int64, error) {
	return f.spent, nil
}

func (f *fakeExtraUsageStore) CreateRequest(req *types.Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logged = append(f.logged, req)
	return nil
}

func (f *fakeExtraUsageStore) loggedRequests() []*types.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*types.Request, len(f.logged))
	copy(out, f.logged)
	return out
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

// runGuardWithModel mirrors runGuardWithIntent but also attaches a *types.Model
// to the context, simulating ResolveModelMiddleware's effect.
func runGuardWithModel(t *testing.T, cfg config.ExtraUsageConfig, st extraUsageStore, proj *types.Project, m *types.Model) (*httptest.ResponseRecorder, *bool) {
	t.Helper()
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := ExtraUsageGuardMiddleware(cfg, st, slog.Default())(inner)

	r := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := context.WithValue(r.Context(), ctxExtraUsageIntent, ExtraUsageIntent{Reason: "rate_limited"})
	ctx = context.WithValue(ctx, ctxProject, proj)
	if m != nil {
		ctx = context.WithValue(ctx, ctxModel, m)
	}
	r = r.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr, &called
}

func TestExtraUsageGuard_NoBypass_ModelMissingDefaultRate_Rejected(t *testing.T) {
	st := &fakeExtraUsageStore{
		settings: &types.ExtraUsageSettings{
			ProjectID:  "p1",
			Enabled:    true,
			BalanceFen: 100000,
		},
	}
	// Model has no DefaultCreditRate — settle would silently no-op without
	// the guard pre-check. With the pre-check, the request is rejected
	// before it reaches the upstream so no free ride happens.
	m := &types.Model{Name: "uncalibrated-model"}
	rr, called := runGuardWithModel(t, dummyCfg(true), st, &types.Project{ID: "p1"}, m)
	if *called {
		t.Error("inner must not be called for unpriced model")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr.Code)
	}
}

func TestExtraUsageGuard_NoBypass_CreditPriceUnset_Rejected(t *testing.T) {
	st := &fakeExtraUsageStore{
		settings: &types.ExtraUsageSettings{
			ProjectID:  "p1",
			Enabled:    true,
			BalanceFen: 100000,
		},
	}
	cfg := config.ExtraUsageConfig{Enabled: true, CreditPriceFen: 0}
	m := &types.Model{Name: "any", DefaultCreditRate: &types.CreditRate{InputRate: 1}}
	rr, called := runGuardWithModel(t, cfg, st, &types.Project{ID: "p1"}, m)
	if *called {
		t.Error("inner must not be called when credit price is unset")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr.Code)
	}
}

// TestExtraUsageGuard_ClientRestriction_NotEnabled_LogsRequest exercises the
// production scenario reported in ops: a non-Claude-Code client hits an
// anthropic-publisher model on a project that hasn't enabled extra usage. The
// guard returns 429, but historically the rejection bypassed the requests
// table entirely (RateLimitMiddleware took the classic-only happy path and
// the guard only bumped a Prometheus counter). This test pins the new
// behaviour: such rejections must produce a `rate_limited` row carrying the
// reason and the human-readable rejection message so operators can locate
// individual rejections in the requests table.
func TestExtraUsageGuard_ClientRestriction_NotEnabled_LogsRequest(t *testing.T) {
	st := &fakeExtraUsageStore{settings: nil} // extra-usage settings row absent
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
	h := ExtraUsageGuardMiddleware(dummyCfg(true), st, slog.Default())(inner)

	body := strings.NewReader(`{"model":"claude-haiku-4-5","stream":true}`)
	r := httptest.NewRequest("POST", "/v1/messages", body)
	r.Header.Set("User-Agent", "foo/1.0")
	ctx := context.WithValue(r.Context(), ctxExtraUsageIntent, ExtraUsageIntent{Reason: "client_restriction"})
	ctx = context.WithValue(ctx, ctxProject, &types.Project{ID: "p1"})
	ctx = context.WithValue(ctx, ctxAPIKey, &types.APIKey{ID: "k1", CreatedBy: "u1"})
	ctx = context.WithValue(ctx, ctxTraceID, "trace-xyz")
	ctx = context.WithValue(ctx, ctxModel, &types.Model{Name: "claude-haiku-4-5", Publisher: types.PublisherAnthropic})
	ctx = context.WithValue(ctx, ctxOAuthGrantID, "grant-xyz")
	r = r.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)

	if called {
		t.Fatalf("inner must not be called when guard rejects")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429", rr.Code)
	}

	// CreateRequest is fired in a goroutine on the production path; give it
	// a brief window to land before asserting.
	deadline := time.Now().Add(2 * time.Second)
	var logged []*types.Request
	for time.Now().Before(deadline) {
		logged = st.loggedRequests()
		if len(logged) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(logged) != 1 {
		t.Fatalf("expected exactly 1 logged request, got %d", len(logged))
	}
	got := logged[0]
	if got.ProjectID != "p1" {
		t.Errorf("ProjectID=%q, want p1", got.ProjectID)
	}
	if got.APIKeyID != "k1" {
		t.Errorf("APIKeyID=%q, want k1", got.APIKeyID)
	}
	if got.CreatedBy != "u1" {
		t.Errorf("CreatedBy=%q, want u1", got.CreatedBy)
	}
	if got.TraceID != "trace-xyz" {
		t.Errorf("TraceID=%q, want trace-xyz", got.TraceID)
	}
	if got.Model != "claude-haiku-4-5" {
		t.Errorf("Model=%q, want claude-haiku-4-5", got.Model)
	}
	if got.Status != types.RequestStatusRateLimited {
		t.Errorf("Status=%q, want %q", got.Status, types.RequestStatusRateLimited)
	}
	if got.ExtraUsageReason != "client_restriction" {
		t.Errorf("ExtraUsageReason=%q, want client_restriction", got.ExtraUsageReason)
	}
	wantMsg := "this client cannot use subscription for anthropic models; enable extra usage"
	if got.ErrorMessage != wantMsg {
		t.Errorf("ErrorMessage=%q, want %q", got.ErrorMessage, wantMsg)
	}
	if got.RequestKind != types.KindAnthropicMessages {
		t.Errorf("RequestKind=%q, want %q", got.RequestKind, types.KindAnthropicMessages)
	}
	if !got.Streaming {
		t.Errorf("Streaming=false, want true")
	}
	if got.OAuthGrantID != "grant-xyz" {
		t.Errorf("OAuthGrantID=%q, want grant-xyz", got.OAuthGrantID)
	}
	if got.Provider != "" {
		t.Errorf("Provider=%q, want empty (rejection happens before upstream selection)", got.Provider)
	}
	if got.Metadata["user_agent"] != "foo/1.0" {
		t.Errorf("metadata[user_agent]=%q, want foo/1.0", got.Metadata["user_agent"])
	}
}

// TestExtraUsageGuard_GlobalDisabled_LogsRequest pins that the global-disabled
// short-circuit at the top of the guard also produces a requests row. This
// path runs before the per-project settings lookup, so it's the only 4xx exit
// that does not see `settings`.
func TestExtraUsageGuard_GlobalDisabled_LogsRequest(t *testing.T) {
	st := &fakeExtraUsageStore{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := ExtraUsageGuardMiddleware(dummyCfg(false), st, slog.Default())(inner)

	body := strings.NewReader(`{"model":"claude-haiku-4-5"}`)
	r := httptest.NewRequest("POST", "/v1/messages", body)
	ctx := context.WithValue(r.Context(), ctxExtraUsageIntent, ExtraUsageIntent{Reason: "rate_limited"})
	ctx = context.WithValue(ctx, ctxProject, &types.Project{ID: "p1"})
	ctx = context.WithValue(ctx, ctxAPIKey, &types.APIKey{ID: "k1", CreatedBy: "u1"})
	r = r.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429", rr.Code)
	}

	deadline := time.Now().Add(2 * time.Second)
	var logged []*types.Request
	for time.Now().Before(deadline) {
		logged = st.loggedRequests()
		if len(logged) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(logged) != 1 {
		t.Fatalf("expected exactly 1 logged request, got %d", len(logged))
	}
	if logged[0].ExtraUsageReason != "rate_limited" {
		t.Errorf("ExtraUsageReason=%q, want rate_limited", logged[0].ExtraUsageReason)
	}
}

// TestExtraUsageGuard_ClientRestriction_LogsRequestDetails pins that when the
// guard rejects, a structured slog line is emitted carrying the inputs that
// determined the verdict (publisher, client_kind, user-agent, presence of the
// four client-kind detection signals, etc). Without this, operators can see
// "client_restriction" was the reason but not why the request wasn't
// recognised as the matching client.
func TestExtraUsageGuard_ClientRestriction_LogsRequestDetails(t *testing.T) {
	st := &fakeExtraUsageStore{settings: nil}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := ExtraUsageGuardMiddleware(dummyCfg(true), st, logger)(inner)

	body := strings.NewReader(`{"model":"claude-haiku-4-5","metadata":{"user_id":"{\"device_id\":\"abc\",\"account_uuid\":\"u-1\"}"}}`)
	r := httptest.NewRequest("POST", "/v1/messages", body)
	r.Header.Set("User-Agent", "curl/8.4.0")
	r.Header.Set("x-app", "my-tool")
	r.RemoteAddr = "10.0.0.5:54321"
	ctx := context.WithValue(r.Context(), ctxExtraUsageIntent, ExtraUsageIntent{Reason: "client_restriction"})
	ctx = context.WithValue(ctx, ctxProject, &types.Project{ID: "p1"})
	ctx = context.WithValue(ctx, ctxAPIKey, &types.APIKey{ID: "k1", CreatedBy: "u1"})
	ctx = context.WithValue(ctx, ctxTraceID, "trace-xyz")
	ctx = context.WithValue(ctx, ctxModel, &types.Model{Name: "claude-haiku-4-5", Publisher: types.PublisherAnthropic})
	ctx = context.WithValue(ctx, ctxClientKind, types.ClientKindUnknown)
	r = r.WithContext(ctx)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429", rr.Code)
	}

	// Find the guard-rejection log line.
	var found map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logBuf.Bytes()), []byte("\n")) {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if msg, _ := rec["msg"].(string); msg == "extra_usage_rejected" {
			found = rec
			break
		}
	}
	if found == nil {
		t.Fatalf("no extra_usage_rejected log line emitted; buf=%s", logBuf.String())
	}

	// Spot-check fields that ops needs to attribute and triage.
	wantStr := map[string]string{
		"reason":           "client_restriction",
		"sub_reason":       "not_enabled",
		"project_id":       "p1",
		"api_key_id":       "k1",
		"created_by":       "u1",
		"trace_id":         "trace-xyz",
		"model":            "claude-haiku-4-5",
		"publisher":        "anthropic",
		"client_kind":      "unknown",
		"user_agent":       "curl/8.4.0",
		"client_ip":        "10.0.0.5:54321",
		"path":             "/v1/messages",
		"user_id_shape":    "json_no_session",
		"opencode_header":  "false",
		"codex_session":    "false",
		"openclaw_match":   "false",
	}
	for k, want := range wantStr {
		got, _ := found[k].(string)
		if got != want {
			t.Errorf("log[%s]=%q, want %q", k, got, want)
		}
	}
}

func TestExtraUsageGuard_NoBypass_PriceableModel_Allowed(t *testing.T) {
	st := &fakeExtraUsageStore{
		settings: &types.ExtraUsageSettings{
			ProjectID:  "p1",
			Enabled:    true,
			BalanceFen: 100000,
		},
	}
	m := &types.Model{Name: "priced", DefaultCreditRate: &types.CreditRate{InputRate: 3, OutputRate: 15}}
	rr, called := runGuardWithModel(t, dummyCfg(true), st, &types.Project{ID: "p1"}, m)
	if !*called {
		t.Errorf("inner handler not called; body=%q", rr.Body.String())
	}
}
