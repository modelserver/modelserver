package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// buildChain produces a handler with SubscriptionEligibilityMiddleware at its
// tip and a recorder as the inner handler that captures the decision.
func buildChain(seed func(context.Context) context.Context) (http.Handler, *SubscriptionEligibility) {
	got := &SubscriptionEligibility{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elig := SubscriptionEligibilityFromContext(r.Context())
		*got = elig
	})
	mw := SubscriptionEligibilityMiddleware()
	seedMW := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if seed != nil {
				ctx = seed(ctx)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	return seedMW(mw(inner)), got
}

func withModelKind(m *types.Model, kind string) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		if m != nil {
			ctx = context.WithValue(ctx, ctxModel, m)
		}
		ctx = context.WithValue(ctx, ctxClientKind, kind)
		return ctx
	}
}

func TestSubscriptionEligibility_AnthropicClaudeCode_Eligible(t *testing.T) {
	m := &types.Model{Name: "claude-opus-4-7", Publisher: types.PublisherAnthropic}
	h, got := buildChain(withModelKind(m, types.ClientKindClaudeCode))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x", nil))
	if !got.Eligible || got.Reason != "" {
		t.Errorf("claude-code+anthropic → %+v, want eligible", *got)
	}
}

func TestSubscriptionEligibility_AnthropicOther_ClientRestriction(t *testing.T) {
	m := &types.Model{Name: "claude-opus-4-7", Publisher: types.PublisherAnthropic}
	for _, kind := range []string{types.ClientKindOpenCode, types.ClientKindCodex, types.ClientKindOpenClaw, types.ClientKindUnknown} {
		h, got := buildChain(withModelKind(m, kind))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x", nil))
		if got.Eligible || got.Reason != types.ExtraUsageReasonClientRestriction {
			t.Errorf("kind=%q → %+v, want ineligible+client_restriction", kind, *got)
		}
	}
}

func TestSubscriptionEligibility_OpenAIAndGoogle_AnyClientEligible(t *testing.T) {
	for _, pub := range []string{types.PublisherOpenAI, types.PublisherGoogle} {
		m := &types.Model{Name: "x", Publisher: pub}
		for _, kind := range []string{types.ClientKindClaudeCode, types.ClientKindOpenCode, types.ClientKindUnknown} {
			h, got := buildChain(withModelKind(m, kind))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x", nil))
			if !got.Eligible {
				t.Errorf("pub=%q kind=%q → ineligible, want eligible", pub, kind)
			}
		}
	}
}

func TestSubscriptionEligibility_MissingPublisher_Eligible(t *testing.T) {
	m := &types.Model{Name: "mystery"} // Publisher == ""
	h, got := buildChain(withModelKind(m, types.ClientKindOpenCode))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x", nil))
	if !got.Eligible {
		t.Errorf("empty publisher must default to eligible, got %+v", *got)
	}
}

func TestSubscriptionEligibility_NoModelInContext_Eligible(t *testing.T) {
	h, got := buildChain(withModelKind(nil, types.ClientKindOpenCode))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if !got.Eligible {
		t.Errorf("no model in context → got %+v, want eligible", *got)
	}
}
