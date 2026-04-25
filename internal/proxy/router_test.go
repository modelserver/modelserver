package proxy

import (
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
