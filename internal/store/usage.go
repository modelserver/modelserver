package store

import (
	"context"
	"fmt"
	"time"
)

// UsageSummary holds aggregated usage data.
type UsageSummary struct {
	Model              string  `json:"model"`
	RequestCount       int64   `json:"request_count"`
	TotalInputTokens   int64   `json:"total_input_tokens"`
	TotalOutputTokens  int64   `json:"total_output_tokens"`
	TotalCacheCreation int64   `json:"total_cache_creation_tokens"`
	TotalCacheRead     int64   `json:"total_cache_read_tokens"`
	AvgLatencyMs       float64 `json:"avg_latency_ms"`
}

// DailyUsage holds usage data for a single day.
type DailyUsage struct {
	Date         time.Time `json:"date"`
	RequestCount int64     `json:"request_count"`
	TotalTokens  int64     `json:"total_tokens"`
}

// UpstreamUsageSummary holds aggregated usage data per upstream.
type UpstreamUsageSummary struct {
	UpstreamID   string  `json:"upstream_id"`
	RequestCount int64   `json:"request_count"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalCredits float64 `json:"total_credits"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	SuccessCount int64   `json:"success_count"`
	ErrorCount   int64   `json:"error_count"`
}

// GetUsageByUpstream returns usage aggregated by upstream across all projects.
func (s *Store) GetUsageByUpstream(since, until time.Time) ([]UpstreamUsageSummary, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT upstream_id, COUNT(*) as request_count,
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(credits_consumed), 0), COALESCE(AVG(latency_ms), 0),
			COUNT(*) FILTER (WHERE status = 'success'),
			COUNT(*) FILTER (WHERE status != 'success')
		FROM requests
		WHERE upstream_id IS NOT NULL AND created_at >= $1 AND created_at <= $2
		GROUP BY upstream_id
		ORDER BY request_count DESC`,
		since, until)
	if err != nil {
		return nil, fmt.Errorf("usage by upstream: %w", err)
	}
	defer rows.Close()

	var summaries []UpstreamUsageSummary
	for rows.Next() {
		var u UpstreamUsageSummary
		if err := rows.Scan(&u.UpstreamID, &u.RequestCount,
			&u.InputTokens, &u.OutputTokens,
			&u.TotalCredits, &u.AvgLatencyMs,
			&u.SuccessCount, &u.ErrorCount); err != nil {
			return nil, err
		}
		summaries = append(summaries, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

// GetCreditsByUpstreamSince returns total credits consumed per upstream since a given time.
func (s *Store) GetCreditsByUpstreamSince(since time.Time) (map[string]float64, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT upstream_id, COALESCE(SUM(credits_consumed), 0)
		FROM requests
		WHERE upstream_id IS NOT NULL AND created_at >= $1
		GROUP BY upstream_id`, since)
	if err != nil {
		return nil, fmt.Errorf("credits by upstream: %w", err)
	}
	defer rows.Close()

	m := make(map[string]float64)
	for rows.Next() {
		var id string
		var credits float64
		if err := rows.Scan(&id, &credits); err != nil {
			return nil, err
		}
		m[id] = credits
	}
	return m, rows.Err()
}

