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

// Claude Desktop is Anthropic's first-party Electron app; like Claude Code it
// is allowed to consume the project's subscription against anthropic-publisher
// models. Anything else (third-party tools, unknown clients) must still hit
// the client_restriction branch.
func TestSubscriptionEligibility_AnthropicClaudeDesktop_Eligible(t *testing.T) {
	m := &types.Model{Name: "claude-opus-4-7", Publisher: types.PublisherAnthropic}
	h, got := buildChain(withModelKind(m, types.ClientKindClaudeDesktop))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x", nil))
	if !got.Eligible || got.Reason != "" {
		t.Errorf("claude-desktop+anthropic → %+v, want eligible", *got)
	}
}

func TestSubscriptionEligibility_AnthropicOther_ClientRestriction(t *testing.T) {
	m := &types.Model{Name: "claude-opus-4-7", Publisher: types.PublisherAnthropic}
	// Note: ClientKindClaudeDesktop is intentionally absent — desktop is
	// covered by AnthropicClaudeDesktop_Eligible above.
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
		for _, kind := range []string{types.ClientKindClaudeCode, types.ClientKindClaudeDesktop, types.ClientKindOpenCode, types.ClientKindUnknown} {
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

// Models flagged with metadata.extra_usage_only are premium-tier: they
// must take the extra-usage path for EVERY client kind, including the
// first-party ones (Claude Code, Claude Desktop) that would otherwise
// pass the publisher gate. This is the whole point of the flag —
// subscribers can't be given silent access to a model priced above their
// plan bundle.
func TestSubscriptionEligibility_ExtraUsageOnlyModel_AllClientsIneligible(t *testing.T) {
	m := &types.Model{
		Name:      "claude-fable-5",
		Publisher: types.PublisherAnthropic,
		Metadata:  types.ModelMetadata{ExtraUsageOnly: true},
	}
	// Every ClientKind constant must be exercised — the flag's whole
	// contract is "premium regardless of kind", so a future refactor that
	// accidentally special-cases one must fail the test. If a new
	// ClientKind is added to types/extra_usage.go, add it here too.
	for _, kind := range []string{
		types.ClientKindClaudeCode,
		types.ClientKindClaudeDesktop,
		types.ClientKindOpenCode,
		types.ClientKindOpenClaw,
		types.ClientKindCodex,
		types.ClientKindUnknown,
	} {
		h, got := buildChain(withModelKind(m, kind))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x", nil))
		if got.Eligible || got.Reason != types.ExtraUsageReasonClientRestriction {
			t.Errorf("extra_usage_only model kind=%q → %+v, want ineligible+client_restriction",
				kind, *got)
		}
	}
}

// A premium model with an empty Publisher must still be ineligible — the
// premium check runs before the missing-publisher data-hole branch, so a
// mis-seeded catalog row can't accidentally let the model through as
// eligible via the metric-and-pass path.
func TestSubscriptionEligibility_ExtraUsageOnlyModel_MissingPublisherStillIneligible(t *testing.T) {
	m := &types.Model{
		Name:     "claude-fable-5", // Publisher == ""
		Metadata: types.ModelMetadata{ExtraUsageOnly: true},
	}
	h, got := buildChain(withModelKind(m, types.ClientKindClaudeCode))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x", nil))
	if got.Eligible || got.Reason != types.ExtraUsageReasonClientRestriction {
		t.Errorf("extra_usage_only model with empty publisher → %+v, want ineligible+client_restriction", *got)
	}
}

// The zero value (ExtraUsageOnly=false) must not affect existing behavior:
// non-flagged models keep going through the publisher/client-kind gate
// unchanged. Pairs with the AnthropicOther test above; this one asserts
// the eligible half of the gate for an unflagged model.
func TestSubscriptionEligibility_ExtraUsageOnly_ZeroValueIsNoop(t *testing.T) {
	m := &types.Model{
		Name:      "claude-opus-4-8",
		Publisher: types.PublisherAnthropic,
		// Metadata omitted → ExtraUsageOnly == false
	}
	h, got := buildChain(withModelKind(m, types.ClientKindClaudeCode))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/x", nil))
	if !got.Eligible || got.Reason != "" {
		t.Errorf("unflagged anthropic model on claude-code → %+v, want eligible", *got)
	}
}
