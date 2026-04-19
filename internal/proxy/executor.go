package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelserver/modelserver/internal/httplog"
	"github.com/modelserver/modelserver/internal/collector"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/metrics"
	"github.com/modelserver/modelserver/internal/modelcatalog"
	"github.com/modelserver/modelserver/internal/ratelimit"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
	"github.com/tidwall/sjson"
)

// ErrMissingDefaultCreditRate is returned when an extra-usage settle attempt
// finds no DefaultCreditRate on the resolved catalog model. We log + skip
// settlement (no charge, but the request is still logged with
// is_extra_usage=true) so admins can backfill and the next request succeeds.
var ErrMissingDefaultCreditRate = errors.New("extra usage: model has no DefaultCreditRate")

// RequestContext carries all request-scoped data through the Executor pipeline.
// It is populated by the handler before calling Execute.
type RequestContext struct {
	ProjectID        string
	APIKeyID         string
	OAuthGrantID     string
	UserID           string
	Model            string   // Original canonical model name from the client request
	ModelRef         *types.Model // Resolved catalog entry (needed for extra-usage billing)
	ActualModel      string   // After ModelMap resolution (set per-attempt by Executor)
	IsStream         bool
	AllowedProviders []string // If non-empty, only route to upstreams with these providers
	TraceID          string
	TraceSource      string
	SessionID        string
	ClientIP         string
	Policy           *types.RateLimitPolicy
	APIKey           *types.APIKey
	Project          *types.Project
	RequestID        string // DB request ID (pending record)

	// Extra-usage state. `HasExtraUsageCtx`/`ExtraUsageCtx` are filled by
	// Execute() from the inbound r.Context so the deferred streaming callback
	// (which fires after r is gone) can still settle. The remaining fields
	// are outputs populated by settleExtraUsage.
	HasExtraUsageCtx          bool
	ExtraUsageCtx             ExtraUsageContext
	IsExtraUsage              bool
	ExtraUsageCostFen         int64
	ExtraUsageReason          string
	ExtraUsageBalanceAfterFen int64

	// HTTP logging state. HttpLogEnabled is set by the handler based on
	// model publisher. The Captured* fields are populated by Execute()
	// just before committing the response, capturing the actual upstream
	// request data (after TransformBody + SetUpstream + sanitize).
	HttpLogEnabled            bool
	CapturedUpstreamBody      []byte
	CapturedUpstreamHeaders   http.Header
	CapturedRequestTruncated  bool
}

// proxyResult classifies the outcome of a single upstream attempt.
type proxyResult int

const (
	proxyResultCommit    proxyResult = iota // Success or non-retryable error: commit to client
	proxyResultRetryable                    // Retryable error: try next upstream
)

// Executor replaces httputil.ReverseProxy with an http.Client-based proxy engine
// that supports cross-upstream retry with per-provider body transformations.
type Executor struct {
	router         *Router
	httpClient     *http.Client
	store          *store.Store
	collector      *collector.Collector
	rateLimiter    ratelimit.RateLimiter
	catalog        modelcatalog.Catalog
	logger         *slog.Logger
	maxBodySize    int64
	extraUsageCfg  config.ExtraUsageConfig
	httpLogger     *httplog.Logger
	httpLogCfg     config.HttpLogConfig
}

// NewExecutor creates a new Executor wired to the given Router and dependencies.
func NewExecutor(
	router *Router,
	st *store.Store,
	coll *collector.Collector,
	limiter ratelimit.RateLimiter,
	catalog modelcatalog.Catalog,
	logger *slog.Logger,
	maxBodySize int64,
	extraUsageCfg config.ExtraUsageConfig,
	bl *httplog.Logger,
	blCfg config.HttpLogConfig,
) *Executor {
	return &Executor{
		router:        router,
		catalog:       catalog,
		extraUsageCfg: extraUsageCfg,
		httpClient: &http.Client{
			// No timeout here; streaming responses can be long-lived.
			// Per-upstream timeouts are applied via request context.
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
				// DisableCompression so we control Accept-Encoding ourselves
				// (same behavior as the existing ReverseProxy setup).
				DisableCompression: true,
			},
		},
		store:       st,
		collector:   coll,
		rateLimiter: limiter,
		logger:      logger,
		maxBodySize: maxBodySize,
		httpLogger:  bl,
		httpLogCfg:  blCfg,
	}
}

// catalogDefaultRate returns the catalog's default credit rate for a
// (canonical) model name, or nil if the catalog is unwired or the model is
// unknown / has no default set. Consumed by billing's fallback chain.
func (e *Executor) catalogDefaultRate(canonical string) *types.CreditRate {
	if e.catalog == nil {
		return nil
	}
	m, ok := e.catalog.Get(canonical)
	if !ok {
		return nil
	}
	return m.DefaultCreditRate
}

