package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelserver/modelserver/internal/collector"
	"github.com/modelserver/modelserver/internal/config"
	"github.com/modelserver/modelserver/internal/crypto"
	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// Handler handles proxied LLM API requests.
type Handler struct {
	store         *store.Store
	collector     *collector.Collector
	channelRouter *ChannelRouter
	encryptionKey []byte
	logger        *slog.Logger
	maxBodySize   int64
}

// NewHandler creates a new proxy handler.
func NewHandler(st *store.Store, coll *collector.Collector, router *ChannelRouter, encKey []byte, logger *slog.Logger, cfg config.ServerConfig) *Handler {
	return &Handler{
		store:         st,
		collector:     coll,
		channelRouter: router,
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

	channelAPIKey, err := crypto.Decrypt(h.encryptionKey, channel.APIKeyEncrypted)
	if err != nil {
		h.logger.Error("failed to decrypt channel key", "channel_id", channel.ID, "error", err)
		writeProxyError(w, http.StatusInternalServerError, "channel configuration error")
		return
	}

	traceID := TraceIDFromContext(r.Context())

	logger := h.logger.With(
		"project_id", project.ID,
		"api_key_id", apiKey.ID,
		"channel_id", channel.ID,
		"model", model,
		"trace_id", traceID,
		"streaming", isStreaming,
	)

	startTime := time.Now()

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			directorSetUpstream(req, channel.BaseURL, string(channelAPIKey))
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				duration := time.Since(startTime).Milliseconds()
				h.collector.Record(types.Request{
					ProjectID:  project.ID,
					APIKeyID:   apiKey.ID,
					ChannelID:  channel.ID,
					TraceID:    traceID,
					Provider:   channel.Provider,
					Model:      model,
					Streaming:  isStreaming,
					Status:     types.RequestStatusError,
					StatusCode: resp.StatusCode,
					LatencyMs:  duration,
				})
				return nil
			}

			if isStreaming {
				return h.interceptStreaming(resp, project, apiKey, channel, model, traceID, startTime, logger)
			}
			return h.interceptNonStreaming(resp, project, apiKey, channel, model, traceID, startTime, logger)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("proxy error", "error", err)
			writeProxyError(w, http.StatusBadGateway, "upstream error")
		},
	}

	proxy.ServeHTTP(w, r)
}

func (h *Handler) interceptNonStreaming(resp *http.Response, project *types.Project, apiKey *types.APIKey, channel *types.Channel, model, traceID string, startTime time.Time, logger *slog.Logger) error {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		logger.Error("failed to read response body", "error", err)
		resp.Body = io.NopCloser(bytes.NewReader(nil))
		return nil
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	parsedModel, _, usage, err := ParseNonStreamingResponse(body)
	if err != nil {
		logger.Warn("failed to parse response", "error", err)
		return nil
	}
	if parsedModel != "" {
		model = parsedModel
	}

	duration := time.Since(startTime).Milliseconds()

	h.collector.Record(types.Request{
		ProjectID:           project.ID,
		APIKeyID:            apiKey.ID,
		ChannelID:           channel.ID,
		TraceID:             traceID,
		Provider:            channel.Provider,
		Model:               model,
		Streaming:           false,
		Status:              types.RequestStatusSuccess,
		StatusCode:          resp.StatusCode,
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheCreationTokens: usage.CacheCreationInputTokens,
		CacheReadTokens:     usage.CacheReadInputTokens,
		LatencyMs:           duration,
	})

	logger.Info("request completed", "input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens, "duration_ms", duration)
	return nil
}

func (h *Handler) interceptStreaming(resp *http.Response, project *types.Project, apiKey *types.APIKey, channel *types.Channel, model, traceID string, startTime time.Time, logger *slog.Logger) error {
	resp.Body = newStreamInterceptor(resp.Body, startTime, func(parsedModel, msgID string, usage anthropic.Usage, ttft int64) {
		if parsedModel != "" {
			model = parsedModel
		}
		duration := time.Since(startTime).Milliseconds()

		h.collector.Record(types.Request{
			ProjectID:           project.ID,
			APIKeyID:            apiKey.ID,
			ChannelID:           channel.ID,
			TraceID:             traceID,
			Provider:            channel.Provider,
			Model:               model,
			Streaming:           true,
			Status:              types.RequestStatusSuccess,
			StatusCode:          resp.StatusCode,
			InputTokens:         usage.InputTokens,
			OutputTokens:        usage.OutputTokens,
			CacheCreationTokens: usage.CacheCreationInputTokens,
			CacheReadTokens:     usage.CacheReadInputTokens,
			LatencyMs:           duration,
			TTFTMs:              ttft,
		})

		logger.Info("streaming request completed",
			"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens,
			"duration_ms", duration, "ttft_ms", ttft)
	})
	return nil
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
