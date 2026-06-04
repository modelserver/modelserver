package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// TestCheckModelAllowed_DenylistChecksFirst verifies that when both the
// member denylist AND the api_key allowlist would reject a model, the
// error message names the denylist (the harder, project-wide constraint).
func TestCheckModelAllowed_DenylistChecksFirst(t *testing.T) {
	ctx := context.WithValue(context.Background(),
		ctxUserDeniedModels, []string{"claude-opus-4-8"})

	apiKey := &types.APIKey{AllowedModels: []string{"gpt-5"}}

	var (
		gotStatus int
		gotMsg    string
	)
	writeErr := func(_ http.ResponseWriter, status int, msg string) {
		gotStatus = status
		gotMsg = msg
	}

	ok := (&Handler{}).checkModelAllowed(nil, ctx, apiKey, "claude-opus-4-8", writeErr)
	if ok {
		t.Fatalf("expected check to fail")
	}
	if gotStatus != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", gotStatus)
	}
	if gotMsg != "model denied for this member by project policy" {
		t.Fatalf("msg = %q; want denylist message (proves denylist checked first)", gotMsg)
	}
}

func TestCheckModelAllowed_AllowlistOnly(t *testing.T) {
	ctx := context.Background()
	apiKey := &types.APIKey{AllowedModels: []string{"gpt-5"}}

	var gotMsg string
	writeErr := func(_ http.ResponseWriter, _ int, msg string) { gotMsg = msg }

	ok := (&Handler{}).checkModelAllowed(nil, ctx, apiKey, "claude-opus-4-8", writeErr)
	if ok {
		t.Fatalf("expected check to fail")
	}
	if gotMsg != "model not allowed for this API key" {
		t.Fatalf("msg = %q; want allowlist message", gotMsg)
	}
}

func TestCheckModelAllowed_BothPass(t *testing.T) {
	ctx := context.WithValue(context.Background(),
		ctxUserDeniedModels, []string{"claude-opus-4-8"})
	apiKey := &types.APIKey{AllowedModels: []string{"gpt-5"}}

	var called bool
	writeErr := func(_ http.ResponseWriter, _ int, _ string) { called = true }

	ok := (&Handler{}).checkModelAllowed(nil, ctx, apiKey, "gpt-5", writeErr)
	if !ok {
		t.Fatalf("expected check to pass")
	}
	if called {
		t.Fatalf("writeErr should not be called on pass")
	}
}

func TestCheckModelAllowed_EmptyDenylistEmptyAllowlist(t *testing.T) {
	ctx := context.Background()
	apiKey := &types.APIKey{}

	var called bool
	writeErr := func(_ http.ResponseWriter, _ int, _ string) { called = true }

	ok := (&Handler{}).checkModelAllowed(nil, ctx, apiKey, "anything", writeErr)
	if !ok {
		t.Fatalf("expected check to pass on empty config")
	}
	if called {
		t.Fatalf("writeErr should not be called on pass")
	}
}

func TestCheckModelAllowed_DenylistOnly(t *testing.T) {
	// Denylist set, no allowlist. Model in denylist → reject.
	ctx := context.WithValue(context.Background(),
		ctxUserDeniedModels, []string{"a", "b"})
	apiKey := &types.APIKey{}

	var gotMsg string
	writeErr := func(_ http.ResponseWriter, _ int, msg string) { gotMsg = msg }

	ok := (&Handler{}).checkModelAllowed(nil, ctx, apiKey, "a", writeErr)
	if ok {
		t.Fatalf("expected denylist hit to fail check")
	}
	if gotMsg != "model denied for this member by project policy" {
		t.Fatalf("msg = %q", gotMsg)
	}
}