// GetUsageByModel returns usage aggregated by model for a project.
func (s *Store) GetUsageByModel(projectID string, since, until time.Time) ([]UsageSummary, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT model, COUNT(*) as request_count,
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(AVG(latency_ms), 0)
		FROM requests
		WHERE project_id = $1 AND created_at >= $2 AND created_at <= $3
		GROUP BY model ORDER BY request_count DESC`,
		projectID, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []UsageSummary
	for rows.Next() {
		var u UsageSummary
		if err := rows.Scan(&u.Model, &u.RequestCount,
			&u.TotalInputTokens, &u.TotalOutputTokens,
			&u.TotalCacheCreation, &u.TotalCacheRead,
			&u.AvgLatencyMs); err != nil {
			return nil, err
		}
		summaries = append(summaries, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

// GetUsageByMember returns usage aggregated by project member (requests.created_by),
// sorted by total tokens descending, with page/offset pagination. Rows whose
// created_by no longer resolves to a user are still included with empty
// nickname/picture/email so totals stay honest. The second return value is the
// total distinct-member count for pagination meta.
//
// The query aggregates first (a bounded set per project) and only joins users
// against the aggregate so the hot GROUP BY can use idx_requests_project_user_created
// cleanly. A secondary ORDER BY on created_by gives deterministic tiebreaking
// so pagination doesn't shuffle rows between pages when totals are equal.
func (s *Store) GetUsageByMember(projectID string, since, until time.Time, limit, offset int) ([]map[string]interface{}, int, error) {
	ctx := context.Background()

	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT created_by)
		FROM requests
		WHERE project_id = $1
			AND created_by IS NOT NULL
			AND created_at >= $2 AND created_at <= $3`,
		projectID, since, until,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("usage by member count: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		WITH agg AS (
			SELECT created_by,
				COUNT(*) AS request_count,
				COALESCE(SUM(input_tokens + output_tokens), 0) AS total_tokens
			FROM requests
			WHERE project_id = $1
				AND created_by IS NOT NULL
				AND created_at >= $2 AND created_at <= $3
			GROUP BY created_by
		)
		SELECT a.created_by,
			COALESCE(u.nickname, ''),
			COALESCE(u.picture, ''),
			COALESCE(u.email, ''),
			a.request_count,
			a.total_tokens
		FROM agg a
		LEFT JOIN users u ON u.id::text = a.created_by
		ORDER BY a.total_tokens DESC, a.created_by
		LIMIT $4 OFFSET $5`,
		projectID, since, until, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("usage by member: %w", err)
	}
	defer rows.Close()

	results := make([]map[string]interface{}, 0)
	for rows.Next() {
		var userID, nickname, picture, email string
		var count, tokens int64
		if err := rows.Scan(&userID, &nickname, &picture, &email, &count, &tokens); err != nil {
			return nil, 0, err
		}
		results = append(results, map[string]interface{}{
			"user_id":       userID,
			"nickname":      nickname,
			"picture":       picture,
			"email":         email,
			"request_count": count,
			"total_tokens":  tokens,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return results, total, nil
}

// GetDailyUsage returns daily usage breakdown.
func (s *Store) GetDailyUsage(projectID string, since, until time.Time) ([]DailyUsage, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT DATE(created_at) as date, COUNT(*) as request_count,
			COALESCE(SUM(input_tokens + output_tokens), 0) as total_tokens
		FROM requests
		WHERE project_id = $1 AND created_at >= $2 AND created_at <= $3
		GROUP BY DATE(created_at) ORDER BY date ASC`,
		projectID, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var daily []DailyUsage
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Date, &d.RequestCount, &d.TotalTokens); err != nil {
			return nil, err
		}
		daily = append(daily, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return daily, nil
}

// SumCreditsInWindow returns total credits consumed by an API key within a time window.
func (s *Store) SumCreditsInWindow(apiKeyID string, windowStart time.Time) (float64, error) {
	var total float64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(credits_consumed), 0)
		FROM requests
		WHERE api_key_id = $1 AND created_at >= $2`,
		apiKeyID, windowStart,
	).Scan(&total)
	return total, err
}

// CountRequestsInWindow returns the number of requests by an API key within a time window.
func (s *Store) CountRequestsInWindow(apiKeyID string, windowStart time.Time) (int64, error) {
	var count int64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM requests
		WHERE api_key_id = $1 AND created_at >= $2`,
		apiKeyID, windowStart,
	).Scan(&count)
	return count, err
}

// SumTokensInWindow returns total tokens (input+output) by an API key within a time window.
func (s *Store) SumTokensInWindow(apiKeyID string, windowStart time.Time) (int64, error) {
	var total int64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(input_tokens + output_tokens), 0)
		FROM requests
		WHERE api_key_id = $1 AND created_at >= $2`,
		apiKeyID, windowStart,
	).Scan(&total)
	return total, err
}

// SumTokensInWindowForModel returns total tokens for a specific model.
func (s *Store) SumTokensInWindowForModel(apiKeyID, model string, windowStart time.Time) (int64, error) {
	var total int64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(input_tokens + output_tokens), 0)
		FROM requests
		WHERE api_key_id = $1 AND model = $2 AND created_at >= $3`,
		apiKeyID, model, windowStart,
	).Scan(&total)
	return total, err
}

// CountRequestsInWindowForModel returns request count for a specific model.
func (s *Store) CountRequestsInWindowForModel(apiKeyID, model string, windowStart time.Time) (int64, error) {
	var count int64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM requests
		WHERE api_key_id = $1 AND model = $2 AND created_at >= $3`,
		apiKeyID, model, windowStart,
	).Scan(&count)
	return count, err
}

// GetTokenBreakdownByUpstreamAndModelSince returns per-model token breakdowns for a specific upstream since a given time.
func (s *Store) GetTokenBreakdownByUpstreamAndModelSince(upstreamID string, since time.Time) (map[string]*UpstreamTokenBreakdown, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT model,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0),
			COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(credits_consumed), 0),
			COUNT(*)
		FROM requests
		WHERE upstream_id = $1 AND created_at >= $2
		GROUP BY model`, upstreamID, since)
	if err != nil {
		return nil, fmt.Errorf("token breakdown by model: %w", err)
	}
	defer rows.Close()

	m := make(map[string]*UpstreamTokenBreakdown)
	for rows.Next() {
		var model string
		var b UpstreamTokenBreakdown
		if err := rows.Scan(&model, &b.InputTokens, &b.OutputTokens, &b.CacheCreationTokens, &b.CacheReadTokens, &b.CreditsConsumed, &b.RequestCount); err != nil {
			return nil, err
		}
		m[model] = &b
	}
	return m, rows.Err()
}

