package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelserver/modelserver/internal/config"
)

func TestTraceMiddleware_RequireSession_RejectsAnonymousPOST(t *testing.T) {
	cfg := config.TraceConfig{
		TraceHeader:    "X-Trace-Id",
		RequireSession: true,
	}

	handler := TraceMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be reached")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestTraceMiddleware_RequireSession_AllowsWithTraceID(t *testing.T) {
	cfg := config.TraceConfig{
		TraceHeader:    "X-Trace-Id",
		RequireSession: true,
	}

	called := false
	handler := TraceMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("X-Trace-Id", "session-123")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler should have been called")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestTraceMiddleware_RequireSession_AllowsGETWithoutSession(t *testing.T) {
	cfg := config.TraceConfig{
		TraceHeader:    "X-Trace-Id",
		RequireSession: true,
	}

	called := false
	handler := TraceMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler should have been called for GET")
	}
}

func TestTraceMiddleware_RequireSessionDisabled_AllowsAnonymous(t *testing.T) {
	cfg := config.TraceConfig{
		TraceHeader:    "X-Trace-Id",
		RequireSession: false,
	}

	called := false
	handler := TraceMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler should have been called when require_session is false")
	}
}

func TestExtractClaudeTraceID(t *testing.T) {
	tests := []struct {
		name   string
		userID string
		want   string
	}{
		// Current JSON format (Claude Code ≥ v2.1)
		{
			name:   "json format with all fields",
			userID: `{"device_id":"264a5b050a3a389cafb40a1e7f5980bd6450b1f366e404b00c2a40a550ab945a","account_uuid":"04633d98-7e59-4420-afb8-675468f67c71","session_id":"68c6d0ca-3753-43b2-aa92-8ccb0701ebff"}`,
			want:   "68c6d0ca-3753-43b2-aa92-8ccb0701ebff",
		},
		{
			name:   "json format with empty account_uuid",
			userID: `{"device_id":"264a5b050a3a389cafb40a1e7f5980bd6450b1f366e404b00c2a40a550ab945a","account_uuid":"","session_id":"68c6d0ca-3753-43b2-aa92-8ccb0701ebff"}`,
			want:   "68c6d0ca-3753-43b2-aa92-8ccb0701ebff",
		},
		{
			name:   "json format with extra metadata fields",
			userID: `{"custom_key":"custom_value","device_id":"264a5b050a3a389cafb40a1e7f5980bd6450b1f366e404b00c2a40a550ab945a","account_uuid":"","session_id":"aabbccdd-1234-5678-9abc-def012345678"}`,
			want:   "aabbccdd-1234-5678-9abc-def012345678",
		},
		// Legacy string format with account UUID
		{
			name:   "legacy format with account uuid",
			userID: "user_264a5b050a3a389cafb40a1e7f5980bd6450b1f366e404b00c2a40a550ab945a_account_04633d98-7e59-4420-afb8-675468f67c71_session_68c6d0ca-3753-43b2-aa92-8ccb0701ebff",
			want:   "68c6d0ca-3753-43b2-aa92-8ccb0701ebff",
		},
		// Legacy string format without account UUID
		{
			name:   "legacy format without account uuid",
			userID: "user_264a5b050a3a389cafb40a1e7f5980bd6450b1f366e404b00c2a40a550ab945a_account__session_68c6d0ca-3753-43b2-aa92-8ccb0701ebff",
			want:   "68c6d0ca-3753-43b2-aa92-8ccb0701ebff",
		},
		// Invalid inputs
		{
			name:   "empty string",
			userID: "",
			want:   "",
		},
		{
			name:   "random string",
			userID: "not-a-valid-format",
			want:   "",
		},
		{
			name:   "json without session_id",
			userID: `{"device_id":"abc","account_uuid":"def"}`,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractClaudeTraceID(tt.userID)
			if got != tt.want {
				t.Errorf("extractClaudeTraceID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTraceMiddleware_RequireSession_RejectsOpenAIResponses(t *testing.T) {
	cfg := config.TraceConfig{
		TraceHeader:    "X-Trace-Id",
		RequireSession: true,
	}

	handler := TraceMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be reached for /v1/responses without session")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}
