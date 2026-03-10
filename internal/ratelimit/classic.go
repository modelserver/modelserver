package ratelimit

import (
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// ClassicLimiter enforces RPM/TPM/RPD/TPD rate limits using in-memory counters.
type ClassicLimiter struct {
	counters *MemoryCounters
}

// NewClassicLimiter creates a new classic rate limiter.
func NewClassicLimiter() *ClassicLimiter {
	cl := &ClassicLimiter{
		counters: NewMemoryCounters(),
	}
	cl.startCleanup()
	return cl
}

// Check verifies all classic rules pass.
func (cl *ClassicLimiter) Check(apiKeyID, model string, rules []types.ClassicRule) (bool, time.Duration) {
	for _, rule := range rules {
		window := metricWindow(rule.Metric)
		key := counterKey(apiKeyID, rule.Metric, model, rule.PerModel)

		var current int64
		if isRequestMetric(rule.Metric) {
			current = cl.counters.CountRequests(key, window)
		} else if isTokenMetric(rule.Metric) {
			current = cl.counters.SumTokens(key, window)
		}

		if current >= rule.Limit {
			return false, window
		}
	}
	return true, 0
}

// Record updates counters after a request completes.
func (cl *ClassicLimiter) Record(apiKeyID, model string, usage types.TokenUsage) {
	totalTokens := usage.InputTokens + usage.OutputTokens

	for _, metric := range []string{"rpm", "rpd"} {
		cl.counters.AddRequest(counterKey(apiKeyID, metric, model, false))
		cl.counters.AddRequest(counterKey(apiKeyID, metric, model, true))
	}

	for _, metric := range []string{"tpm", "tpd"} {
		cl.counters.AddTokens(counterKey(apiKeyID, metric, model, false), totalTokens)
		cl.counters.AddTokens(counterKey(apiKeyID, metric, model, true), totalTokens)
	}
}

func (cl *ClassicLimiter) startCleanup() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		for range ticker.C {
			cl.counters.Cleanup(24 * time.Hour)
		}
	}()
}

func counterKey(apiKeyID, metric, model string, perModel bool) string {
	if perModel {
		return apiKeyID + ":" + metric + ":" + model
	}
	return apiKeyID + ":" + metric
}

func metricWindow(metric string) time.Duration {
	switch metric {
	case "rpm", "tpm":
		return time.Minute
	case "rpd", "tpd":
		return 24 * time.Hour
	default:
		return time.Minute
	}
}

func isRequestMetric(m string) bool { return m == "rpm" || m == "rpd" }
func isTokenMetric(m string) bool   { return m == "tpm" || m == "tpd" }
