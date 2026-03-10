package ratelimit

import (
	"context"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// RateLimiter checks and records rate limit usage.
type RateLimiter interface {
	// PreCheck validates whether a request should be allowed.
	// Returns (allowed, retryAfter, error).
	PreCheck(ctx context.Context, projectID, apiKeyID, model string, policy *types.RateLimitPolicy) (bool, time.Duration, error)

	// PostRecord records actual usage after a response completes.
	PostRecord(ctx context.Context, apiKeyID, model string, usage types.TokenUsage)
}

// CreditWindowStatus shows credit usage for a time window.
type CreditWindowStatus struct {
	Window      string  `json:"window"`
	MaxCredits  int64   `json:"max_credits"`
	UsedCredits float64 `json:"used_credits"`
	ResetsAt    string  `json:"resets_at,omitempty"`
}

// ClassicMetricStatus shows classic rate limit status.
type ClassicMetricStatus struct {
	Metric  string `json:"metric"`
	Limit   int64  `json:"limit"`
	Current int64  `json:"current"`
	Model   string `json:"model,omitempty"`
}
