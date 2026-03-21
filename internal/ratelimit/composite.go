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
		cache:   NewCreditCache(10 * time.Second),
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
		windowStart := WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)
		cacheKey := fmt.Sprintf("%s|%s|%s|%s", projectID, apiKeyID, rule.Window, rule.WindowType)
		if rule.EffectiveScope() == types.CreditScopeProject {
			cacheKey = fmt.Sprintf("p:%s|%s|%s", projectID, rule.Window, rule.WindowType)
		}

		var used float64
		if cached, ok := c.cache.Get(cacheKey); ok {
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
			c.cache.Set(cacheKey, used)
		}

		if used >= float64(rule.MaxCredits) {
			retryAfter := WindowResetDuration(rule.Window, rule.WindowType, rule.AnchorTime)
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

// CheckUserQuota validates per-user credit quota against project-scope rules.
func (c *CompositeRateLimiter) CheckUserQuota(ctx context.Context, projectID, userID string, quotaPct float64, policy *types.RateLimitPolicy) (bool, time.Duration, error) {
	if policy == nil {
		return true, 0, nil
	}
	for _, rule := range policy.CreditRules {
		if rule.EffectiveScope() != types.CreditScopeProject {
			continue
		}
		windowStart := WindowStartTime(rule.Window, rule.WindowType, rule.AnchorTime)
		userLimit := float64(rule.MaxCredits) * (quotaPct / 100.0)

		cacheKey := fmt.Sprintf("u:%s:%s|%s|%s", projectID, userID, rule.Window, rule.WindowType)
		var used float64
		if cached, ok := c.cache.Get(cacheKey); ok {
			used = cached
		} else {
			var err error
			used, err = c.store.SumCreditsInWindowByUser(projectID, userID, windowStart)
			if err != nil {
				c.logger.Error("user quota check query failed", "error", err)
				return true, 0, nil // Fail open.
			}
			c.cache.Set(cacheKey, used)
		}

		if used >= userLimit {
			retryAfter := WindowResetDuration(rule.Window, rule.WindowType, rule.AnchorTime)
			return false, retryAfter, nil
		}
	}
	return true, 0, nil
}

// PostRecord records actual usage and invalidates caches.
func (c *CompositeRateLimiter) PostRecord(ctx context.Context, projectID, apiKeyID, userID, model string, usage types.TokenUsage) {
	c.classic.Record(apiKeyID, model, usage)
	c.cache.invalidatePrefix(apiKeyID)
	c.cache.invalidatePrefix("p:" + projectID)
	if userID != "" {
		c.cache.invalidatePrefix("u:" + projectID + ":" + userID)
	}
}

// WindowStartTimeAt is the testable core that accepts an explicit now.
func WindowStartTimeAt(now time.Time, window, windowType string, anchorTime *time.Time) time.Time {
	if windowType == types.WindowTypeCalendar {
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

	if windowType == types.WindowTypeFixed {
		if anchorTime == nil {
			d := ParseDurationStr(window)
			return now.Add(-d)
		}
		anchor := anchorTime.UTC()
		d := ParseDurationStr(window)
		elapsed := now.Sub(anchor)
		if elapsed < 0 {
			return anchor
		}
		windowIndex := int64(elapsed / d)
		return anchor.Add(time.Duration(windowIndex) * d)
	}

	// Default: sliding window.
	d := ParseDurationStr(window)
	return now.Add(-d)
}

// WindowStartTime returns the start of the current window.
func WindowStartTime(window, windowType string, anchorTime *time.Time) time.Time {
	return WindowStartTimeAt(time.Now().UTC(), window, windowType, anchorTime)
}

// WindowResetDurationAt is the testable core that accepts an explicit now.
func WindowResetDurationAt(now time.Time, window, windowType string, anchorTime *time.Time) time.Duration {
	if windowType == types.WindowTypeCalendar {
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

	if windowType == types.WindowTypeFixed {
		windowStart := WindowStartTimeAt(now, window, windowType, anchorTime)
		d := ParseDurationStr(window)
		windowEnd := windowStart.Add(d)
		return windowEnd.Sub(now)
	}

	// Default: sliding window.
	return ParseDurationStr(window)
}

// WindowResetDuration returns how long until the window resets.
func WindowResetDuration(window, windowType string, anchorTime *time.Time) time.Duration {
	return WindowResetDurationAt(time.Now().UTC(), window, windowType, anchorTime)
}

// ParseDurationStr parses a duration string with support for day (d) and week (w) suffixes.
func ParseDurationStr(s string) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	if len(s) > 1 {
		suffix := s[len(s)-1]
		numPart := s[:len(s)-1]
		switch suffix {
		case 'd':
			if d, err := time.ParseDuration(numPart + "h"); err == nil {
				return d * 24
			}
		case 'w':
			if d, err := time.ParseDuration(numPart + "h"); err == nil {
				return d * 24 * 7
			}
		}
	}
	return time.Hour
}
