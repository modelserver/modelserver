package lb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HealthStatus represents the health state of an upstream.
type HealthStatus int

const (
	HealthUnknown  HealthStatus = iota
	HealthOK                    // Responding normally
	HealthDegraded              // Responding but slow (latency > 2x baseline)
	HealthDown                  // Connection error, timeout, or 5xx
)

// String returns a human-readable name for the health status.
func (s HealthStatus) String() string {
	switch s {
	case HealthUnknown:
		return "unknown"
	case HealthOK:
		return "ok"
	case HealthDegraded:
		return "degraded"
	case HealthDown:
		return "down"
	default:
		return "unknown"
	}
}

const (
	defaultHealthInterval = 30 * time.Second
	defaultHealthTimeout  = 30 * time.Second
	baselineWindowSize    = 10 // Number of recent successful probe latencies to average
)

// TokenFetcher retrieves a valid access token for the given upstream.
// Used by health checker for providers that require dynamic token refresh (e.g., Vertex AI).
type TokenFetcher func(upstreamID string) (string, error)

// HealthChecker performs periodic active health probes against upstreams.
type HealthChecker struct {
	mu             sync.RWMutex
	upstreams      map[string]*healthEntry // upstreamID -> health state
	circuitBreaker *CircuitBreaker         // Shares state with passive CB
	metrics        *UpstreamMetrics
	logger         *slog.Logger
	httpClient     *http.Client
	stop           chan struct{}
	stopOnce       sync.Once // guards close(stop) against double-close panic
	wg             sync.WaitGroup
	tokenFetcher   TokenFetcher           // Optional: for Vertex AI token refresh
}

type healthEntry struct {
	upstreamID      string
	provider        string
	baseURL         string
	testModel       string        // Model to use in probe
	apiKey          string        // Decrypted key for probe auth
	interval        time.Duration // Per-upstream override, default 30s
	timeout         time.Duration // Probe timeout, default 5s
	lastCheck       time.Time
	lastStatus      HealthStatus
	consecutiveOK   int
	consecutiveFail int
	baselineLatency time.Duration   // Rolling average of successful probe latencies
	latencyWindow   []time.Duration // Recent successful probe latencies for rolling average
}

// NewHealthChecker creates a HealthChecker that shares state with the given CircuitBreaker.
func NewHealthChecker(cb *CircuitBreaker, metrics *UpstreamMetrics, logger *slog.Logger, tokenFetcher TokenFetcher) *HealthChecker {
	return &HealthChecker{
		upstreams:      make(map[string]*healthEntry),
		circuitBreaker: cb,
		metrics:        metrics,
		logger:         logger,
		httpClient: &http.Client{
			Timeout: defaultHealthTimeout,
		},
		stop:         make(chan struct{}),
		tokenFetcher: tokenFetcher,
	}
}

// Register adds an upstream to be health-checked.
// If testModel is empty, the upstream is NOT registered (health checking requires a test model).
func (hc *HealthChecker) Register(upstreamID, provider, baseURL, testModel, apiKey string, interval, timeout time.Duration) {
	if testModel == "" {
		hc.logger.Debug("skipping health check registration: no test model",
			"upstream_id", upstreamID)
		return
	}

	if interval <= 0 {
		interval = defaultHealthInterval
	}
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}

	hc.mu.Lock()
	defer hc.mu.Unlock()

	hc.upstreams[upstreamID] = &healthEntry{
		upstreamID:    upstreamID,
		provider:      provider,
		baseURL:       baseURL,
		testModel:     testModel,
		apiKey:        apiKey,
		interval:      interval,
		timeout:       timeout,
		lastStatus:    HealthUnknown,
		latencyWindow: make([]time.Duration, 0, baselineWindowSize),
	}
}

// Deregister removes an upstream from health checking.
func (hc *HealthChecker) Deregister(upstreamID string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	delete(hc.upstreams, upstreamID)
}

// Start begins background health check goroutines.
// For each registered upstream, starts a goroutine that probes at the configured interval.
func (hc *HealthChecker) Start(ctx context.Context) {
	hc.mu.RLock()
	entries := make([]*healthEntry, 0, len(hc.upstreams))
	for _, entry := range hc.upstreams {
		entries = append(entries, entry)
	}
	hc.mu.RUnlock()

	for _, entry := range entries {
		hc.wg.Add(1)
		go hc.runProbeLoop(ctx, entry)
	}

	hc.logger.Info("health checker started", "upstream_count", len(entries))
}

// Stop terminates all health check goroutines and waits for them to finish.
// Safe to call multiple times.
func (hc *HealthChecker) Stop() {
	hc.stopOnce.Do(func() { close(hc.stop) })
	hc.wg.Wait()
	hc.logger.Info("health checker stopped")
}

