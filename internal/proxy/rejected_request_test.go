package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestRequestKindFromRequest_AllRoutes(t *testing.T) {
	cases := []struct {
		method, path string
		want         string
	}{
		// Proxy POST surface mounted in router.go.
		{"POST", "/v1/messages", types.KindAnthropicMessages},
		{"POST", "/v1/messages/count_tokens", types.KindAnthropicCountTokens},
		{"POST", "/v1/responses", types.KindOpenAIResponses},
		{"POST", "/v1/responses/compact", types.KindOpenAIResponsesCompact},
		{"POST", "/v1/chat/completions", types.KindOpenAIChatCompletions},
		{"POST", "/v1/images/generations", types.KindOpenAIImagesGenerations},
		{"POST", "/v1/images/edits", types.KindOpenAIImagesEdits},
		{"POST", "/v1beta/models/gemini-2.5-flash:generateContent", types.KindGoogleGenerateContent},
		{"POST", "/v1beta/models/gemini-2.5-flash:streamGenerateContent", types.KindGoogleGenerateContent},
		{"POST", "/v1beta/models/foo:streamRawPredict", types.KindGoogleGenerateContent},

		// Non-proxy surface — should not be classified.
		{"GET", "/v1/messages", ""},
		{"GET", "/v1/models", ""},
		{"GET", "/v1/usage", ""},
		{"POST", "/admin/upstreams", ""},
		{"POST", "/healthz", ""},
		{"POST", "/v1beta/other/path", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := requestKindFromRequest(r); got != c.want {
			t.Errorf("%s %s → %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

func TestPeekStreaming(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		want                     bool
	}{
		// Path-based streaming (Gemini native).
		{"gemini stream", "POST", "/v1beta/models/gemini-2.5-flash:streamGenerateContent", "", true},
		{"gemini stream raw predict", "POST", "/v1beta/models/foo:streamRawPredict", "", true},
		{"gemini unary", "POST", "/v1beta/models/gemini-2.5-flash:generateContent", "", false},

		// Body-based streaming (Anthropic / OpenAI).
		{"anthropic stream true", "POST", "/v1/messages", `{"model":"x","stream":true}`, true},
		{"anthropic stream false", "POST", "/v1/messages", `{"model":"x","stream":false}`, false},
		{"anthropic stream absent", "POST", "/v1/messages", `{"model":"x"}`, false},
		{"openai stream true", "POST", "/v1/chat/completions", `{"stream":true}`, true},
		{"openai responses stream true", "POST", "/v1/responses", `{"stream":true}`, true},

		// Non-streaming surfaces — always false.
		{"count_tokens with stream true ignored", "POST", "/v1/messages/count_tokens", `{"stream":true}`, false},
		{"images generations", "POST", "/v1/images/generations", `{"stream":true}`, false},
		{"images edits", "POST", "/v1/images/edits", `{"stream":true}`, false},

		// Malformed inputs.
		{"malformed body", "POST", "/v1/messages", `{not json`, false},
		{"empty body", "POST", "/v1/messages", "", false},
		{"unknown path", "POST", "/v1/whatever", `{"stream":true}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r *http.Request
			if c.body == "" {
				r = httptest.NewRequest(c.method, c.path, nil)
			} else {
				r = httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			}
			got := peekStreaming(r)
			if got != c.want {
				t.Errorf("peekStreaming(%s %s, body=%q)=%v, want %v", c.method, c.path, c.body, got, c.want)
			}
			// Body must remain readable downstream.
			if c.body != "" && r.Body != nil {
				rest, _ := io.ReadAll(r.Body)
				if string(rest) != c.body {
					t.Errorf("body not restored: got %q, want %q", string(rest), c.body)
				}
			}
		})
	}
}

func TestBuildRejectedRequestRow_FullContext(t *testing.T) {
	body := `{"model":"claude-opus-4-7","stream":true}`
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	r.Header.Set("User-Agent", "foo/1.0")
	r.RemoteAddr = "10.0.0.5:54321"
	ctx := context.WithValue(r.Context(), ctxProject, &types.Project{ID: "p1"})
	ctx = context.WithValue(ctx, ctxAPIKey, &types.APIKey{ID: "k1", CreatedBy: "u1"})
	ctx = context.WithValue(ctx, ctxModel, &types.Model{Name: "claude-opus-4-7", Publisher: types.PublisherAnthropic})
	ctx = context.WithValue(ctx, ctxOAuthGrantID, "grant-xyz")
	ctx = context.WithValue(ctx, ctxTraceID, "trace-xyz")
	r = r.WithContext(ctx)

	got := buildRejectedRequestRow(r, types.RequestStatusRateLimited, "denied", "client_restriction")
	if got == nil {
		t.Fatalf("got nil, want populated request row")
	}
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
	if got.OAuthGrantID != "grant-xyz" {
		t.Errorf("OAuthGrantID=%q, want grant-xyz", got.OAuthGrantID)
	}
	if got.Model != "claude-opus-4-7" {
		t.Errorf("Model=%q, want claude-opus-4-7", got.Model)
	}
	if got.Provider != "" {
		t.Errorf("Provider=%q, want empty (rejection happens before upstream selection)", got.Provider)
	}
	if got.RequestKind != types.KindAnthropicMessages {
		t.Errorf("RequestKind=%q, want %q", got.RequestKind, types.KindAnthropicMessages)
	}
	if !got.Streaming {
		t.Errorf("Streaming=false, want true")
	}
	if got.Status != types.RequestStatusRateLimited {
		t.Errorf("Status=%q, want %q", got.Status, types.RequestStatusRateLimited)
	}
	if got.ErrorMessage != "denied" {
		t.Errorf("ErrorMessage=%q, want denied", got.ErrorMessage)
	}
	if got.ExtraUsageReason != "client_restriction" {
		t.Errorf("ExtraUsageReason=%q, want client_restriction", got.ExtraUsageReason)
	}
	if got.ClientIP != "10.0.0.5:54321" {
		t.Errorf("ClientIP=%q, want 10.0.0.5:54321", got.ClientIP)
	}
	if got.Metadata["user_agent"] != "foo/1.0" {
		t.Errorf("metadata[user_agent]=%q, want foo/1.0", got.Metadata["user_agent"])
	}
}

func TestBuildRejectedRequestRow_MissingProjectOrAPIKey(t *testing.T) {
	mk := func(seed func(context.Context) context.Context) *http.Request {
		r := httptest.NewRequest("POST", "/v1/messages", nil)
		if seed != nil {
			r = r.WithContext(seed(r.Context()))
		}
		return r
	}
	cases := []struct {
		name string
		seed func(context.Context) context.Context
	}{
		{"no project, no apikey", nil},
		{"project only", func(ctx context.Context) context.Context {
			return context.WithValue(ctx, ctxProject, &types.Project{ID: "p1"})
		}},
		{"apikey only", func(ctx context.Context) context.Context {
			return context.WithValue(ctx, ctxAPIKey, &types.APIKey{ID: "k1"})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildRejectedRequestRow(mk(c.seed), types.RequestStatusRateLimited, "msg", "")
			if got != nil {
				t.Errorf("want nil, got %+v", got)
			}
		})
	}
}

func TestBuildRejectedRequestRow_NoUserAgent(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	ctx := context.WithValue(r.Context(), ctxProject, &types.Project{ID: "p1"})
	ctx = context.WithValue(ctx, ctxAPIKey, &types.APIKey{ID: "k1"})
	r = r.WithContext(ctx)

	got := buildRejectedRequestRow(r, types.RequestStatusRateLimited, "msg", "")
	if got == nil {
		t.Fatalf("got nil")
	}
	if _, ok := got.Metadata["user_agent"]; ok {
		t.Errorf("metadata.user_agent must be absent when no UA header, got metadata=%v", got.Metadata)
	}
}

func TestBuildRejectedRequestRow_ModelFallsBackToBodyPeek(t *testing.T) {
	// No ModelRef in context — must fall back to peekModel reading the body.
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"x"}`))
	ctx := context.WithValue(r.Context(), ctxProject, &types.Project{ID: "p1"})
	ctx = context.WithValue(ctx, ctxAPIKey, &types.APIKey{ID: "k1"})
	r = r.WithContext(ctx)

	got := buildRejectedRequestRow(r, types.RequestStatusRateLimited, "msg", "")
	if got == nil {
		t.Fatalf("got nil")
	}
	if got.Model != "x" {
		t.Errorf("Model=%q, want x", got.Model)
	}
	if got.Provider != "" {
		t.Errorf("Provider=%q, want empty (no ModelRef in ctx)", got.Provider)
	}
}

func TestBuildRejectedRequestRow_AnthropicHeaders_OnMessages(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")
	r.Header.Set("Anthropic-Version", "2023-06-01")
	ctx := context.WithValue(r.Context(), ctxProject, &types.Project{ID: "p1"})
	ctx = context.WithValue(ctx, ctxAPIKey, &types.APIKey{ID: "k1"})
	r = r.WithContext(ctx)

	got := buildRejectedRequestRow(r, types.RequestStatusRateLimited, "msg", "")
	if got == nil {
		t.Fatalf("got nil")
	}
	if got.Metadata["anthropic_beta"] != "interleaved-thinking-2025-05-14" {
		t.Errorf("anthropic_beta=%q, want interleaved-thinking-2025-05-14", got.Metadata["anthropic_beta"])
	}
	if got.Metadata["anthropic_version"] != "2023-06-01" {
		t.Errorf("anthropic_version=%q, want 2023-06-01", got.Metadata["anthropic_version"])
	}
}

func TestBuildRejectedRequestRow_AnthropicHeaders_OnCountTokens(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/messages/count_tokens", nil)
	r.Header.Set("Anthropic-Beta", "beta-x")
	r.Header.Set("Anthropic-Version", "2023-06-01")
	ctx := context.WithValue(r.Context(), ctxProject, &types.Project{ID: "p1"})
	ctx = context.WithValue(ctx, ctxAPIKey, &types.APIKey{ID: "k1"})
	r = r.WithContext(ctx)

	got := buildRejectedRequestRow(r, types.RequestStatusRateLimited, "msg", "")
	if got == nil {
		t.Fatalf("got nil")
	}
	if got.Metadata["anthropic_beta"] != "beta-x" {
		t.Errorf("anthropic_beta=%q, want beta-x", got.Metadata["anthropic_beta"])
	}
	if got.Metadata["anthropic_version"] != "2023-06-01" {
		t.Errorf("anthropic_version=%q, want 2023-06-01", got.Metadata["anthropic_version"])
	}
}

func TestBuildRejectedRequestRow_AnthropicHeaders_NotCapturedOnNonAnthropicPaths(t *testing.T) {
	// Even if a client wrongly sends Anthropic-Beta to a non-anthropic
	// path, mirror the success-path scoping and do not capture it. This
	// keeps the metadata column shape consistent across kinds.
	cases := []struct{ method, path string }{
		{"POST", "/v1/chat/completions"},
		{"POST", "/v1/responses"},
		{"POST", "/v1/images/generations"},
		{"POST", "/v1beta/models/gemini-2.5-flash:generateContent"},
		{"POST", "/unknown/route"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		r.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")
		r.Header.Set("Anthropic-Version", "2023-06-01")
		ctx := context.WithValue(r.Context(), ctxProject, &types.Project{ID: "p1"})
		ctx = context.WithValue(ctx, ctxAPIKey, &types.APIKey{ID: "k1"})
		r = r.WithContext(ctx)

		got := buildRejectedRequestRow(r, types.RequestStatusRateLimited, "msg", "")
		if got == nil {
			t.Fatalf("%s %s: got nil", c.method, c.path)
		}
		if _, ok := got.Metadata["anthropic_beta"]; ok {
			t.Errorf("%s %s: anthropic_beta must NOT be captured on non-anthropic paths, got metadata=%v", c.method, c.path, got.Metadata)
		}
		if _, ok := got.Metadata["anthropic_version"]; ok {
			t.Errorf("%s %s: anthropic_version must NOT be captured on non-anthropic paths, got metadata=%v", c.method, c.path, got.Metadata)
		}
	}
}
