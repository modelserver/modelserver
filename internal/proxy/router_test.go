package proxy

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/types"
)

// helper: a tiny catalog stub
func newTestCatalog() modelcatalog.Catalog {
	return modelcatalog.New([]types.Model{
		{
			Name:        "gpt-5",
			DisplayName: "GPT-5",
			Publisher:   "openai",
			CreatedAt:   time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		},
		{
			Name:        "claude-opus-4-7",
			DisplayName: "Claude Opus 4.7",
			Publisher:   "anthropic",
			CreatedAt:   time.Date(2026, 2, 20, 9, 30, 0, 0, time.UTC),
		},
	})
}

func TestWriteOpenAIModelsList(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	writeOpenAIModelsList(w, newTestCatalog(), []string{"gpt-5", "claude-opus-4-7"})

	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("object = %q, want list", resp.Object)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].ID != "gpt-5" || resp.Data[0].OwnedBy != "openai" {
		t.Errorf("entry 0 = %+v", resp.Data[0])
	}
	if resp.Data[0].Object != "model" {
		t.Errorf("entry 0 object = %q, want model", resp.Data[0].Object)
	}
	if resp.Data[1].OwnedBy != "anthropic" {
		t.Errorf("entry 1 owned_by = %q", resp.Data[1].OwnedBy)
	}
}