// Execute proxies a request through the routing pipeline with retry support.
// It matches the request to an upstream group, selects candidates, and
// attempts each in order until one succeeds or all are exhausted.
func (e *Executor) Execute(w http.ResponseWriter, r *http.Request, reqCtx *RequestContext) {
	// Capture extra-usage context from the inbound request context onto
	// reqCtx so the deferred streaming callback (which fires after r is
	// gone) still has what it needs for settlement.
	if euCtx, has := ExtraUsageContextFromContext(r.Context()); has {
		reqCtx.HasExtraUsageCtx = true
		reqCtx.ExtraUsageCtx = euCtx
	}

	// 1. Match the request to an upstream group.
	group, err := e.router.Match(reqCtx.ProjectID, reqCtx.Model)
	if err != nil {
		writeProxyError(w, http.StatusNotFound, "no route configured for model "+reqCtx.Model)
		return
	}

	// 2. Get ordered list of upstream candidates (primary + retry fallbacks).
	candidates := e.router.SelectWithRetry(r.Context(), group, reqCtx.SessionID, reqCtx.Model)

	if len(candidates) == 0 {
		e.logger.Warn("SelectWithRetry returned no candidates",
			"model", reqCtx.Model,
			"group_members", len(group.members))
		// Log why each member was skipped.
		for _, m := range group.members {
			uid := m.upstream.ID
			e.logger.Warn("upstream skipped",
				"upstream_id", uid,
				"upstream_name", m.upstream.Name,
				"status", m.upstream.Status,
				"health", e.router.healthChecker.Status(uid).String(),
				"circuit_open", !e.router.circuitBreaker.CanPass(uid),
				"concurrent", e.router.connTracker.Count(uid),
				"max_concurrent", m.upstream.MaxConcurrent)
		}
	}

	// 2b. Filter by allowed providers if the handler specified a constraint.
	// This ensures /v1/messages only goes to Anthropic/Bedrock/ClaudeCode and
	// /v1/responses only goes to OpenAI upstreams.
	if len(reqCtx.AllowedProviders) > 0 {
		allowed := make(map[string]bool, len(reqCtx.AllowedProviders))
		for _, p := range reqCtx.AllowedProviders {
			allowed[p] = true
		}
		filtered := candidates[:0]
		for _, c := range candidates {
			if allowed[c.Upstream.Provider] {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}

	if len(candidates) == 0 {
		writeProxyError(w, http.StatusServiceUnavailable, "no upstreams available")
		return
	}

	// 3. Read and buffer the original request body (for potential retries).
	originalBody, err := io.ReadAll(io.LimitReader(r.Body, e.maxBodySize+1))
	if err != nil {
		writeProxyError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if int64(len(originalBody)) > e.maxBodySize {
		writeProxyError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	// 4. Cache transformed bodies per (provider, resolvedModel) pair to avoid
	//    redundant transforms when retrying across upstreams with the same
	//    provider AND the same model resolution. Different ModelMap entries
	//    on upstreams of the same provider produce different bodies.
	bodyCache := make(map[string][]byte) // "provider:resolvedModel" -> transformed body

	// 5. Get retry policy from the group.
	var retryPolicy *types.RetryPolicy
	if group.group.RetryPolicy != nil {
		retryPolicy = group.group.RetryPolicy
	}

	startTime := time.Now()

	// Track whether we've already attempted an OAuth token refresh for a
	// claudecode upstream on this request. We retry at most once per request
	// to avoid infinite loops.
	claudeCodeOAuthRetried := false

	// 6. Retry loop: try each candidate in order.
	for attempt, candidate := range candidates {
		upstream := candidate.Upstream
		transformer := GetProviderTransformer(upstream.Provider)

		logger := e.logger.With(
			"project_id", reqCtx.ProjectID,
			"api_key_id", reqCtx.APIKeyID,
			"upstream_id", upstream.ID,
			"model", reqCtx.Model,
			"attempt", attempt+1,
			"streaming", reqCtx.IsStream,
		)

		// 6a. Resolve model name via upstream's ModelMap.
		actualModel := upstream.ResolveModel(reqCtx.Model)
		reqCtx.ActualModel = actualModel

		// 6b. Get or compute transformed body for this (provider, resolvedModel) pair.
		//     Different upstreams of the same provider may resolve to different model
		//     names via ModelMap, producing different request bodies.
		cacheKey := upstream.Provider + ":" + actualModel
		transformedBody, ok := bodyCache[cacheKey]
		if !ok {
			// Start with original body. If the model was remapped and this is
			// not Bedrock (which strips the model field), rewrite it in the body.
			bodyForTransform := originalBody
			if actualModel != reqCtx.Model && upstream.Provider != types.ProviderBedrock && upstream.Provider != types.ProviderVertexAnthropic && upstream.Provider != types.ProviderGemini && upstream.Provider != types.ProviderVertexGoogle {
				bodyForTransform, _ = sjson.SetBytes(append([]byte{}, originalBody...), "model", actualModel)
			}

			transformedBody, err = transformer.TransformBody(bodyForTransform, actualModel, reqCtx.IsStream, r.Header)
			if err != nil {
				logger.Error("body transform failed", "provider", upstream.Provider, "error", err)
				// Transform failure is not retryable; skip this upstream.
				continue
			}
			bodyCache[cacheKey] = transformedBody
		}

		// 6c. Build a clean outgoing request with NO headers from the original
		//     request. Each provider's SetUpstream is responsible for setting
		//     all necessary headers from scratch.
		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), io.NopCloser(bytes.NewReader(transformedBody)))
		if err != nil {
			logger.Error("failed to create outgoing request", "error", err)
			continue
		}
		outReq.ContentLength = int64(len(transformedBody))
		outReq.Header.Set("Content-Type", "application/json")

		// Forward select client headers that upstream providers need.
		for _, h := range []string{
			"Anthropic-Beta",
			"Anthropic-Dangerous-Direct-Browser-Access",
			"Anthropic-Version",
			"User-Agent",
			"X-App",
			// Claude Code client headers for analytics and request correlation.
			"X-Claude-Code-Session-Id",
			"X-Client-Request-Id",
			"X-Client-App",
			"X-Anthropic-Additional-Protection",
			"X-Claude-Remote-Container-Id",
			"X-Claude-Remote-Session-Id",
		} {
			if v := r.Header.Get(h); v != "" {
				outReq.Header.Set(h, v)
			}
		}
		// Forward X-Stainless-* headers.
		for key, vals := range r.Header {
			if strings.HasPrefix(http.CanonicalHeaderKey(key), "X-Stainless-") {
				outReq.Header[http.CanonicalHeaderKey(key)] = vals
			}
		}

		// For Bedrock, inject the resolved model and streaming flag into the
		// request context so SetUpstream can construct the correct URL path.
		if upstream.Provider == types.ProviderBedrock {
			outReq = withBedrockParams(outReq, actualModel, reqCtx.IsStream)
		}

		// For Vertex Anthropic, inject the resolved model and streaming flag into the
		// request context so SetUpstream can construct the correct URL path.
		if upstream.Provider == types.ProviderVertexAnthropic {
			outReq = withVertexAnthropicParams(outReq, actualModel, reqCtx.IsStream)
		}

		// For Gemini, inject the resolved model and streaming flag into the
		// request context so SetUpstream can construct the correct URL path.
		if upstream.Provider == types.ProviderGemini {
			outReq = withGeminiParams(outReq, actualModel, reqCtx.IsStream)
		}

		// For Vertex Google, inject the resolved model and streaming flag into the
		// request context so SetUpstream can construct the correct URL path.
		if upstream.Provider == types.ProviderVertexGoogle {
			outReq = withVertexGoogleParams(outReq, actualModel, reqCtx.IsStream)
		}

		// For Claude Code upstreams, resolve a fresh OAuth access token
		// via the OAuthTokenManager instead of using the raw credentials JSON.
		apiKeyForUpstream := candidate.APIKey
		if upstream.Provider == types.ProviderClaudeCode {
			if token, err := e.router.GetClaudeCodeAccessToken(upstream.ID); err == nil {
				apiKeyForUpstream = token
			} else {
				logger.Warn("claudecode token resolution failed, falling back to stored key", "error", err)
			}
		}

		// 6d. Configure the outbound request for this upstream.
		if err := transformer.SetUpstream(outReq, upstream, apiKeyForUpstream); err != nil {
			logger.Error("set upstream failed", "error", err)
			continue
		}

		// 6d2. Defensive whitelist: strip any header not explicitly allowed.
		outReq.Header = sanitizeOutboundHeaders(outReq.Header)

		// Debug: log outgoing request details.
		bodyPreview := string(transformedBody)
		if len(bodyPreview) > 300 {
			bodyPreview = bodyPreview[:300] + "..."
		}
		logger.Info("outgoing upstream request",
			"method", outReq.Method,
			"url", outReq.URL.String(),
			"host", outReq.Host,
			"headers", fmt.Sprintf("%v", outReq.Header),
			"body_len", len(transformedBody),
			"body_preview", bodyPreview)

		// 6e. Track the connection. Placed AFTER SetUpstream so that a failed
		// SetUpstream doesn't leave the counter incremented (connection leak).
		e.router.ConnTracker().Acquire(upstream.ID)

		// 6f. Apply per-upstream timeout via request context.
		attemptCtx := outReq.Context()
		var cancelFn context.CancelFunc
		if timeout := upstreamTimeout(upstream, reqCtx.IsStream); timeout > 0 {
			attemptCtx, cancelFn = context.WithTimeout(attemptCtx, timeout)
		}
		outReq = outReq.WithContext(attemptCtx)

		// 6g. Execute the request.
		attemptStart := time.Now()
		resp, doErr := e.httpClient.Do(outReq)

		if cancelFn != nil && doErr != nil {
			// On error, cancel immediately – there is no body to read.
			// On success (both streaming and non-streaming), defer cancel
			// to commitResponse so the timeout context stays alive while
			// the response body is being read/streamed.
			cancelFn()
		}

		// 6h. Evaluate the response for retryability.
		result := e.evaluateResponse(resp, doErr, retryPolicy)

		if result == proxyResultRetryable {
			// Release connection, record error, log, and try next candidate.
			e.router.ConnTracker().Release(upstream.ID)
			e.router.CircuitBreaker().RecordFailure(upstream.ID)
			e.router.Metrics().RecordError(upstream.ID)

			errMsg := "unknown error"
			statusCode := 0
			if doErr != nil {
				errMsg = doErr.Error()
			} else if resp != nil {
				statusCode = resp.StatusCode
				// Read and discard the error body.
				errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				errMsg = string(errBody)
			}

			logger.Warn("upstream attempt failed, retrying",
				"status", statusCode,
				"error", errMsg,
				"duration_ms", time.Since(attemptStart).Milliseconds(),
			)

			if cancelFn != nil {
				cancelFn()
			}
			continue
		}

		// 6h2. Claude Code OAuth 401/403 recovery: if the upstream returned
		//       401 or 403, force-refresh the token and retry once. This
		//       handles server-side token revocation and clock drift (mirrors
		//       the real Claude Code client's withOAuth401Retry behaviour).
		if upstream.Provider == types.ProviderClaudeCode && resp != nil &&
			(resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) &&
			!claudeCodeOAuthRetried {
			claudeCodeOAuthRetried = true

			// Discard the error response body and clean up.
			io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			e.router.ConnTracker().Release(upstream.ID)
			if cancelFn != nil {
				cancelFn()
			}

			newToken, refreshErr := e.router.ForceRefreshClaudeCodeAccessToken(upstream.ID)
			if refreshErr != nil {
				logger.Warn("claudecode OAuth refresh failed on 401/403, returning original error", "error", refreshErr)
				writeProxyError(w, resp.StatusCode, "upstream authentication failed")
				// Complete the request record so it doesn't stay in "processing" forever.
				if reqCtx.RequestID != "" {
					duration := time.Since(startTime).Milliseconds()
					failReq := types.Request{
						OAuthGrantID: reqCtx.OAuthGrantID,
						Status:       types.RequestStatusError,
						LatencyMs:    duration,
						ErrorMessage: "claudecode OAuth refresh failed",
						ClientIP:     reqCtx.ClientIP,
					}
					go func() {
						if err := e.store.CompleteRequest(reqCtx.RequestID, &failReq); err != nil {
							e.logger.Error("failed to complete request", "request_id", reqCtx.RequestID, "error", err)
						}
					}()
				}
				return
			}

			logger.Info("retrying claudecode request after OAuth token refresh", "upstream_id", upstream.ID)

			// Rebuild the outgoing request with the refreshed token.
			// Use outReq.URL (the upstream URL set by SetUpstream), not
			// r.URL (the original client URL).
			retryReq, _ := http.NewRequestWithContext(r.Context(), r.Method, outReq.URL.String(), io.NopCloser(bytes.NewReader(transformedBody)))
			retryReq.ContentLength = int64(len(transformedBody))
			retryReq.Host = outReq.Host
			retryReq.Header = outReq.Header.Clone()
			retryReq.Header.Set("Authorization", "Bearer "+newToken)

			e.router.ConnTracker().Acquire(upstream.ID)

			retryCtx := retryReq.Context()
			var retryCancelFn context.CancelFunc
			if timeout := upstreamTimeout(upstream, reqCtx.IsStream); timeout > 0 {
				retryCtx, retryCancelFn = context.WithTimeout(retryCtx, timeout)
			}
			retryReq = retryReq.WithContext(retryCtx)

			resp, doErr = e.httpClient.Do(retryReq)
			if retryCancelFn != nil && doErr != nil {
				retryCancelFn()
			}
			cancelFn = retryCancelFn

			// Fall through to the normal commit path with the retry result.
		}

		// 6i. Commit: this is the final response (success or non-retryable error).
		//     Only record success in CB/metrics if we got a non-5xx response.
		//     Connection errors (resp==nil) or 5xx responses that weren't retried
		//     (because no retry policy) should still count as failures.
		if resp != nil && resp.StatusCode < 500 {
			e.router.CircuitBreaker().RecordSuccess(upstream.ID)
			e.router.Metrics().RecordSuccess(upstream.ID)
		} else {
			e.router.CircuitBreaker().RecordFailure(upstream.ID)
			e.router.Metrics().RecordError(upstream.ID)
			if doErr != nil {
				logger.Warn("upstream request failed",
					"error", doErr.Error(),
					"duration_ms", time.Since(attemptStart).Milliseconds())
			} else if resp != nil {
				logger.Warn("upstream returned error",
					"status", resp.StatusCode,
					"duration_ms", time.Since(attemptStart).Milliseconds())
			}
		}

		// Bind the session to this upstream for stickiness (only on success).
		if reqCtx.SessionID != "" && resp != nil && resp.StatusCode < 500 {
			e.router.BindSession(reqCtx.SessionID, reqCtx.Model, upstream.ID)
		}

		if e.httpLogger != nil && reqCtx.HttpLogEnabled {
			captured := append([]byte(nil), transformedBody...)
			if e.httpLogCfg.MaxRequestBody > 0 && int64(len(captured)) > e.httpLogCfg.MaxRequestBody {
				captured = captured[:e.httpLogCfg.MaxRequestBody]
				reqCtx.CapturedRequestTruncated = true
			}
			reqCtx.CapturedUpstreamBody = captured
			reqCtx.CapturedUpstreamHeaders = outReq.Header.Clone()
		}

		e.commitResponse(w, resp, candidate, reqCtx, transformer, startTime, cancelFn, logger)
		return
	}

	// 7. All candidates exhausted.
	writeProxyError(w, http.StatusBadGateway, "all upstreams failed")

	// Record the overall failure.
	if reqCtx.RequestID != "" {
		duration := time.Since(startTime).Milliseconds()
		req := types.Request{
			OAuthGrantID: reqCtx.OAuthGrantID,
			Status:       types.RequestStatusError,
			LatencyMs:    duration,
			ErrorMessage: "all upstreams exhausted",
			ClientIP:     reqCtx.ClientIP,
		}
		go func() {
			if err := e.store.CompleteRequest(reqCtx.RequestID, &req); err != nil {
				e.logger.Error("failed to complete request", "request_id", reqCtx.RequestID, "error", err)
			}
		}()
	}
}

// evaluateResponse determines whether a response is retryable based on the
// retry policy and the response status/error.
func (e *Executor) evaluateResponse(resp *http.Response, err error, policy *types.RetryPolicy) proxyResult {
	if policy == nil {
		return proxyResultCommit
	}

	retryOn := make(map[string]bool, len(policy.RetryOn))
	for _, r := range policy.RetryOn {
		retryOn[r] = true
	}

	// Connection error (dial failure, DNS, etc.).
	if err != nil {
		if isConnectionError(err) && retryOn["connection_error"] {
			return proxyResultRetryable
		}
		// Timeout errors.
		if isTimeoutError(err) && retryOn["timeout"] {
			return proxyResultRetryable
		}
		// Unknown network error; treat as connection_error.
		if retryOn["connection_error"] {
			return proxyResultRetryable
		}
		return proxyResultCommit
	}

	if resp == nil {
		return proxyResultCommit
	}

	// 5xx server errors.
	if resp.StatusCode >= 500 && resp.StatusCode < 600 && retryOn["5xx"] {
		return proxyResultRetryable
	}

	// 429 rate limit.
	if resp.StatusCode == http.StatusTooManyRequests && retryOn["429"] {
		return proxyResultRetryable
	}

	// 2xx success or 4xx client errors: commit.
	return proxyResultCommit
}

// commitResponse writes the upstream response to the client and records metrics.
func (e *Executor) commitResponse(
	w http.ResponseWriter,
	resp *http.Response,
	candidate *SelectedUpstream,
	reqCtx *RequestContext,
	transformer ProviderTransformer,
	startTime time.Time,
	cancelFn context.CancelFunc,
	logger *slog.Logger,
) {
	if resp == nil {
		e.router.ConnTracker().Release(candidate.Upstream.ID)
		if cancelFn != nil {
			cancelFn()
		}
		writeProxyError(w, http.StatusBadGateway, "upstream returned no response")
		return
	}

	// Handle error responses (non-2xx).
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e.commitErrorResponse(w, resp, candidate, reqCtx, startTime, logger)
		if cancelFn != nil {
			cancelFn()
		}
		return
	}

	// Copy response headers, stripping hop-by-hop headers that must not be
	// forwarded by proxies (RFC 7230 §6.1). httputil.ReverseProxy does this
	// automatically; since we use http.Client directly, we must do it ourselves.
	for key, values := range resp.Header {
		if isHopByHopHeader(key) {
			continue
		}
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}

	if reqCtx.IsStream {
		e.commitStreamingResponse(w, resp, candidate, reqCtx, transformer, startTime, cancelFn, logger)
	} else {
		e.commitNonStreamingResponse(w, resp, candidate, reqCtx, transformer, startTime, logger)
		if cancelFn != nil {
			cancelFn()
		}
	}
}

// commitErrorResponse handles non-2xx responses by logging, recording metrics,
// and forwarding the error to the client.
func (e *Executor) commitErrorResponse(
	w http.ResponseWriter,
	resp *http.Response,
	candidate *SelectedUpstream,
	reqCtx *RequestContext,
	startTime time.Time,
	logger *slog.Logger,
) {
	defer resp.Body.Close()
	e.router.ConnTracker().Release(candidate.Upstream.ID)

	duration := time.Since(startTime).Milliseconds()
	status := types.RequestStatusError
	if resp.StatusCode == http.StatusTooManyRequests {
		status = types.RequestStatusRateLimited
	}

	// Read error body for logging.
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	logger.Warn("upstream error response",
		"status", resp.StatusCode,
		"body", string(errBody),
	)

	// Record the error request.
	req := types.Request{
		ProjectID:    reqCtx.Project.ID,
		APIKeyID:     reqCtx.APIKeyID,
		OAuthGrantID: reqCtx.OAuthGrantID,
		UpstreamID:   candidate.Upstream.ID,
		TraceID:      reqCtx.TraceID,
		Provider:     candidate.Upstream.Provider,
		Model:        reqCtx.Model,
		Streaming:    reqCtx.IsStream,
		Status:       status,
		LatencyMs:    duration,
		ErrorMessage: string(errBody),
		ClientIP:     reqCtx.ClientIP,
	}
	if reqCtx.RequestID != "" {
		go func() {
			if err := e.store.CompleteRequest(reqCtx.RequestID, &req); err != nil {
				logger.Error("failed to complete request", "request_id", reqCtx.RequestID, "error", err)
			}
		}()
	} else {
		e.collector.Record(req)
	}

	if e.httpLogger != nil && reqCtx.HttpLogEnabled && reqCtx.RequestID != "" {
		rec := &httplog.Record{
			RequestID:       reqCtx.RequestID,
			ProjectID:       reqCtx.ProjectID,
			RequestHeaders:  reqCtx.CapturedUpstreamHeaders,
			RequestBody:     reqCtx.CapturedUpstreamBody,
			ResponseHeaders: resp.Header.Clone(),
			ResponseBody:    errBody,
			ResponseStatus:  resp.StatusCode,
			Truncated:       reqCtx.CapturedRequestTruncated,
		}
		e.httpLogger.Enqueue(rec)
	}

	// Forward the error response to the client (stripping hop-by-hop headers).
	for key, values := range resp.Header {
		if isHopByHopHeader(key) {
			continue
		}
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(errBody)
}

// commitStreamingResponse wraps the stream with the provider's interceptor
// and copies it to the client.
func (e *Executor) commitStreamingResponse(
	w http.ResponseWriter,
	resp *http.Response,
	candidate *SelectedUpstream,
	reqCtx *RequestContext,
	transformer ProviderTransformer,
	startTime time.Time,
	cancelFn context.CancelFunc,
	logger *slog.Logger,
) {
	// For Bedrock streaming, set the correct Content-Type header since the
	// upstream returns application/vnd.amazon.eventstream but our adapter
	// converts it to SSE.
	if candidate.Upstream.Provider == types.ProviderBedrock {
		w.Header().Set("Content-Type", "text/event-stream")
	}

	// Extra-usage header hints that can be written pre-body. Cost/balance
	// require post-stream settlement and are therefore NOT emittable here;
	// clients learn final balance via GET /extra-usage.
	if reqCtx.HasExtraUsageCtx {
		w.Header().Set("X-Extra-Usage", "true")
		w.Header().Set("X-Extra-Usage-Reason", reqCtx.ExtraUsageCtx.Reason)
	}

	w.WriteHeader(resp.StatusCode)

	// Wrap with provider-specific stream interceptor.
	wrapped := transformer.WrapStream(resp.Body, startTime, func(metrics StreamMetrics) {
		// Release connection when stream completes.
		e.router.ConnTracker().Release(candidate.Upstream.ID)
		if cancelFn != nil {
			cancelFn()
		}

		model := metrics.Model
		if model == "" {
			model = reqCtx.Model
		}

		duration := time.Since(startTime).Milliseconds()

		var credits float64
		if reqCtx.Policy != nil {
			credits = reqCtx.Policy.ComputeCreditsWithDefault(model, e.catalogDefaultRate(model), metrics.InputTokens, metrics.OutputTokens, metrics.CacheCreationTokens, metrics.CacheReadTokens)
		}

		// Settle extra-usage BEFORE persisting the request row so the ledger
		// and the request's is_extra_usage columns stay consistent.
		e.settleExtraUsage(context.Background(), reqCtx, types.TokenUsage{
			InputTokens:         metrics.InputTokens,
			OutputTokens:        metrics.OutputTokens,
			CacheCreationTokens: metrics.CacheCreationTokens,
			CacheReadTokens:     metrics.CacheReadTokens,
		})

		req := types.Request{
			ProjectID:           reqCtx.Project.ID,
			APIKeyID:            reqCtx.APIKeyID,
			OAuthGrantID:        reqCtx.OAuthGrantID,
			UpstreamID:          candidate.Upstream.ID,
			TraceID:             reqCtx.TraceID,
			MsgID:               metrics.MsgID,
			Provider:            candidate.Upstream.Provider,
			Model:               model,
			Streaming:           true,
			Status:              types.RequestStatusSuccess,
			InputTokens:         metrics.InputTokens,
			OutputTokens:        metrics.OutputTokens,
			CacheCreationTokens: metrics.CacheCreationTokens,
			CacheReadTokens:     metrics.CacheReadTokens,
			CreditsConsumed:     credits,
			LatencyMs:           duration,
			TTFTMs:              metrics.TTFTMs,
			ClientIP:            reqCtx.ClientIP,
			IsExtraUsage:        reqCtx.IsExtraUsage,
			ExtraUsageCostFen:   reqCtx.ExtraUsageCostFen,
			ExtraUsageReason:    reqCtx.ExtraUsageReason,
		}
		if reqCtx.RequestID != "" {
			go func() {
				if err := e.store.CompleteRequest(reqCtx.RequestID, &req); err != nil {
					logger.Error("failed to complete request", "request_id", reqCtx.RequestID, "error", err)
				}
			}()
		} else {
			e.collector.Record(req)
		}

		logger.Info("request completed",
			"msg_id", metrics.MsgID,
			"status", types.RequestStatusSuccess,
			"streaming", true,
			"input_tokens", metrics.InputTokens,
			"output_tokens", metrics.OutputTokens,
			"cache_creation_tokens", metrics.CacheCreationTokens,
			"cache_read_tokens", metrics.CacheReadTokens,
			"credits", credits,
			"duration_ms", duration,
			"ttft_ms", metrics.TTFTMs,
		)

		if e.rateLimiter != nil {
			e.rateLimiter.PostRecord(context.Background(), reqCtx.Project.ID, reqCtx.APIKeyID, reqCtx.UserID, model, types.TokenUsage{
				InputTokens:         metrics.InputTokens,
				OutputTokens:        metrics.OutputTokens,
				CacheCreationTokens: metrics.CacheCreationTokens,
				CacheReadTokens:     metrics.CacheReadTokens,
			})
		}
	})

	// Optionally wrap with TeeReadCloser for http logging.
	var streamReader io.ReadCloser = wrapped
	if e.httpLogger != nil && reqCtx.HttpLogEnabled && reqCtx.RequestID != "" {
		maxResp := e.httpLogCfg.MaxResponseBody
		if maxResp <= 0 {
			maxResp = 52428800
		}
		respHeaders := resp.Header.Clone()
		streamReader = httplog.NewTeeReadCloser(wrapped, maxResp, func(data []byte, truncated bool) {
			rec := &httplog.Record{
				RequestID:       reqCtx.RequestID,
				ProjectID:       reqCtx.ProjectID,
				RequestHeaders:  reqCtx.CapturedUpstreamHeaders,
				RequestBody:     reqCtx.CapturedUpstreamBody,
				ResponseHeaders: respHeaders,
				ResponseBody:    data,
				ResponseStatus:  resp.StatusCode,
				Streaming:       true,
				Truncated:       truncated || reqCtx.CapturedRequestTruncated,
			}
			e.httpLogger.Enqueue(rec)
		})
	}

	// Flush streaming data to the client.
	flusher, _ := w.(http.Flusher)

	n, copyErr := copyWithFlush(streamReader, w, flusher)
	if copyErr != nil {
		logger.Warn("stream_interrupted",
			"request_id", reqCtx.RequestID,
			"upstream_id", candidate.Upstream.ID,
			"bytes_sent", n,
			"error", copyErr.Error(),
		)
		e.router.Metrics().RecordError(candidate.Upstream.ID)
	}

	streamReader.Close()
}

// commitNonStreamingResponse reads the full response, parses metrics, and
// writes it to the client.
func (e *Executor) commitNonStreamingResponse(
	w http.ResponseWriter,
	resp *http.Response,
	candidate *SelectedUpstream,
	reqCtx *RequestContext,
	transformer ProviderTransformer,
	startTime time.Time,
	logger *slog.Logger,
) {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	e.router.ConnTracker().Release(candidate.Upstream.ID)

	if err != nil {
		logger.Error("failed to read response body", "error", err)
		// Clear upstream headers already copied by commitResponse and
		// return a proper error so the client doesn't see 200 + empty body.
		for k := range w.Header() {
			w.Header().Del(k)
		}
		writeProxyError(w, http.StatusBadGateway, "failed to read upstream response body")
		return
	}

	// For extra-usage requests we need to parse + settle BEFORE writing
	// headers so X-Extra-Usage-Cost-Fen / X-Extra-Usage-Balance-Fen can be
	// included. Otherwise keep the original order (write headers+body first,
	// parse metrics afterwards) so non-extra-usage traffic's TTFB is not
	// affected.
	var respMetrics *ResponseMetrics
	var parseErr error
	if reqCtx.HasExtraUsageCtx {
		respMetrics, parseErr = transformer.ParseResponse(body)
		if parseErr != nil {
			logger.Warn("failed to parse response", "error", parseErr)
		}
		if respMetrics != nil {
			e.settleExtraUsage(context.Background(), reqCtx, types.TokenUsage{
				InputTokens:         respMetrics.InputTokens,
				OutputTokens:        respMetrics.OutputTokens,
				CacheCreationTokens: respMetrics.CacheCreationTokens,
				CacheReadTokens:     respMetrics.CacheReadTokens,
			})
		}
		writeExtraUsageSuccessHeaders(w, reqCtx)
	}

	// Write the response body to the client.
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(resp.StatusCode)
	w.Write(body)

	if !reqCtx.HasExtraUsageCtx {
		respMetrics, parseErr = transformer.ParseResponse(body)
		if parseErr != nil {
			logger.Warn("failed to parse response", "error", parseErr)
		}
	}
	if respMetrics == nil {
		// Parse failed entirely; downstream code expects a non-nil value.
		respMetrics = &ResponseMetrics{}
	}

	model := respMetrics.Model
	if model == "" {
		model = reqCtx.Model
	}

	duration := time.Since(startTime).Milliseconds()

	var credits float64
	if reqCtx.Policy != nil {
		credits = reqCtx.Policy.ComputeCreditsWithDefault(model, e.catalogDefaultRate(model), respMetrics.InputTokens, respMetrics.OutputTokens, respMetrics.CacheCreationTokens, respMetrics.CacheReadTokens)
	}

	req := types.Request{
		ProjectID:           reqCtx.Project.ID,
		APIKeyID:            reqCtx.APIKeyID,
		OAuthGrantID:        reqCtx.OAuthGrantID,
		UpstreamID:          candidate.Upstream.ID,
		TraceID:             reqCtx.TraceID,
		MsgID:               respMetrics.MsgID,
		Provider:            candidate.Upstream.Provider,
		Model:               model,
		Streaming:           false,
		Status:              types.RequestStatusSuccess,
		InputTokens:         respMetrics.InputTokens,
		OutputTokens:        respMetrics.OutputTokens,
		CacheCreationTokens: respMetrics.CacheCreationTokens,
		CacheReadTokens:     respMetrics.CacheReadTokens,
		CreditsConsumed:     credits,
		LatencyMs:           duration,
		ClientIP:            reqCtx.ClientIP,
		IsExtraUsage:        reqCtx.IsExtraUsage,
		ExtraUsageCostFen:   reqCtx.ExtraUsageCostFen,
		ExtraUsageReason:    reqCtx.ExtraUsageReason,
	}
	if reqCtx.RequestID != "" {
		go func() {
			if err := e.store.CompleteRequest(reqCtx.RequestID, &req); err != nil {
				logger.Error("failed to complete request", "request_id", reqCtx.RequestID, "error", err)
			}
		}()
	} else {
		e.collector.Record(req)
	}

	logger.Info("request completed",
		"msg_id", respMetrics.MsgID,
		"status", types.RequestStatusSuccess,
		"streaming", false,
		"input_tokens", respMetrics.InputTokens,
		"output_tokens", respMetrics.OutputTokens,
		"cache_creation_tokens", respMetrics.CacheCreationTokens,
		"cache_read_tokens", respMetrics.CacheReadTokens,
		"credits", credits,
		"duration_ms", duration,
	)

	if e.rateLimiter != nil {
		e.rateLimiter.PostRecord(context.Background(), reqCtx.Project.ID, reqCtx.APIKeyID, reqCtx.UserID, model, types.TokenUsage{
			InputTokens:         respMetrics.InputTokens,
			OutputTokens:        respMetrics.OutputTokens,
			CacheCreationTokens: respMetrics.CacheCreationTokens,
			CacheReadTokens:     respMetrics.CacheReadTokens,
		})
	}

	if e.httpLogger != nil && reqCtx.HttpLogEnabled && reqCtx.RequestID != "" {
		rec := &httplog.Record{
			RequestID:       reqCtx.RequestID,
			ProjectID:       reqCtx.ProjectID,
			RequestHeaders:  reqCtx.CapturedUpstreamHeaders,
			RequestBody:     reqCtx.CapturedUpstreamBody,
			ResponseHeaders: resp.Header.Clone(),
			ResponseBody:    body,
			ResponseStatus:  resp.StatusCode,
			Truncated:       reqCtx.CapturedRequestTruncated,
		}
		if e.httpLogCfg.MaxResponseBody > 0 && int64(len(rec.ResponseBody)) > e.httpLogCfg.MaxResponseBody {
			rec.ResponseBody = rec.ResponseBody[:e.httpLogCfg.MaxResponseBody]
			rec.Truncated = true
		}
		e.httpLogger.Enqueue(rec)
	}
}

// copyWithFlush copies from src to dst, flushing after each read if a Flusher
// is available. Returns the number of bytes copied and any error.
func copyWithFlush(src io.Reader, dst io.Writer, flusher http.Flusher) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			nw, writeErr := dst.Write(buf[:n])
			total += int64(nw)
			if flusher != nil {
				flusher.Flush()
			}
			if writeErr != nil {
				return total, writeErr
			}
			if nw != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}

// upstreamTimeout returns the appropriate timeout for the upstream.
// This timeout covers the entire round-trip: dial, TLS, request send,
// upstream processing, and response read. Non-streaming LLM calls can
// take well over 30s for large outputs, so the default is 5 minutes
// (same as streaming).
func upstreamTimeout(upstream *types.Upstream, isStream bool) time.Duration {
	if upstream.ReadTimeout > 0 {
		return upstream.ReadTimeout
	}
	return 5 * time.Minute
}

// isConnectionError returns true if the error is a network connection error
// (dial failure, DNS resolution, connection refused, etc.).
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	// Check for net.Error (includes dial and DNS errors).
	var netErr net.Error
	if errors.As(err, &netErr) {
		return !netErr.Timeout()
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	return false
}

// isTimeoutError returns true if the error is a timeout.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// hopByHopHeaders are headers that must not be forwarded by proxies (RFC 7230 §6.1).
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func isHopByHopHeader(key string) bool {
	return hopByHopHeaders[http.CanonicalHeaderKey(key)]
}


// computeExtraUsageCostFen converts a TokenUsage to CNY fen using the
// catalog's DefaultCreditRate. Rate is sourced from the catalog (not the
// plan override) per spec §4.1 — extra usage is always priced at official
// API rates. Returns (cost_fen, credits_used, err). Cost is ceil so
// sub-fen fractions round up to 1.
func computeExtraUsageCostFen(m *types.Model, u types.TokenUsage, creditPriceFen int64) (int64, float64, error) {
	if m == nil || m.DefaultCreditRate == nil {
		return 0, 0, ErrMissingDefaultCreditRate
	}
	if creditPriceFen <= 0 {
		return 0, 0, fmt.Errorf("extra usage: credit_price_fen must be > 0")
	}
	rate := m.DefaultCreditRate
	credits := rate.InputRate*float64(u.InputTokens) +
		rate.OutputRate*float64(u.OutputTokens) +
		rate.CacheCreationRate*float64(u.CacheCreationTokens) +
		rate.CacheReadRate*float64(u.CacheReadTokens)
	if credits <= 0 {
		return 0, credits, nil
	}
	cost := int64(math.Ceil(credits * float64(creditPriceFen) / 1_000_000))
	if cost < 1 {
		cost = 1
	}
	return cost, credits, nil
}

// settleExtraUsage charges the per-project balance when the request was
// routed through the extra-usage path. Call after the response has been
// successfully committed (status=success) and before the pending-request
// row is updated. Mutates rc with the settlement fields so CompleteRequest
// can persist them in the same UPDATE.
//
// All failure modes record metrics + logs and return silently. The request
// itself is still recorded (with is_extra_usage=true) so audit traces reflect
// user intent even when settlement is skipped.
func (e *Executor) settleExtraUsage(ctx context.Context, rc *RequestContext, usage types.TokenUsage) {
	if !rc.HasExtraUsageCtx {
		return
	}
	euCtx := rc.ExtraUsageCtx

	rc.IsExtraUsage = true
	rc.ExtraUsageReason = euCtx.Reason

	if usage.InputTokens+usage.OutputTokens+usage.CacheCreationTokens+usage.CacheReadTokens == 0 {
		// Provider returned no usage — don't synthesise a charge.
		e.logger.Warn("extra_usage_settle_no_usage",
			"project_id", rc.ProjectID, "request_id", rc.RequestID)
		metrics.IncExtraUsageDeduction("no_usage")
		return
	}

	costFen, credits, err := computeExtraUsageCostFen(rc.ModelRef, usage, e.extraUsageCfg.CreditPriceFen)
	if err != nil {
		modelName := rc.Model
		if rc.ModelRef != nil {
			modelName = rc.ModelRef.Name
		}
		e.logger.Error("extra_usage_missing_default_rate",
			"project_id", rc.ProjectID, "model", modelName, "error", err)
		metrics.IncExtraUsageMissingRate(modelName)
		return
	}
	rc.ExtraUsageCostFen = costFen

	newBal, err := e.store.DeductExtraUsage(store.DeductExtraUsageReq{
		ProjectID:   rc.ProjectID,
		AmountFen:   costFen,
		RequestID:   rc.RequestID,
		Reason:      euCtx.Reason,
		Description: fmt.Sprintf("%s | credits=%.2f | model=%s", euCtx.Reason, credits, rc.Model),
	})
	switch {
	case err == nil:
		rc.ExtraUsageBalanceAfterFen = newBal
		metrics.IncExtraUsageDeduction("ok")
		metrics.SetExtraUsageBalance(rc.ProjectID, newBal)
	case errors.Is(err, store.ErrInsufficientBalance):
		e.logger.Warn("extra_usage_underdraft",
			"project_id", rc.ProjectID, "cost_fen", costFen)
		metrics.IncExtraUsageDeduction("insufficient")
		metrics.IncExtraUsageUnderdraft(rc.ProjectID)
	case errors.Is(err, store.ErrMonthlyLimitReached):
		e.logger.Warn("extra_usage_monthly_limit_at_settle",
			"project_id", rc.ProjectID, "cost_fen", costFen)
		metrics.IncExtraUsageDeduction("monthly_limit")
	case errors.Is(err, store.ErrExtraUsageNotEnabled):
		e.logger.Warn("extra_usage_disabled_at_settle",
			"project_id", rc.ProjectID)
		metrics.IncExtraUsageDeduction("not_enabled")
	default:
		e.logger.Error("extra_usage_deduction_failed",
			"project_id", rc.ProjectID, "error", err)
		metrics.IncExtraUsageDeduction("err")
	}
}

// writeExtraUsageSuccessHeaders writes the X-Extra-Usage-* response headers
// onto a successful response. Must be called before the first WriteHeader.
func writeExtraUsageSuccessHeaders(w http.ResponseWriter, rc *RequestContext) {
	if !rc.IsExtraUsage {
		return
	}
	w.Header().Set("X-Extra-Usage", "true")
	if rc.ExtraUsageReason != "" {
		w.Header().Set("X-Extra-Usage-Reason", rc.ExtraUsageReason)
	}
	if rc.ExtraUsageCostFen > 0 {
		w.Header().Set("X-Extra-Usage-Cost-Fen", strconv.FormatInt(rc.ExtraUsageCostFen, 10))
	}
	w.Header().Set("X-Extra-Usage-Balance-Fen", strconv.FormatInt(rc.ExtraUsageBalanceAfterFen, 10))
}

// sanitizeOutboundHeaders returns a new header map containing only headers
// that are safe to send to upstream AI providers. Applied as a defensive
// layer after each provider's SetUpstream has configured its headers.
func sanitizeOutboundHeaders(h http.Header) http.Header {
	allowed := make(http.Header, len(h))
	for key, vals := range h {
		canon := http.CanonicalHeaderKey(key)
		switch {
		case canon == "Content-Type",
			canon == "User-Agent",
			canon == "X-App",
			canon == "Anthropic-Beta",
			canon == "Anthropic-Dangerous-Direct-Browser-Access",
			canon == "Anthropic-Version",
			canon == "X-Api-Key",
			canon == "Authorization",
			// Claude Code client headers for analytics and request correlation.
			canon == "X-Claude-Code-Session-Id",
			canon == "X-Client-Request-Id",
			canon == "X-Client-App",
			canon == "X-Anthropic-Additional-Protection",
			canon == "X-Claude-Remote-Container-Id",
			canon == "X-Claude-Remote-Session-Id",
			// Gemini API key header.
			canon == "X-Goog-Api-Key":
			allowed[canon] = vals
		default:
			if strings.HasPrefix(canon, "X-Stainless-") {
				allowed[canon] = vals
			}
		}
	}
	return allowed
}
