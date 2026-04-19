package ratelimit

import (
	"context"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// PreCheckResult classifies a rate-limit decision. `LimitType` distinguishes
// credit-window exhaustion (extra-usage intent candidate) from classic
// hard-limits (always 429) so the caller can branch on the right action.
type PreCheckResult struct {
	Allowed    bool
	RetryAfter time.Duration
	LimitType  string // "", "credit", or "classic"
	HitRuleID  string // optional audit hint
}

// Classifier values for PreCheckResult.LimitType.
const (
	LimitTypeNone    = ""
	LimitTypeCredit  = "credit"
	LimitTypeClassic = "classic"
)

// RateLimiter checks and records rate limit usage.
type RateLimiter interface {
	// PreCheck validates whether a request should be allowed against the full
	// set of rules (credit + classic). When denied, LimitType tells the caller
	// which family triggered.
	PreCheck(ctx context.Context, projectID, apiKeyID, model string, policy *types.RateLimitPolicy) (PreCheckResult, error)

	// PreCheckClassicOnly checks only classic (RPM/TPM/RPD/TPD) rules,
	// skipping credit windows. Used by the extra-usage bypass path where
	// the credit budget is not consulted but classic protection of upstream
	// providers still applies.
	PreCheckClassicOnly(ctx context.Context, projectID, apiKeyID, model string, policy *types.RateLimitPolicy) (PreCheckResult, error)

	// CheckUserQuota validates per-user credit quota against project-scope rules.
	// quotaPct is in [0, 100]. Only project-scope credit rules are checked.
	// Returns (allowed, retryAfter, error).
	CheckUserQuota(ctx context.Context, projectID, userID string, quotaPct float64, policy *types.RateLimitPolicy) (bool, time.Duration, error)

	// PostRecord records actual usage after a response completes.
	PostRecord(ctx context.Context, projectID, apiKeyID, userID, model string, usage types.TokenUsage)
}

// CreditWindowStatus shows credit usage for a time window.
type CreditWindowStatus struct {
	Window     string  `json:"window"`
	Percentage float64 `json:"percentage"`
	ResetsAt   string  `json:"resets_at,omitempty"`
}

// ClassicMetricStatus shows classic rate limit status.
type ClassicMetricStatus struct {
	Metric  string `json:"metric"`
	Limit   int64  `json:"limit"`
	Current int64  `json:"current"`
	Model   string `json:"model,omitempty"`
}