func TestWriteOpenAIModelsList_FallbackOwnedBy(t *testing.T) {
	// Model with no Publisher should fall back to "system".
	cat := modelcatalog.New([]types.Model{{Name: "x", CreatedAt: time.Now()}})
	w := httptest.NewRecorder()
	writeOpenAIModelsList(w, cat, []string{"x"})
	var resp struct {
		Data []struct {
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data[0].OwnedBy != "system" {
		t.Errorf("owned_by = %q, want system", resp.Data[0].OwnedBy)
	}
}

func TestWriteAnthropicModelsList(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	writeAnthropicModelsList(w, newTestCatalog(), []string{"gpt-5", "claude-opus-4-7"})

	var resp struct {
		Data []struct {
			Type        string `json:"type"`
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			CreatedAt   string `json:"created_at"`
		} `json:"data"`
		FirstID string `json:"first_id"`
		LastID  string `json:"last_id"`
		HasMore bool   `json:"has_more"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data len = %d", len(resp.Data))
	}
	if resp.Data[0].Type != "model" {
		t.Errorf("type = %q, want model", resp.Data[0].Type)
	}
	if resp.Data[0].DisplayName != "GPT-5" {
		t.Errorf("display_name = %q", resp.Data[0].DisplayName)
	}
	if resp.Data[0].CreatedAt != "2026-01-15T12:00:00Z" {
		t.Errorf("created_at = %q", resp.Data[0].CreatedAt)
	}
	if resp.FirstID != "gpt-5" || resp.LastID != "claude-opus-4-7" {
		t.Errorf("first/last = %q/%q", resp.FirstID, resp.LastID)
	}
	if resp.HasMore {
		t.Error("has_more should be false")
	}
}

func TestWriteAnthropicModelsList_Empty(t *testing.T) {
	// Empty list should give empty string IDs (not null).
	w := httptest.NewRecorder()
	writeAnthropicModelsList(w, newTestCatalog(), nil)
	body := w.Body.String()
	if !strings.Contains(body, `"first_id":""`) || !strings.Contains(body, `"last_id":""`) {
		t.Errorf("expected empty-string first_id/last_id, got body: %s", body)
	}
}

func TestWriteAnthropicModelsList_FallbackDisplayName(t *testing.T) {
	cat := modelcatalog.New([]types.Model{{Name: "raw-name", CreatedAt: time.Now()}})
	w := httptest.NewRecorder()
	writeAnthropicModelsList(w, cat, []string{"raw-name"})
	var resp struct {
		Data []struct {
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data[0].DisplayName != "raw-name" {
		t.Errorf("display_name fallback = %q, want raw-name", resp.Data[0].DisplayName)
	}
}

func TestMountRoutes_ImageEndpointsAreRegistered(t *testing.T) {
	r := chi.NewRouter()
	MountRoutes(
		r,
		nil,
		&Handler{},
		config.TraceConfig{},
		nil,
		nil,
		config.ExtraUsageConfig{},
		16<<20,
		200<<20,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
	)

	for _, path := range []string{"/v1/images/generations", "/v1/images/edits"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-image-2"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		r.ServeHTTP(w, req)

		if w.Code == http.StatusNotFound {
			t.Fatalf("%s was not registered; got %d body %q", path, w.Code, w.Body.String())
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want %d before auth", path, w.Code, http.StatusUnauthorized)
		}
	}
}

func TestMountRoutes_ResponsesCompactIsRegistered(t *testing.T) {
	r := chi.NewRouter()
	MountRoutes(
		r,
		nil,
		&Handler{},
		config.TraceConfig{},
		nil,
		nil,
		config.ExtraUsageConfig{},
		16<<20,
		200<<20,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5","input":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("/v1/responses/compact was not registered; got %d body %q", w.Code, w.Body.String())
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d before auth", w.Code, http.StatusUnauthorized)
	}
}

// TestHandleListModels_DenylistSubtract ensures the /v1/models output
// has the per-member denylist subtracted, regardless of whether the
// caller's api_key has an allowlist.
func TestHandleListModels_DenylistSubtract(t *testing.T) {
	cat := newTestCatalog()
	h := &Handler{catalog: cat}

	cases := []struct {
		name    string
		allowed []string
		denied  []string
		want    []string
	}{
		{
			name:    "allowlist intersected with denylist",
			allowed: []string{"gpt-5", "claude-opus-4-7"},
			denied:  []string{"claude-opus-4-7"},
			want:    []string{"gpt-5"},
		},
		{
			name:    "empty denylist = unchanged",
			allowed: []string{"gpt-5", "claude-opus-4-7"},
			denied:  nil,
			want:    []string{"gpt-5", "claude-opus-4-7"},
		},
		{
			name:    "denylist removes all entries from allowlist",
			allowed: []string{"gpt-5"},
			denied:  []string{"gpt-5"},
			want:    []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/models", nil)
			ctx := context.WithValue(req.Context(), ctxAPIKey, &types.APIKey{
				AllowedModels: tc.allowed,
			})
			if len(tc.denied) > 0 {
				ctx = context.WithValue(ctx, ctxUserDeniedModels, tc.denied)
			}
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			h.HandleListModels(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got := make([]string, 0, len(resp.Data))
			for _, d := range resp.Data {
				got = append(got, d.ID)
			}
			if !equalListModelsStringSet(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func equalListModelsStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
		if m[s] < 0 {
			return false
		}
	}
	return true
}

// TestSubtractStrings exercises the pure helper that HandleListModels
// applies to the source model list. The helper runs unconditionally on
// both branches of HandleListModels (allowlist-derived names AND
// router.ActiveModels()-derived names), so direct unit coverage here
// substitutes for setting up a full Router fixture in the table-driven
// HandleListModels test above.
func TestSubtractStrings(t *testing.T) {
	cases := []struct {
		name string
		a    []string
		b    []string
		want []string
	}{
		{"empty_b_returns_a", []string{"x", "y"}, nil, []string{"x", "y"}},
		{"empty_a_returns_empty", nil, []string{"x"}, []string{}},
		{"remove_one", []string{"a", "b", "c"}, []string{"b"}, []string{"a", "c"}},
		{"remove_all", []string{"a", "b"}, []string{"a", "b"}, []string{}},
		{"preserves_order", []string{"z", "a", "m"}, []string{"a"}, []string{"z", "m"}},
		{"no_match_returns_a", []string{"a"}, []string{"x", "y"}, []string{"a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := subtractStrings(c.a, c.b)
			if !equalListModelsStringSet(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			// For order-preservation cases, also check exact slice ordering.
			if c.name == "preserves_order" || c.name == "remove_one" || c.name == "no_match_returns_a" {
				if len(got) != len(c.want) {
					t.Fatalf("len got %d, want %d", len(got), len(c.want))
				}
				for i := range got {
					if got[i] != c.want[i] {
						t.Fatalf("at %d: got %q, want %q", i, got[i], c.want[i])
					}
				}
			}
		})
	}
}
