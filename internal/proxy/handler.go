package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelserver/modelserver/internal/collector"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/ratelimit"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// Handler handles proxied LLM API requests.
type Handler struct {
	store         *store.Store
	collector     *collector.Collector
	channelRouter *ChannelRouter
	rateLimiter   ratelimit.RateLimiter
	encryptionKey []byte
	logger        *slog.Logger
	maxBodySize   int64
}

// NewHandler creates a new proxy handler.
func NewHandler(st *store.Store, coll *collector.Collector, router *ChannelRouter, limiter ratelimit.RateLimiter, encKey []byte, logger *slog.Logger, cfg config.ServerConfig) *Handler {
	return &Handler{
		store:         st,
		collector:     coll,
		channelRouter: router,
		rateLimiter:   limiter,
		encryptionKey: encKey,
		logger:        logger,
		maxBodySize:   cfg.MaxRequestBody,
	}
}

// HandleMessages proxies Anthropic /v1/messages requests.
func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	apiKey := APIKeyFromContext(r.Context())
	project := ProjectFromContext(r.Context())
	if apiKey == nil || project == nil {
		writeProxyError(w, http.StatusInternalServerError, "missing auth context")
		return
	}

	clientIP := r.RemoteAddr

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
	isStreaming := reqShape.Stream
	model := reqShape.Model

	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, model) {
		writeProxyError(w, http.StatusForbidden, "model not allowed for this API key")
		return
	}

	candidates := h.channelRouter.MatchChannels(project.ID, model)
	if len(candidates) == 0 {
		writeProxyError(w, http.StatusServiceUnavailable, "no channels available for model "+model)
		return
	}

	channel := SelectChannel(candidates)
	if channel == nil {
		writeProxyError(w, http.StatusServiceUnavailable, "no channels available")
		return
	}

	channelAPIKey := h.channelRouter.GetChannelKey(channel.ID)
	if channelAPIKey == "" {
		h.logger.Error("no decrypted key for channel", "channel_id", channel.ID)
		writeProxyError(w, http.StatusInternalServerError, "channel configuration error")
		return
	}

	// Transform request body for Bedrock provider.
	if channel.Provider == types.ProviderBedrock {
		betaValues := r.Header.Values("anthropic-beta")
		bodyBytes, err = transformBedrockBody(bodyBytes, betaValues)
		if err != nil {
			writeProxyError(w, http.StatusInternalServerError, "failed to transform request for bedrock")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
	}

	policy := PolicyFromContext(r.Context())
	traceID := TraceIDFromContext(r.Context())
	threadID := ThreadIDFromContext(r.Context())

	logger := h.logger.With(
		"project_id", project.ID,
		"api_key_id", apiKey.ID,
		"channel_id", channel.ID,
		"model", model,
		"trace_id", traceID,
		"streaming", isStreaming,
	)

	// Register the trace in the database before creating the request record,
	// since requests.trace_id has a foreign key constraint on traces.id.
	if traceID != "" {
		source := TraceSourceFromContext(r.Context())
		if err := h.store.EnsureTrace(project.ID, traceID, threadID, source); err != nil {
			logger.Warn("failed to ensure trace", "error", err)
		}
	}

	// Insert a pending request record before proxying.
	pendingReq := &types.Request{
		ProjectID: project.ID,
		APIKeyID:  apiKey.ID,
		ChannelID: channel.ID,
		TraceID:   traceID,
		Provider:  channel.Provider,
		Model:     model,
		Streaming: isStreaming,
		Status:    types.RequestStatusProcessing,
		ClientIP:  clientIP,
	}
	if err := h.store.CreateRequest(pendingReq); err != nil {
		logger.Warn("failed to insert pending request", "error", err)
		pendingReq.ID = "" // signal fallback to collector
	}

	startTime := time.Now()

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			if channel.Provider == types.ProviderBedrock {
				directorSetBedrockUpstream(req, channel.BaseURL, channelAPIKey, model, isStreaming)
			} else {
				directorSetUpstream(req, channel.BaseURL, channelAPIKey)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				duration := time.Since(startTime).Milliseconds()
				status := types.RequestStatusError
				if resp.StatusCode == http.StatusTooManyRequests {
					status = types.RequestStatusRateLimited
				}
				req := types.Request{
					ProjectID: project.ID,
					APIKeyID:  apiKey.ID,
					ChannelID: channel.ID,
					TraceID:   traceID,
					Provider:  channel.Provider,
					Model:     model,
					Streaming: isStreaming,
					Status:    status,
					LatencyMs: duration,
					ClientIP:  clientIP,
				}
				if pendingReq.ID != "" {
					go func() {
						if err := h.store.CompleteRequest(pendingReq.ID, &req); err != nil {
							logger.Error("failed to complete request", "request_id", pendingReq.ID, "error", err)
						}
					}()
				} else {
					h.collector.Record(req)
				}
				return nil
			}

			if isStreaming {
				if channel.Provider == types.ProviderBedrock {
					resp.Body = newBedrockStreamAdapter(resp.Body)
					resp.Header.Set("Content-Type", "text/event-stream")
				}
				return h.interceptStreaming(resp, pendingReq.ID, project, apiKey, channel, model, traceID, policy, clientIP, startTime, logger)
			}
			return h.interceptNonStreaming(resp, pendingReq.ID, project, apiKey, channel, model, traceID, policy, clientIP, startTime, logger)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("proxy error", "error", err)
			writeProxyError(w, http.StatusBadGateway, "upstream error")
		},
	}

	proxy.ServeHTTP(w, r)
}

