package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

// CompositeRateLimiter combines credit-based and classic rate limiters.
type CompositeRateLimiter struct {
	classic *ClassicLimiter
	store   *store.Store
	cache   *creditCache
	logger  *slog.Logger
}

// NewCompositeRateLimiter creates a composite rate limiter.
func NewCompositeRateLimiter(st *store.Store, logger *slog.Logger) *CompositeRateLimiter {
	return &CompositeRateLimiter{
		classic: NewClassicLimiter(),
		store:   st,
		cache:   newCreditCache(10 * time.Second),
		logger:  logger,
	}
}

// PreCheck validates all rate limit rules for the API key.
func (c *CompositeRateLimiter) PreCheck(ctx context.Context, projectID, apiKeyID, model string, policy *types.RateLimitPolicy) (bool, time.Duration, error) {
	if policy == nil {
		return true, 0, nil
	}

	// Check credit rules.
	for _, rule := range policy.CreditRules {
		windowStart := windowStartTime(rule.Window, rule.WindowType)
		cacheKey := fmt.Sprintf("%s|%s|%s", projectID, apiKeyID, rule.Window)
		if rule.EffectiveScope() == types.CreditScopeProject {
			cacheKey = fmt.Sprintf("p:%s|%s", projectID, rule.Window)
		}

		var used float64
		if cached, ok := c.cache.get(cacheKey); ok {
			used = cached
		} else {
			var err error
			if rule.EffectiveScope() == types.CreditScopeProject {
				used, err = c.store.SumCreditsInWindowByProject(projectID, windowStart)
			} else {
				used, err = c.store.SumCreditsInWindow(apiKeyID, windowStart)
			}
			if err != nil {
				c.logger.Error("credit check query failed", "error", err)
				return true, 0, nil // Fail open.
			}
			c.cache.set(cacheKey, used)
		}

		if used >= float64(rule.MaxCredits) {
			retryAfter := windowResetDuration(rule.Window, rule.WindowType)
			return false, retryAfter, nil
		}
	}

	// Check classic rules.
	if len(policy.ClassicRules) > 0 {
		allowed, retryAfter := c.classic.Check(apiKeyID, model, policy.ClassicRules)
		if !allowed {
			return false, retryAfter, nil
		}
	}

	return true, 0, nil
}

// PostRecord records actual usage and invalidates caches.
func (c *CompositeRateLimiter) PostRecord(ctx context.Context, projectID, apiKeyID, model string, usage types.TokenUsage) {
	c.classic.Record(apiKeyID, model, usage)
	c.cache.invalidatePrefix(apiKeyID)
	c.cache.invalidatePrefix("p:" + projectID)
}

// windowStartTime returns the start of the current window.
func windowStartTime(window, windowType string) time.Time {
	now := time.Now().UTC()

	if windowType == "calendar" {
		switch window {
		case "1w":
			weekday := now.Weekday()
			if weekday == 0 {
				weekday = 7
			}
			start := now.AddDate(0, 0, -int(weekday-1))
			return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		case "1M":
			return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		}
	}

	d := parseDurationStr(window)
	return now.Add(-d)
}

// windowResetDuration returns how long until the window resets.
func windowResetDuration(window, windowType string) time.Duration {
	now := time.Now().UTC()

	if windowType == "calendar" {
		switch window {
		case "1w":
			weekday := now.Weekday()
			if weekday == 0 {
				weekday = 7
			}
			daysUntilMonday := 8 - int(weekday)
			nextMonday := time.Date(now.Year(), now.Month(), now.Day()+daysUntilMonday, 0, 0, 0, 0, time.UTC)
			return nextMonday.Sub(now)
		case "1M":
			nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			return nextMonth.Sub(now)
		}
	}

	return parseDurationStr(window)
}

func parseDurationStr(s string) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	if len(s) > 1 && s[len(s)-1] == 'd' {
		if d, err := time.ParseDuration(s[:len(s)-1] + "h"); err == nil {
			return d * 24
		}
	}
	return time.Hour
}