// GetCreditsByUpstreamIDSince returns total credits consumed by a specific upstream since a given time.
func (s *Store) GetCreditsByUpstreamIDSince(upstreamID string, since time.Time) (float64, error) {
	var total float64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(credits_consumed), 0)
		FROM requests
		WHERE upstream_id = $1 AND created_at >= $2`,
		upstreamID, since,
	).Scan(&total)
	return total, err
}

// UpstreamTokenBreakdown holds per-token-type sums for a specific upstream within a time window.
type UpstreamTokenBreakdown struct {
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CreditsConsumed     float64 `json:"credits_consumed"`
	RequestCount        int64   `json:"request_count"`
}

// GetTokenBreakdownByUpstreamSince returns per-token-type sums for a specific upstream since a given time.
func (s *Store) GetTokenBreakdownByUpstreamSince(upstreamID string, since time.Time) (*UpstreamTokenBreakdown, error) {
	var b UpstreamTokenBreakdown
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(input_tokens), 0),
			   COALESCE(SUM(output_tokens), 0),
			   COALESCE(SUM(cache_creation_tokens), 0),
			   COALESCE(SUM(cache_read_tokens), 0),
			   COALESCE(SUM(credits_consumed), 0),
			   COUNT(*)
		FROM requests
		WHERE upstream_id = $1 AND created_at >= $2`,
		upstreamID, since,
	).Scan(&b.InputTokens, &b.OutputTokens, &b.CacheCreationTokens, &b.CacheReadTokens, &b.CreditsConsumed, &b.RequestCount)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// --- Project-level credit queries (for shared/project-scope rate limiting) ---

// SumCreditsInWindowByProject returns total credits consumed by all keys in a project within a time window.
func (s *Store) SumCreditsInWindowByProject(projectID string, windowStart time.Time) (float64, error) {
	var total float64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(credits_consumed), 0)
		FROM requests
		WHERE project_id = $1 AND created_at >= $2`,
		projectID, windowStart,
	).Scan(&total)
	return total, err
}

// CountRequestsInWindowByProject returns the number of requests by all keys in a project within a time window.
func (s *Store) CountRequestsInWindowByProject(projectID string, windowStart time.Time) (int64, error) {
	var count int64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM requests
		WHERE project_id = $1 AND created_at >= $2`,
		projectID, windowStart,
	).Scan(&count)
	return count, err
}

// SumCreditsInWindowByUser sums credits consumed by a user within a project
// during a time window. Uses the denormalized created_by column on requests.
func (s *Store) SumCreditsInWindowByUser(projectID, userID string, windowStart time.Time) (float64, error) {
	var total float64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(credits_consumed), 0)
		FROM requests
		WHERE project_id = $1 AND created_by = $2 AND created_at >= $3`,
		projectID, userID, windowStart,
	).Scan(&total)
	return total, err
}

// SumTokensInWindowByProject returns total tokens for all keys in a project within a time window.
func (s *Store) SumTokensInWindowByProject(projectID string, windowStart time.Time) (int64, error) {
	var total int64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(input_tokens + output_tokens), 0)
		FROM requests
		WHERE project_id = $1 AND created_at >= $2`,
		projectID, windowStart,
	).Scan(&total)
	return total, err
}

// GetUsageOverview returns a high-level usage overview for a project.
func (s *Store) GetUsageOverview(projectID string, since, until time.Time) (map[string]interface{}, error) {
	var requestCount int64
	var totalTokens int64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(*), COALESCE(SUM(input_tokens + output_tokens), 0)
		FROM requests WHERE project_id = $1 AND created_at >= $2 AND created_at <= $3`,
		projectID, since, until,
	).Scan(&requestCount, &totalTokens)
	if err != nil {
		return nil, fmt.Errorf("usage overview: %w", err)
	}

	return map[string]interface{}{
		"request_count": requestCount,
		"total_tokens":  totalTokens,
		"since":         since.Format(time.RFC3339),
		"until":         until.Format(time.RFC3339),
	}, nil
}