func (h *Handler) interceptNonStreaming(resp *http.Response, requestID string, project *types.Project, apiKey *types.APIKey, channel *types.Channel, model, traceID string, policy *types.RateLimitPolicy, clientIP string, startTime time.Time, logger *slog.Logger) error {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		logger.Error("failed to read response body", "error", err)
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	parsedModel, msgID, usage, err := ParseNonStreamingResponse(body)
	if err != nil {
		logger.Warn("failed to parse response", "error", err)
		return nil
	}
	if parsedModel != "" {
		model = parsedModel
	}

	duration := time.Since(startTime).Milliseconds()

	var credits float64
	if policy != nil {
		credits = policy.ComputeCredits(model, usage.InputTokens, usage.OutputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
	}

	req := types.Request{
		ProjectID:           project.ID,
		APIKeyID:            apiKey.ID,
		ChannelID:           channel.ID,
		TraceID:             traceID,
		MsgID:               msgID,
		Provider:            channel.Provider,
		Model:               model,
		Streaming:           false,
		Status:              types.RequestStatusSuccess,
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationInputTokens,
		CacheReadTokens:     usage.CacheReadInputTokens,
		CreditsConsumed:     credits,
		LatencyMs:           duration,
		ClientIP:            clientIP,
	}
	if requestID != "" {
		go func() {
			if err := h.store.CompleteRequest(requestID, &req); err != nil {
				logger.Error("failed to complete request", "request_id", requestID, "error", err)
			}
		}()
	} else {
		h.collector.Record(req)
	}

	logger.Info("request completed",
		"msg_id", msgID,
		"status", types.RequestStatusSuccess,
		"streaming", false,
		"input_tokens", usage.InputTokens,
		"output_tokens", usage.OutputTokens,
		"cache_creation_tokens", usage.CacheCreationInputTokens,
		"cache_read_tokens", usage.CacheReadInputTokens,
		"credits", credits,
		"duration_ms", duration,
	)

	if h.rateLimiter != nil {
		h.rateLimiter.PostRecord(context.Background(), project.ID, apiKey.ID, model, types.TokenUsage{
			InputTokens:         usage.InputTokens,
			OutputTokens:        usage.OutputTokens,
			CacheCreationTokens: usage.CacheCreationInputTokens,
			CacheReadTokens:     usage.CacheReadInputTokens,
		})
	}

	return nil
}

func (h *Handler) interceptStreaming(resp *http.Response, requestID string, project *types.Project, apiKey *types.APIKey, channel *types.Channel, model, traceID string, policy *types.RateLimitPolicy, clientIP string, startTime time.Time, logger *slog.Logger) error {
	resp.Body = newStreamInterceptor(resp.Body, startTime, func(parsedModel, msgID string, usage anthropic.Usage, ttft int64) {
		if parsedModel != "" {
			model = parsedModel
		}
		duration := time.Since(startTime).Milliseconds()

		var credits float64
		if policy != nil {
			credits = policy.ComputeCredits(model, usage.InputTokens, usage.OutputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
		}

		req := types.Request{
			ProjectID:           project.ID,
			APIKeyID:            apiKey.ID,
			ChannelID:           channel.ID,
			TraceID:             traceID,
			MsgID:               msgID,
			Provider:            channel.Provider,
			Model:               model,
			Streaming:           true,
			Status:              types.RequestStatusSuccess,
			InputTokens:         usage.InputTokens,
			OutputTokens:        usage.OutputTokens,
			CacheCreationTokens: usage.CacheCreationInputTokens,
			CacheReadTokens:     usage.CacheReadInputTokens,
			CreditsConsumed:     credits,
			LatencyMs:           duration,
			TTFTMs:              ttft,
			ClientIP:            clientIP,
		}
		if requestID != "" {
			go func() {
				if err := h.store.CompleteRequest(requestID, &req); err != nil {
					logger.Error("failed to complete request", "request_id", requestID, "error", err)
				}
			}()
		} else {
			h.collector.Record(req)
		}

		logger.Info("request completed",
			"msg_id", msgID,
			"status", types.RequestStatusSuccess,
			"streaming", true,
			"input_tokens", usage.InputTokens,
			"output_tokens", usage.OutputTokens,
			"cache_creation_tokens", usage.CacheCreationInputTokens,
			"cache_read_tokens", usage.CacheReadInputTokens,
			"credits", credits,
			"duration_ms", duration,
			"ttft_ms", ttft,
		)

		if h.rateLimiter != nil {
			h.rateLimiter.PostRecord(context.Background(), project.ID, apiKey.ID, model, types.TokenUsage{
				InputTokens:         usage.InputTokens,
				OutputTokens:        usage.OutputTokens,
				CacheCreationTokens: usage.CacheCreationInputTokens,
				CacheReadTokens:     usage.CacheReadInputTokens,
			})
		}
	})
	return nil
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
	model := reqShape.Model

	if len(apiKey.AllowedModels) > 0 && !modelInList(apiKey.AllowedModels, model) {
		writeProxyError(w, http.StatusForbidden, "model not allowed for this API key")
		return
	}

	candidates := h.channelRouter.MatchChannels(project.ID, model)
	if len(candidates) == 0 {
		writeProxyError(w, http.StatusServiceUnavailable, "no channels available for model "+model)
		return
	}

	channel := SelectChannel(candidates)
	if channel == nil {
		writeProxyError(w, http.StatusServiceUnavailable, "no channels available")
		return
	}

	channelAPIKey := h.channelRouter.GetChannelKey(channel.ID)
	if channelAPIKey == "" {
		h.logger.Error("no decrypted key for channel", "channel_id", channel.ID)
		writeProxyError(w, http.StatusInternalServerError, "channel configuration error")
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			directorSetUpstream(req, channel.BaseURL, channelAPIKey)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.logger.Error("count_tokens proxy error", "error", err, "project_id", project.ID, "model", model)
			writeProxyError(w, http.StatusBadGateway, "upstream error")
		},
	}

	proxy.ServeHTTP(w, r)
}

func directorSetUpstream(req *http.Request, baseURL, apiKey string) {
	req.URL.Scheme = "https"
	if baseURL != "" {
		req.URL.Host = stripScheme(baseURL)
		if hasScheme(baseURL, "http") {
			req.URL.Scheme = "http"
		}
	}
	req.Host = req.URL.Host

	// Remove user credentials so they are never forwarded to the upstream provider.
	req.Header.Del("Authorization")
	req.Header.Del("x-api-key")

	// Set the channel's own API key for the upstream request.
	req.Header.Set("x-api-key", apiKey)
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
}

func stripScheme(u string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if len(u) > len(prefix) && u[:len(prefix)] == prefix {
			return u[len(prefix):]
		}
	}
	return u
}

func hasScheme(u, scheme string) bool {
	return len(u) > len(scheme)+3 && u[:len(scheme)+3] == scheme+"://"
}

func modelInList(list []string, model string) bool {
	for _, m := range list {
		if m == model {
			return true
		}
	}
	return false
}