// runProbeLoop runs the periodic probe loop for a single upstream.
func (hc *HealthChecker) runProbeLoop(ctx context.Context, entry *healthEntry) {
	defer hc.wg.Done()

	ticker := time.NewTicker(entry.interval)
	defer ticker.Stop()

	// Run an initial probe immediately
	status := hc.probe(entry)
	hc.OnProbeResult(entry.upstreamID, status)

	for {
		select {
		case <-ctx.Done():
			return
		case <-hc.stop:
			return
		case <-ticker.C:
			status := hc.probe(entry)
			hc.OnProbeResult(entry.upstreamID, status)
		}
	}
}

// probe sends a minimal inference request to the upstream using the configured TestModel.
// Uses max_tokens=1 to minimize token cost while validating the full request path.
// Returns HealthOK, HealthDegraded (latency > 2x baseline), or HealthDown.
func (hc *HealthChecker) probe(entry *healthEntry) HealthStatus {
	req, err := hc.buildProbeRequest(entry)
	if err != nil {
		hc.logger.Warn("failed to build probe request",
			"upstream_id", entry.upstreamID,
			"error", err)
		return HealthDown
	}

	// Use per-entry timeout
	client := &http.Client{Timeout: entry.timeout}

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)

	if err != nil {
		hc.logger.Debug("probe failed",
			"upstream_id", entry.upstreamID,
			"error", err,
			"latency", latency)
		return HealthDown
	}
	defer resp.Body.Close()

	// 5xx responses indicate upstream failure
	if resp.StatusCode >= 500 {
		hc.logger.Debug("probe returned server error",
			"upstream_id", entry.upstreamID,
			"status_code", resp.StatusCode,
			"latency", latency)
		return HealthDown
	}

	// 4xx responses (except 429) are OK -- they mean the upstream is reachable
	// and processing requests. 429 could indicate overload.
	// For simplicity, treat any non-5xx as "upstream is alive".

	// Update baseline latency with rolling average
	hc.mu.Lock()
	entry.latencyWindow = append(entry.latencyWindow, latency)
	if len(entry.latencyWindow) > baselineWindowSize {
		entry.latencyWindow = entry.latencyWindow[len(entry.latencyWindow)-baselineWindowSize:]
	}
	entry.baselineLatency = averageDuration(entry.latencyWindow)
	hc.mu.Unlock()

	// Check for degraded status (latency > 2x baseline)
	// Only compare if we have enough samples for a meaningful baseline
	if len(entry.latencyWindow) >= 3 && entry.baselineLatency > 0 && latency > 2*entry.baselineLatency {
		hc.logger.Debug("probe indicates degraded performance",
			"upstream_id", entry.upstreamID,
			"latency", latency,
			"baseline", entry.baselineLatency)
		return HealthDegraded
	}

	return HealthOK
}

// buildProbeRequest constructs the appropriate HTTP request for the upstream's provider.
func (hc *HealthChecker) buildProbeRequest(entry *healthEntry) (*http.Request, error) {
	switch entry.provider {
	case "anthropic":
		return hc.buildAnthropicProbe(entry)
	case "openai":
		return hc.buildOpenAIProbe(entry)
	case "gemini":
		return hc.buildGeminiProbe(entry)
	case "claudecode":
		return hc.buildClaudeCodeProbe(entry)
	case "bedrock":
		return hc.buildBedrockProbe(entry)
	case "vertex-anthropic":
		return hc.buildVertexProbe(entry)
	case "vertex-google":
		return hc.buildVertexGoogleProbe(entry)
	case "vertex-openai":
		return hc.buildVertexOpenAIProbe(entry)
	default:
		return hc.buildOpenAIProbe(entry)
	}
}

func (hc *HealthChecker) buildAnthropicProbe(entry *healthEntry) (*http.Request, error) {
	body := map[string]interface{}{
		"model":      entry.testModel,
		"max_tokens": 1,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal probe body: %w", err)
	}

	url := entry.baseURL + "/v1/messages"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", entry.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	return req, nil
}

func (hc *HealthChecker) buildOpenAIProbe(entry *healthEntry) (*http.Request, error) {
	body := map[string]interface{}{
		"model":      entry.testModel,
		"max_tokens": 1,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal probe body: %w", err)
	}

	url := entry.baseURL + "/v1/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+entry.apiKey)
	return req, nil
}

func (hc *HealthChecker) buildGeminiProbe(entry *healthEntry) (*http.Request, error) {
	body := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{{"text": "hi"}},
				"role":  "user",
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 1,
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal probe body: %w", err)
	}

	base := strings.TrimRight(entry.baseURL, "/")
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", base, entry.testModel)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", entry.apiKey)
	return req, nil
}

