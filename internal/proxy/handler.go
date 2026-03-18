package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"

	"github.com/modelserver/modelserver/internal/collector"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
	"github.com/tidwall/sjson"
)

// Handler handles proxied LLM API requests.
type Handler struct {
	executor    *Executor
	router      *Router
	store       *store.Store
	collector   *collector.Collector
	logger      *slog.Logger
	maxBodySize int64
}

// NewHandler creates a new proxy handler.
func NewHandler(executor *Executor, router *Router, st *store.Store, coll *collector.Collector, logger *slog.Logger, maxBodySize int64) *Handler {
	return &Handler{
		executor:    executor,
		router:      router,
		store:       st,
		collector:   coll,
		logger:      logger,
		maxBodySize: maxBodySize,
	}
}

// HandleMessages proxies Anthropic /v1/messages requests.
func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	h.handleProxyRequest(w, r)
}

// HandleResponses proxies OpenAI /v1/responses requests.
func (h *Handler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	h.handleProxyRequest(w, r)
}

// handleProxyRequest is the shared implementation for HandleMessages and HandleResponses.
// The Executor handles provider detection automatically through the Router.
func (h *Handler) handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	apiKey := APIKeyFromContext(r.Context())
	project := ProjectFromContext(r.Context())
	if apiKey == nil || project == nil {
		writeProxyError(w, http.StatusInternalServerError, "missing auth context")
		return
	}

	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBodySize))
	if err != nil {
		writeProxyError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var reqShape struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	json.Unmarshal(bodyBytes, &reqShape)

	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, reqShape.Model) {
		writeProxyError(w, http.StatusForbidden, "model not allowed for this API key")
		return
	}

	policy := PolicyFromContext(r.Context())
	traceID := TraceIDFromContext(r.Context())

	// Register the trace in the database.
	if traceID != "" {
		source := TraceSourceFromContext(r.Context())
		if err := h.store.EnsureTrace(project.ID, traceID, source); err != nil {
			h.logger.Warn("failed to ensure trace", "error", err)
		}
	}

	// Insert a pending request record before proxying.
	pendingReq := &types.Request{
		ProjectID: project.ID,
		APIKeyID:  apiKey.ID,
		TraceID:   traceID,
		Model:     reqShape.Model,
		Streaming: reqShape.Stream,
		Status:    types.RequestStatusProcessing,
		ClientIP:  r.RemoteAddr,
	}
	if err := h.store.CreateRequest(pendingReq); err != nil {
		h.logger.Warn("failed to insert pending request", "error", err)
		pendingReq.ID = ""
	}

	reqCtx := &RequestContext{
		ProjectID:   project.ID,
		APIKeyID:    apiKey.ID,
		Model:       reqShape.Model,
		IsStream:    reqShape.Stream,
		TraceID:     traceID,
		TraceSource: TraceSourceFromContext(r.Context()),
		SessionID:   traceID, // Use trace ID for session stickiness
		ClientIP:    r.RemoteAddr,
		Policy:      policy,
		APIKey:      apiKey,
		Project:     project,
		RequestID:   pendingReq.ID,
	}

	h.executor.Execute(w, r, reqCtx)
}

// HandleCountTokens proxies Anthropic /v1/messages/count_tokens requests.
func (h *Handler) HandleCountTokens(w http.ResponseWriter, r *http.Request) {
	apiKey := APIKeyFromContext(r.Context())
	project := ProjectFromContext(r.Context())
	if apiKey == nil || project == nil {
		writeProxyError(w, http.StatusInternalServerError, "missing auth context")
		return
	}

	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBodySize))
	if err != nil {
		writeProxyError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var reqShape struct {
		Model string `json:"model"`
	}
	json.Unmarshal(bodyBytes, &reqShape)

	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, reqShape.Model) {
		writeProxyError(w, http.StatusForbidden, "model not allowed for this API key")
		return
	}

	// Use router to find an upstream for count_tokens (Anthropic/ClaudeCode only).
	group, err := h.router.Match(project.ID, reqShape.Model)
	if err != nil {
		writeProxyError(w, http.StatusServiceUnavailable, "no upstreams available for model "+reqShape.Model)
		return
	}
	candidates := h.router.SelectWithRetry(r.Context(), group, "")

	// Filter to Anthropic/ClaudeCode only (count_tokens isn't supported by other providers).
	var selected *SelectedUpstream
	for _, c := range candidates {
		if c.Upstream.Provider == types.ProviderAnthropic || c.Upstream.Provider == types.ProviderClaudeCode {
			selected = c
			break
		}
	}
	if selected == nil {
		writeProxyError(w, http.StatusServiceUnavailable, "no Anthropic upstreams available for model "+reqShape.Model)
		return
	}

	// Resolve model name.
	actualModel := selected.Upstream.ResolveModel(reqShape.Model)
	if actualModel != reqShape.Model {
		bodyBytes, _ = sjson.SetBytes(bodyBytes, "model", actualModel)
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			if selected.Upstream.Provider == types.ProviderClaudeCode {
				accessToken := ParseClaudeCodeAccessToken(selected.APIKey)
				directorSetClaudeCodeUpstream(req, selected.Upstream.BaseURL, accessToken)
			} else {
				directorSetUpstream(req, selected.Upstream.BaseURL, selected.APIKey)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.logger.Error("count_tokens proxy error", "error", err)
			writeProxyError(w, http.StatusBadGateway, "upstream error")
		},
	}
	proxy.ServeHTTP(w, r)
}

func directorSetUpstream(req *http.Request, baseURL, apiKey string) {
	req.URL.Scheme = "https"
	if baseURL != "" {
		if target, err := url.Parse(baseURL); err == nil {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			if target.Path != "" && target.Path != "/" {
				req.URL.Path = path.Join(target.Path, req.URL.Path)
			}
		}
	}
	req.Host = req.URL.Host

	// Remove user credentials so they are never forwarded to the upstream provider.
	req.Header.Del("Authorization")
	req.Header.Del("x-api-key")

	// Remove Accept-Encoding so that Go's http.Transport controls compression.
	// When a client sends Accept-Encoding (e.g. gzip), the Transport forwards it
	// to the upstream but does NOT auto-decompress the response — leaving the
	// streamInterceptor unable to parse the compressed SSE bytes. By deleting
	// the header, the Transport adds its own Accept-Encoding, auto-decompresses,
	// and the interceptor always sees plain-text SSE data.
	req.Header.Del("Accept-Encoding")

	// Suppress X-Forwarded-For so the client's IP is never forwarded to
	// the upstream provider, preventing geo-restriction errors.
	req.Header["X-Forwarded-For"] = nil

	// Set the channel's own API key for the upstream request.
	req.Header.Set("x-api-key", apiKey)
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
}

func modelInList(list []string, model string) bool {
	for _, m := range list {
		if m == model {
			return true
		}
	}
	return false
}
