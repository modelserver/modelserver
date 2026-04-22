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
	"github.com/modelserver/modelserver/internal/httplog"
	"github.com/modelserver/modelserver/internal/collector"
	"github.com/modelserver/modelserver/internal/modelcatalog"
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
	catalog     modelcatalog.Catalog
	logger      *slog.Logger
	maxBodySize int64
	httpLogger  *httplog.Logger
}

// NewHandler creates a new proxy handler.
func NewHandler(executor *Executor, router *Router, st *store.Store, coll *collector.Collector, catalog modelcatalog.Catalog, logger *slog.Logger, maxBodySize int64, bl *httplog.Logger) *Handler {
	return &Handler{
		executor:    executor,
		router:      router,
		store:       st,
		collector:   coll,
		catalog:     catalog,
		logger:      logger,
		maxBodySize: maxBodySize,
		httpLogger:  bl,
	}
}

// resolveModel looks up a raw client-supplied model name in the catalog.
// On success it returns the canonical name. On unknown or disabled the
// response has already been written in the shape of the ingress provider
// and the caller must return. `ingress` selects the error envelope.
func (h *Handler) resolveModel(w http.ResponseWriter, rawModel, ingress string) (string, bool) {
	if h.catalog == nil {
		return rawModel, true
	}
	m, ok := h.catalog.Lookup(rawModel)
	if !ok {
		suggestions := modelcatalog.Suggest(h.catalog, strings.ToLower(rawModel), 2, 3)
		writeUnsupportedModelError(w, ingress, rawModel, suggestions, "unknown")
		return "", false
	}
	if m.Status == types.ModelStatusDisabled {
		writeUnsupportedModelError(w, ingress, rawModel, nil, "disabled")
		return "", false
	}
	return m.Name, true
}

// HandleMessages proxies Anthropic /v1/messages (stream + non-stream).
// Routes are matched against KindAnthropicMessages.
func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	h.handleProxyRequest(w, r, IngressAnthropic, types.KindAnthropicMessages)
}

// HandleResponses proxies OpenAI /v1/responses (stream + non-stream).
// Routes are matched against KindOpenAIResponses.
func (h *Handler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	h.handleProxyRequest(w, r, IngressOpenAI, types.KindOpenAIResponses)
}

// HandleChatCompletions proxies OpenAI Chat Completions format requests.
// Routes are matched against KindOpenAIChatCompletions.
func (h *Handler) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	h.handleProxyRequest(w, r, IngressOpenAI, types.KindOpenAIChatCompletions)
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
	// Supports both colon and slash separators:
	//   "gemini-3-flash:generateContent"  (canonical Gemini API format)
	//   "gemini-3-flash/generateContent"  (some clients use slash)
	wildcard := chi.URLParam(r, "*")
	var model, method string
	if i := strings.LastIndex(wildcard, ":"); i >= 0 {
		model = wildcard[:i]
		method = wildcard[i+1:]
	} else if i := strings.LastIndex(wildcard, "/"); i >= 0 {
		model = wildcard[:i]
		method = wildcard[i+1:]
	} else {
		writeGeminiError(w, http.StatusBadRequest, "invalid Gemini API path: missing method separator (: or /)")
		return
	}

	if model == "" {
		writeGeminiError(w, http.StatusBadRequest, "invalid Gemini API path: missing model")
		return
	}

	// Reject model names containing path-significant characters to prevent
	// path traversal or URL manipulation in the upstream request URL.
	if strings.ContainsAny(model, "?#\\") || strings.Contains(model, "..") {
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

	// Catalog lookup: unknown/disabled → 400 unsupported_model in Gemini shape.
	// The canonical name flows downstream through reqCtx.Model; the upstream
	// URL is built by gemini.go from that context value, so we don't need to
	// rewrite r.URL.Path here.
	canonical, ok := h.resolveModel(w, model, IngressGemini)
	if !ok {
		return
	}
	model = canonical

	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, canonical) {
		writeGeminiError(w, http.StatusForbidden, "model not allowed for this API key")
		return
	}

	policy := PolicyFromContext(r.Context())
	traceID := TraceIDFromContext(r.Context())

	oauthGrantID := OAuthGrantIDFromContext(r.Context())

	metadata := make(map[string]string)
	if v := r.Header.Get("User-Agent"); v != "" {
		metadata["user_agent"] = v
	}

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
		Model:            model,
		ModelRef:         ModelFromContext(r.Context()),
		IsStream:         isStream,
		RequestKind:      types.KindGoogleGenerateContent,
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

