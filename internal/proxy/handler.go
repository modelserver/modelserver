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
	"strings"

	"github.com/go-chi/chi/v5"
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
// Only routes to Anthropic, Bedrock, and ClaudeCode providers.
func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	h.handleProxyRequest(w, r, []string{
		types.ProviderAnthropic,
		types.ProviderBedrock,
		types.ProviderClaudeCode,
		types.ProviderVertex,
	})
}

// HandleResponses proxies OpenAI /v1/responses requests.
// Only routes to OpenAI providers.
func (h *Handler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	h.handleProxyRequest(w, r, []string{
		types.ProviderOpenAI,
	})
}

// HandleGemini proxies Gemini API requests (generateContent / streamGenerateContent).
// The model and streaming flag are extracted from the URL path rather than the body.
// Example: POST /v1beta/models/gemini-3-flash:generateContent
func (h *Handler) HandleGemini(w http.ResponseWriter, r *http.Request) {
	apiKey := APIKeyFromContext(r.Context())
	project := ProjectFromContext(r.Context())
	if apiKey == nil || project == nil {
		writeGeminiError(w, http.StatusInternalServerError, "missing auth context")
		return
	}

	// Extract model and method from URL wildcard.
	// e.g. "gemini-3-flash:generateContent" or "gemini-2.5-pro:streamGenerateContent"
	wildcard := chi.URLParam(r, "*")
	lastColon := strings.LastIndex(wildcard, ":")
	if lastColon < 0 {
		writeGeminiError(w, http.StatusBadRequest, "invalid Gemini API path: missing method")
		return
	}
	model := wildcard[:lastColon]
	method := wildcard[lastColon+1:]

	if model == "" {
		writeGeminiError(w, http.StatusBadRequest, "invalid Gemini API path: missing model")
		return
	}

	// Reject model names containing path-significant characters to prevent
	// path traversal or URL manipulation in the upstream request URL.
	if strings.ContainsAny(model, "/?#\\") || strings.Contains(model, "..") {
		writeGeminiError(w, http.StatusBadRequest, "invalid model name")
		return
	}

	var isStream bool
	switch method {
	case "generateContent":
		isStream = false
	case "streamGenerateContent":
		isStream = true
	default:
		writeGeminiError(w, http.StatusBadRequest, "unsupported Gemini method: "+method)
		return
	}

	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBodySize))
	if err != nil {
		writeGeminiError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, model) {
		writeGeminiError(w, http.StatusForbidden, "model not allowed for this API key")
		return
	}

	policy := PolicyFromContext(r.Context())
	traceID := TraceIDFromContext(r.Context())

	if traceID != "" {
		source := TraceSourceFromContext(r.Context())
		if err := h.store.EnsureTrace(project.ID, traceID, source); err != nil {
			h.logger.Warn("failed to ensure trace", "error", err)
		}
	}

	oauthGrantID := OAuthGrantIDFromContext(r.Context())

	pendingReq := &types.Request{
		ProjectID:    project.ID,
		APIKeyID:     apiKey.ID,
		OAuthGrantID: oauthGrantID,
		CreatedBy:    apiKey.CreatedBy,
		TraceID:      traceID,
		Model:        model,
		Streaming:    isStream,
		Status:       types.RequestStatusProcessing,
		ClientIP:     r.RemoteAddr,
	}
	if err := h.store.CreateRequest(pendingReq); err != nil {
		h.logger.Warn("failed to insert pending request", "error", err)
		pendingReq.ID = ""
	}

	reqCtx := &RequestContext{
		ProjectID:        project.ID,
		APIKeyID:         apiKey.ID,
		OAuthGrantID:     oauthGrantID,
		UserID:           apiKey.CreatedBy,
		Model:            model,
		IsStream:         isStream,
		AllowedProviders: []string{types.ProviderGemini},
		TraceID:          traceID,
		TraceSource:      TraceSourceFromContext(r.Context()),
		SessionID:        traceID,
		ClientIP:         r.RemoteAddr,
		Policy:           policy,
		APIKey:           apiKey,
		Project:          project,
		RequestID:        pendingReq.ID,
	}

	h.executor.Execute(w, r, reqCtx)
}

// handleProxyRequest is the shared implementation for HandleMessages and HandleResponses.
// allowedProviders constrains which provider types can serve this request.
func (h *Handler) handleProxyRequest(w http.ResponseWriter, r *http.Request, allowedProviders []string) {
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

	oauthGrantID := OAuthGrantIDFromContext(r.Context())

	// Capture notable client headers as metadata.
	metadata := make(map[string]string)
	if v := r.Header.Get("Anthropic-Beta"); v != "" {
		metadata["anthropic_beta"] = v
	}
	if v := r.Header.Get("Anthropic-Version"); v != "" {
		metadata["anthropic_version"] = v
	}

	// Insert a pending request record before proxying.
	pendingReq := &types.Request{
		ProjectID:    project.ID,
		APIKeyID:     apiKey.ID,
		OAuthGrantID: oauthGrantID,
		CreatedBy:    apiKey.CreatedBy,
		TraceID:      traceID,
		Model:        reqShape.Model,
		Streaming:    reqShape.Stream,
		Status:       types.RequestStatusProcessing,
		ClientIP:     r.RemoteAddr,
		Metadata:     metadata,
	}
	if err := h.store.CreateRequest(pendingReq); err != nil {
		h.logger.Warn("failed to insert pending request", "error", err)
		pendingReq.ID = ""
	}

	reqCtx := &RequestContext{
		ProjectID:        project.ID,
		APIKeyID:         apiKey.ID,
		OAuthGrantID:     oauthGrantID,
		UserID:           apiKey.CreatedBy,
		Model:            reqShape.Model,
		IsStream:         reqShape.Stream,
		AllowedProviders: allowedProviders,
		TraceID:          traceID,
		TraceSource:      TraceSourceFromContext(r.Context()),
		SessionID:        traceID, // Use trace ID for session stickiness
		ClientIP:         r.RemoteAddr,
		Policy:           policy,
		APIKey:           apiKey,
		Project:          project,
		RequestID:        pendingReq.ID,
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
		writeProxyError(w, http.StatusNotFound, "no route configured for model "+reqShape.Model)
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
				// Resolve fresh OAuth token via the manager.
				accessToken := ParseClaudeCodeAccessToken(selected.APIKey)
				if token, err := h.router.GetClaudeCodeAccessToken(selected.Upstream.ID); err == nil {
					accessToken = token
				}
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

	// Set all required headers from scratch — do not inherit from client.
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

func modelInList(list []string, model string) bool {
	for _, m := range list {
		if m == model {
			return true
		}
	}
	return false
}