func (hc *HealthChecker) buildClaudeCodeProbe(entry *healthEntry) (*http.Request, error) {
	body := map[string]interface{}{
		"model":      entry.testModel,
		"max_tokens": 1,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal probe body: %w", err)
	}

	url := entry.baseURL + "/v1/messages"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+entry.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	return req, nil
}

func (hc *HealthChecker) buildBedrockProbe(entry *healthEntry) (*http.Request, error) {
	// Simplified Bedrock probe -- sends a basic POST to the model invoke endpoint.
	// AWS SigV4 signing is NOT handled here; that will be done by ProviderTransformer in Phase 5.
	// This probe will likely fail auth but validates network connectivity.
	body := map[string]interface{}{
		"inputText": "hi",
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal probe body: %w", err)
	}

	url := entry.baseURL + "/model/" + entry.testModel + "/invoke"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (hc *HealthChecker) buildVertexProbe(entry *healthEntry) (*http.Request, error) {
	body := map[string]interface{}{
		"anthropic_version": "vertex-2023-10-16",
		"max_tokens":        1,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal probe body: %w", err)
	}

	// Construct the rawPredict URL: baseURL/{testModel}:rawPredict
	base := entry.baseURL
	if len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	url := fmt.Sprintf("%s/%s:rawPredict", base, entry.testModel)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Get access token via the token fetcher callback.
	if hc.tokenFetcher != nil {
		token, err := hc.tokenFetcher(entry.upstreamID)
		if err != nil {
			return nil, fmt.Errorf("get vertex token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
}

func (hc *HealthChecker) buildVertexGoogleProbe(entry *healthEntry) (*http.Request, error) {
	body := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{{"text": "hi"}},
				"role":  "user",
			},
		},
		"generationConfig": map[string]interface{}{
			"maxOutputTokens": 1,
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal probe body: %w", err)
	}

	base := entry.baseURL
	if len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	url := fmt.Sprintf("%s/%s:generateContent", base, entry.testModel)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Get access token via the token fetcher callback (shared with vertex).
	if hc.tokenFetcher != nil {
		token, err := hc.tokenFetcher(entry.upstreamID)
		if err != nil {
			return nil, fmt.Errorf("get vertex google token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
}

func (hc *HealthChecker) buildVertexOpenAIProbe(entry *healthEntry) (*http.Request, error) {
	body := map[string]interface{}{
		"model":      entry.testModel,
		"max_tokens": 1,
		"messages": []map[string]string{
			{"role": "user", "content": "hi"},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal probe body: %w", err)
	}

	base := strings.TrimRight(entry.baseURL, "/")
	url := base + "/chat/completions"

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if hc.tokenFetcher != nil {
		token, err := hc.tokenFetcher(entry.upstreamID)
		if err != nil {
			return nil, fmt.Errorf("get vertex openai token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
}

// OnProbeResult feeds probe results into circuit breaker and metrics,
// and updates the health entry state.
func (hc *HealthChecker) OnProbeResult(upstreamID string, status HealthStatus) {
	hc.mu.Lock()
	entry, ok := hc.upstreams[upstreamID]
	if ok {
		entry.lastCheck = time.Now()
		entry.lastStatus = status

		switch status {
		case HealthOK, HealthDegraded:
			entry.consecutiveOK++
			entry.consecutiveFail = 0
		case HealthDown:
			entry.consecutiveFail++
			entry.consecutiveOK = 0
		}
	}
	hc.mu.Unlock()

	// Feed into circuit breaker
	switch status {
	case HealthOK:
		hc.circuitBreaker.RecordSuccess(upstreamID)
		if hc.metrics != nil {
			hc.metrics.RecordSuccess(upstreamID)
		}
	case HealthDegraded:
		// Degraded counts as success for CB (upstream is responding) but we don't
		// record it as a full success in metrics -- it's a warning state.
		hc.circuitBreaker.RecordSuccess(upstreamID)
	case HealthDown:
		hc.circuitBreaker.RecordFailure(upstreamID)
		if hc.metrics != nil {
			hc.metrics.RecordError(upstreamID)
		}
	}

	hc.logger.Debug("probe result",
		"upstream_id", upstreamID,
		"status", status.String(),
		"circuit_state", hc.circuitBreaker.State(upstreamID).String())
}

// Status returns the current health state of an upstream.
// Returns HealthUnknown for unregistered upstreams.
func (hc *HealthChecker) Status(upstreamID string) HealthStatus {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	entry, ok := hc.upstreams[upstreamID]
	if !ok {
		return HealthUnknown
	}
	return entry.lastStatus
}

// averageDuration computes the mean of a slice of durations.
func averageDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range ds {
		total += d
	}
	return total / time.Duration(len(ds))
}