// handleProxyRequest is the shared implementation for HandleMessages, HandleResponses,
// and HandleChatCompletions. ingress is used for model-resolution error formatting;
// kind is set on RequestContext so the router can match the right route.
func (h *Handler) handleProxyRequest(w http.ResponseWriter, r *http.Request, ingress, kind string) {
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

	canonical, ok := h.resolveModel(w, reqShape.Model, ingress)
	if !ok {
		return
	}

	// CCH validation must happen against the client's original body, BEFORE
	// any server-side rewrite below. Result is consumed in the metadata block.
	// Publisher filter is applied at write-time; here we validate unconditionally
	// so non-Anthropic paths (ValidateCCH returns Absent quickly) don't need
	// special handling.
	cchStatus, cchClient, cchExpected := ValidateCCH(bodyBytes)
	fpStatus, fpClient, fpExpected := ValidateFingerprint(bodyBytes)

	if canonical != reqShape.Model {
		bodyBytes, _ = sjson.SetBytes(bodyBytes, "model", canonical)
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
		reqShape.Model = canonical
	}

	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, canonical) {
		writeProxyError(w, http.StatusForbidden, "model not allowed for this API key")
		return
	}

	policy := PolicyFromContext(r.Context())
	traceID := TraceIDFromContext(r.Context())

	oauthGrantID := OAuthGrantIDFromContext(r.Context())

	// Capture notable client headers as metadata.
	metadata := make(map[string]string)
	if v := r.Header.Get("Anthropic-Beta"); v != "" {
		metadata["anthropic_beta"] = v
	}
	if v := r.Header.Get("Anthropic-Version"); v != "" {
		metadata["anthropic_version"] = v
	}
	if v := r.Header.Get("User-Agent"); v != "" {
		metadata["user_agent"] = v
	}

	// Record CCH validation result (computed earlier, against the original
	// client body). Observability only — no behavior change. Gated on Anthropic
	// publisher since other publishers don't use the Claude Code CCH protocol.
	if m := ModelFromContext(r.Context()); m != nil && m.Publisher == types.PublisherAnthropic {
		metadata["cch_status"] = string(cchStatus)
		if cchStatus == CCHStatusMismatch {
			metadata["cch_client"] = cchClient
			metadata["cch_expected"] = cchExpected
		}
		metadata["fingerprint_status"] = string(fpStatus)
		if fpStatus == CCHStatusMismatch {
			metadata["fingerprint_client"] = fpClient
			metadata["fingerprint_expected"] = fpExpected
		}
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
		ModelRef:         ModelFromContext(r.Context()),
		IsStream:         reqShape.Stream,
		RequestKind:      kind,
		TraceID:          traceID,
		TraceSource:      TraceSourceFromContext(r.Context()),
		SessionID:        traceID, // Use trace ID for session stickiness
		ClientIP:         r.RemoteAddr,
		Policy:           policy,
		APIKey:           apiKey,
		Project:          project,
		RequestID:        pendingReq.ID,
	}

	if h.httpLogger != nil {
		if m := ModelFromContext(r.Context()); m != nil && m.Publisher == types.PublisherAnthropic {
			reqCtx.HttpLogEnabled = true
		}
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

	canonical, ok := h.resolveModel(w, reqShape.Model, IngressAnthropic)
	if !ok {
		return
	}
	if canonical != reqShape.Model {
		bodyBytes, _ = sjson.SetBytes(bodyBytes, "model", canonical)
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
		reqShape.Model = canonical
	}

	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, canonical) {
		writeProxyError(w, http.StatusForbidden, "model not allowed for this API key")
		return
	}

	// Use router to find an upstream for count_tokens (Anthropic/ClaudeCode only).
	group, err := h.router.Match(project.ID, reqShape.Model, types.KindAnthropicCountTokens)
	if err != nil {
		writeProxyError(w, http.StatusNotFound, "no route configured for model "+reqShape.Model)
		return
	}
	candidates := h.router.SelectWithRetry(r.Context(), group, "", reqShape.Model)

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

// ingressForProviders maps the allowlist of upstream provider types (set per
// endpoint in HandleMessages / HandleResponses / HandleChatCompletions) back
// to the ingress family whose error envelope clients expect. Body-based
// endpoints that accept Anthropic-compatible bodies return IngressAnthropic;
// OpenAI-flavoured endpoints return IngressOpenAI.
func ingressForProviders(allowed []string) string {
	for _, p := range allowed {
		switch p {
		case types.ProviderOpenAI, types.ProviderVertexOpenAI:
			return IngressOpenAI
		case types.ProviderGemini, types.ProviderVertexGoogle:
			return IngressGemini
		}
	}
	return IngressAnthropic
}

