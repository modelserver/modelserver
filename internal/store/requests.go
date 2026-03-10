package store

import (
	"fmt"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// CreateRequest inserts a new request log.
func (s *Store) CreateRequest(r *types.Request) error {
	return s.db.QueryRow(`
		INSERT INTO requests (project_id, api_key_id, channel_id, trace_id, provider, model, streaming,
			status, status_code, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			credits_consumed, latency_ms, ttft_ms, error_message)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id, created_at`,
		r.ProjectID, r.APIKeyID, r.ChannelID, nullString(r.TraceID),
		r.Provider, r.Model, r.Streaming, r.Status, r.StatusCode,
		r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens,
		r.CreditsConsumed, r.LatencyMs, r.TTFTMs, nullString(r.ErrorMessage),
	).Scan(&r.ID, &r.CreatedAt)
}

// BatchCreateRequests inserts multiple request logs efficiently.
func (s *Store) BatchCreateRequests(requests []types.Request) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for i := range requests {
		r := &requests[i]
		err := tx.QueryRow(`
			INSERT INTO requests (project_id, api_key_id, channel_id, trace_id, provider, model, streaming,
				status, status_code, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
				credits_consumed, latency_ms, ttft_ms, error_message)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			RETURNING id, created_at`,
			r.ProjectID, r.APIKeyID, r.ChannelID, nullString(r.TraceID),
			r.Provider, r.Model, r.Streaming, r.Status, r.StatusCode,
			r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens,
			r.CreditsConsumed, r.LatencyMs, r.TTFTMs, nullString(r.ErrorMessage),
		).Scan(&r.ID, &r.CreatedAt)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ListRequests returns request logs for a project with pagination and filters.
func (s *Store) ListRequests(projectID string, p types.PaginationParams, filters RequestFilters) ([]types.Request, int, error) {
	where, args, argN := buildRequestFilters(projectID, filters)

	var total int
	if err := s.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM requests %s", where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, p.Limit(), p.Offset())
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT id, project_id, api_key_id, channel_id, COALESCE(trace_id::text, ''), provider, model,
			streaming, status, status_code, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			credits_consumed, latency_ms, ttft_ms, COALESCE(error_message, ''), created_at
		FROM requests %s ORDER BY %s %s LIMIT $%d OFFSET $%d`,
		where, sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order), argN, argN+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var requests []types.Request
	for rows.Next() {
		var r types.Request
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.ChannelID, &r.TraceID,
			&r.Provider, &r.Model, &r.Streaming, &r.Status, &r.StatusCode,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
			&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		requests = append(requests, r)
	}
	return requests, total, nil
}

// RequestFilters holds optional filters for listing requests.
type RequestFilters struct {
	Model    string
	Status   string
	APIKeyID string
	Since    time.Time
	Until    time.Time
}

func buildRequestFilters(projectID string, f RequestFilters) (string, []interface{}, int) {
	conditions := []string{"project_id = $1"}
	args := []interface{}{projectID}
	n := 2

	if f.Model != "" {
		conditions = append(conditions, fmt.Sprintf("model = $%d", n))
		args = append(args, f.Model)
		n++
	}
	if f.Status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", n))
		args = append(args, f.Status)
		n++
	}
	if f.APIKeyID != "" {
		conditions = append(conditions, fmt.Sprintf("api_key_id = $%d", n))
		args = append(args, f.APIKeyID)
		n++
	}
	if !f.Since.IsZero() {
		conditions = append(conditions, fmt.Sprintf("created_at >= $%d", n))
		args = append(args, f.Since)
		n++
	}
	if !f.Until.IsZero() {
		conditions = append(conditions, fmt.Sprintf("created_at <= $%d", n))
		args = append(args, f.Until)
		n++
	}

	where := "WHERE " + joinStrings(conditions, " AND ")
	return where, args, n
}
