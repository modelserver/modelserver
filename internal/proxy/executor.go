package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/modelserver/modelserver/internal/collector"
	"github.com/modelserver/modelserver/internal/ratelimit"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
	"github.com/tidwall/sjson"
)

// RequestContext carries all request-scoped data through the Executor pipeline.
// It is populated by the handler before calling Execute.
type RequestContext struct {
	ProjectID   string
	APIKeyID    string
	Model       string      // Original model name from the client request
	ActualModel string      // After ModelMap resolution (set per-attempt by Executor)
	IsStream    bool
	TraceID     string
	TraceSource string
	SessionID   string
	ClientIP    string
	Policy      *types.RateLimitPolicy
	APIKey      *types.APIKey
	Project     *types.Project
	RequestID   string // DB request ID (pending record)
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
	router      *Router
	httpClient  *http.Client
	store       *store.Store
	collector   *collector.Collector
	rateLimiter ratelimit.RateLimiter
	logger      *slog.Logger
	maxBodySize int64
}

// NewExecutor creates a new Executor wired to the given Router and dependencies.
func NewExecutor(
	router *Router,
	st *store.Store,
	coll *collector.Collector,
	limiter ratelimit.RateLimiter,
	logger *slog.Logger,
	maxBodySize int64,
) *Executor {
	return &Executor{
		router: router,
		httpClient: &http.Client{
			// No timeout here; streaming responses can be long-lived.
			// Per-upstream timeouts are applied via request context.
			Transport: &http.Transport{
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
	}
}

// Execute proxies a request through the routing pipeline with retry support.
// It matches the request to an upstream group, selects candidates, and
// attempts each in order until one succeeds or all are exhausted.
func (e *Executor) Execute(w http.ResponseWriter, r *http.Request, reqCtx *RequestContext) {
	// 1. Match the request to an upstream group.
	group, err := e.router.Match(reqCtx.ProjectID, reqCtx.Model)
	if err != nil {
		writeProxyError(w, http.StatusServiceUnavailable, "no upstreams available for model "+reqCtx.Model)
		return
	}

	// 2. Get ordered list of upstream candidates (primary + retry fallbacks).
	candidates := e.router.SelectWithRetry(r.Context(), group, reqCtx.SessionID)
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
			if actualModel != reqCtx.Model && upstream.Provider != types.ProviderBedrock {
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

		// 6c. Clone the original request for this attempt.
		outReq := r.Clone(r.Context())
		outReq.Body = io.NopCloser(bytes.NewReader(transformedBody))
		outReq.ContentLength = int64(len(transformedBody))

		// For Bedrock, inject the resolved model and streaming flag into the
		// request context so SetUpstream can construct the correct URL path.
		if upstream.Provider == types.ProviderBedrock {
			outReq = withBedrockParams(outReq, actualModel, reqCtx.IsStream)
		}

		// 6d. Configure the outbound request for this upstream.
		if err := transformer.SetUpstream(outReq, upstream, candidate.APIKey); err != nil {
			logger.Error("set upstream failed", "error", err)
			continue
		}

		// 6e. Track the connection.
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

		if cancelFn != nil {
			// For streaming: we defer cancel until the stream is fully consumed.
			// For non-streaming or errors: cancel immediately after reading.
			if doErr != nil || !reqCtx.IsStream {
				cancelFn()
			}
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
		}

		// Bind the session to this upstream for stickiness (only on success).
		if reqCtx.SessionID != "" && resp != nil && resp.StatusCode < 500 {
			e.router.BindSession(reqCtx.SessionID, upstream.ID)
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
		// No retry policy: commit whatever we got (success or error).
		if err != nil {
			return proxyResultCommit
		}
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

	// Copy response headers.
	for key, values := range resp.Header {
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

	// Forward the error response to the client.
	for key, values := range resp.Header {
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
			credits = reqCtx.Policy.ComputeCredits(model, metrics.InputTokens, metrics.OutputTokens, metrics.CacheCreationTokens, metrics.CacheReadTokens)
		}

		req := types.Request{
			ProjectID:           reqCtx.Project.ID,
			APIKeyID:            reqCtx.APIKeyID,
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
			e.rateLimiter.PostRecord(context.Background(), reqCtx.Project.ID, reqCtx.APIKeyID, model, types.TokenUsage{
				InputTokens:         metrics.InputTokens,
				OutputTokens:        metrics.OutputTokens,
				CacheCreationTokens: metrics.CacheCreationTokens,
				CacheReadTokens:     metrics.CacheReadTokens,
			})
		}
	})

	// Flush streaming data to the client.
	flusher, _ := w.(http.Flusher)

	n, copyErr := copyWithFlush(wrapped, w, flusher)
	if copyErr != nil {
		logger.Warn("stream_interrupted",
			"request_id", reqCtx.RequestID,
			"upstream_id", candidate.Upstream.ID,
			"bytes_sent", n,
			"error", copyErr.Error(),
		)
		e.router.Metrics().RecordError(candidate.Upstream.ID)
	}

	wrapped.Close()
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
		w.WriteHeader(resp.StatusCode)
		return
	}

	// Write the response body to the client.
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(resp.StatusCode)
	w.Write(body)

	// Parse response metrics.
	metrics, parseErr := transformer.ParseResponse(body)
	if parseErr != nil {
		logger.Warn("failed to parse response", "error", parseErr)
		return
	}

	model := metrics.Model
	if model == "" {
		model = reqCtx.Model
	}

	duration := time.Since(startTime).Milliseconds()

	var credits float64
	if reqCtx.Policy != nil {
		credits = reqCtx.Policy.ComputeCredits(model, metrics.InputTokens, metrics.OutputTokens, metrics.CacheCreationTokens, metrics.CacheReadTokens)
	}

	req := types.Request{
		ProjectID:           reqCtx.Project.ID,
		APIKeyID:            reqCtx.APIKeyID,
		UpstreamID:          candidate.Upstream.ID,
		TraceID:             reqCtx.TraceID,
		MsgID:               metrics.MsgID,
		Provider:            candidate.Upstream.Provider,
		Model:               model,
		Streaming:           false,
		Status:              types.RequestStatusSuccess,
		InputTokens:         metrics.InputTokens,
		OutputTokens:        metrics.OutputTokens,
		CacheCreationTokens: metrics.CacheCreationTokens,
		CacheReadTokens:     metrics.CacheReadTokens,
		CreditsConsumed:     credits,
		LatencyMs:           duration,
		ClientIP:            reqCtx.ClientIP,
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
		"streaming", false,
		"input_tokens", metrics.InputTokens,
		"output_tokens", metrics.OutputTokens,
		"cache_creation_tokens", metrics.CacheCreationTokens,
		"cache_read_tokens", metrics.CacheReadTokens,
		"credits", credits,
		"duration_ms", duration,
	)

	if e.rateLimiter != nil {
		e.rateLimiter.PostRecord(context.Background(), reqCtx.Project.ID, reqCtx.APIKeyID, model, types.TokenUsage{
			InputTokens:         metrics.InputTokens,
			OutputTokens:        metrics.OutputTokens,
			CacheCreationTokens: metrics.CacheCreationTokens,
			CacheReadTokens:     metrics.CacheReadTokens,
		})
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

// upstreamTimeout returns the appropriate timeout for the upstream. Streaming
// requests use ReadTimeout (default 300s); non-streaming use DialTimeout
// (default 30s) as a more aggressive timeout.
func upstreamTimeout(upstream *types.Upstream, isStream bool) time.Duration {
	if isStream {
		if upstream.ReadTimeout > 0 {
			return upstream.ReadTimeout
		}
		return 5 * time.Minute // default streaming timeout
	}
	if upstream.ReadTimeout > 0 {
		return upstream.ReadTimeout
	}
	return 30 * time.Second // default non-streaming timeout
}

// isConnectionError returns true if the error is a network connection error
// (dial failure, DNS resolution, connection refused, etc.).
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	// Check for net.Error (includes dial and DNS errors).
	var netErr net.Error
	if ok := errorAs(err, &netErr); ok {
		return !netErr.Timeout()
	}
	// Check for net.OpError (connection refused, etc.).
	var opErr *net.OpError
	if ok := errorAs(err, &opErr); ok {
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
	if ok := errorAs(err, &netErr); ok {
		return netErr.Timeout()
	}
	return false
}

// errorAs is a thin wrapper around errors.As to avoid a top-level import cycle
// concern (there is none, but this keeps the helper testable).
func errorAs(err error, target interface{}) bool {
	// Use type assertion chain to avoid importing errors package for this simple case.
	switch t := target.(type) {
	case *net.Error:
		if ne, ok := err.(net.Error); ok {
			*t = ne
			return true
		}
	case **net.OpError:
		if oe, ok := err.(*net.OpError); ok {
			*t = oe
			return true
		}
	}
	return false
}
