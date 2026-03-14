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

func TestTraceMiddleware_RequireSession_RejectsAnthropicPrefix(t *testing.T) {
	cfg := config.TraceConfig{
		TraceHeader:    "X-Trace-Id",
		RequireSession: true,
	}

	handler := TraceMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be reached")
	}))

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
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
