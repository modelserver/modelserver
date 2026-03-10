package store

import (
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
	TotalCredits       float64 `json:"total_credits"`
	AvgLatencyMs       float64 `json:"avg_latency_ms"`
}

// DailyUsage holds usage data for a single day.
type DailyUsage struct {
	Date         string  `json:"date"`
	RequestCount int64   `json:"request_count"`
	TotalTokens  int64   `json:"total_tokens"`
	TotalCredits float64 `json:"total_credits"`
}

// GetUsageByModel returns usage aggregated by model for a project.
func (s *Store) GetUsageByModel(projectID string, since, until time.Time) ([]UsageSummary, error) {
	rows, err := s.db.Query(`
		SELECT model, COUNT(*) as request_count,
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cache_creation_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
			COALESCE(SUM(credits_consumed), 0), COALESCE(AVG(latency_ms), 0)
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
			&u.TotalCredits, &u.AvgLatencyMs); err != nil {
			return nil, err
		}
		summaries = append(summaries, u)
	}
	return summaries, nil
}

// GetUsageByAPIKey returns usage aggregated by API key.
func (s *Store) GetUsageByAPIKey(projectID string, since, until time.Time) ([]map[string]interface{}, error) {
	rows, err := s.db.Query(`
		SELECT r.api_key_id, k.name, k.key_prefix, COUNT(*) as request_count,
			COALESCE(SUM(r.input_tokens + r.output_tokens), 0) as total_tokens,
			COALESCE(SUM(r.credits_consumed), 0) as total_credits
		FROM requests r
		JOIN api_keys k ON r.api_key_id = k.id
		WHERE r.project_id = $1 AND r.created_at >= $2 AND r.created_at <= $3
		GROUP BY r.api_key_id, k.name, k.key_prefix
		ORDER BY total_credits DESC`,
		projectID, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var apiKeyID, name, prefix string
		var count, tokens int64
		var credits float64
		if err := rows.Scan(&apiKeyID, &name, &prefix, &count, &tokens, &credits); err != nil {
			return nil, err
		}
		results = append(results, map[string]interface{}{
			"api_key_id":    apiKeyID,
			"api_key_name":  name,
			"key_prefix":    prefix,
			"request_count": count,
			"total_tokens":  tokens,
			"total_credits": credits,
		})
	}
	return results, nil
}

// GetDailyUsage returns daily usage breakdown.
func (s *Store) GetDailyUsage(projectID string, since, until time.Time) ([]DailyUsage, error) {
	rows, err := s.db.Query(`
		SELECT DATE(created_at) as date, COUNT(*) as request_count,
			COALESCE(SUM(input_tokens + output_tokens), 0) as total_tokens,
			COALESCE(SUM(credits_consumed), 0) as total_credits
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
		if err := rows.Scan(&d.Date, &d.RequestCount, &d.TotalTokens, &d.TotalCredits); err != nil {
			return nil, err
		}
		daily = append(daily, d)
	}
	return daily, nil
}

// SumCreditsInWindow returns total credits consumed by an API key within a time window.
func (s *Store) SumCreditsInWindow(apiKeyID string, windowStart time.Time) (float64, error) {
	var total float64
	err := s.db.QueryRow(`
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
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM requests
		WHERE api_key_id = $1 AND created_at >= $2`,
		apiKeyID, windowStart,
	).Scan(&count)
	return count, err
}

// SumTokensInWindow returns total tokens (input+output) by an API key within a time window.
func (s *Store) SumTokensInWindow(apiKeyID string, windowStart time.Time) (int64, error) {
	var total int64
	err := s.db.QueryRow(`
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
	err := s.db.QueryRow(`
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
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM requests
		WHERE api_key_id = $1 AND model = $2 AND created_at >= $3`,
		apiKeyID, model, windowStart,
	).Scan(&count)
	return count, err
}

// GetUsageOverview returns a high-level usage overview for a project.
func (s *Store) GetUsageOverview(projectID string, since, until time.Time) (map[string]interface{}, error) {
	var requestCount int64
	var totalTokens int64
	var totalCredits float64
	err := s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(input_tokens + output_tokens), 0), COALESCE(SUM(credits_consumed), 0)
		FROM requests WHERE project_id = $1 AND created_at >= $2 AND created_at <= $3`,
		projectID, since, until,
	).Scan(&requestCount, &totalTokens, &totalCredits)
	if err != nil {
		return nil, fmt.Errorf("usage overview: %w", err)
	}

	return map[string]interface{}{
		"request_count": requestCount,
		"total_tokens":  totalTokens,
		"total_credits": totalCredits,
		"since":         since.Format(time.RFC3339),
		"until":         until.Format(time.RFC3339),
	}, nil
}
