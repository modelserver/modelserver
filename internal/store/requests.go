package store

import (
	"context"
	"fmt"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// CreateRequest inserts a new request log.
// ChannelID and UpstreamID may be empty at creation time (set later via CompleteRequest).
func (s *Store) CreateRequest(r *types.Request) error {
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO requests (project_id, api_key_id, channel_id, upstream_id, trace_id, msg_id, provider, model, streaming,
			status, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			credits_consumed, latency_ms, ttft_ms, error_message, client_ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		RETURNING id, created_at`,
		r.ProjectID, r.APIKeyID, nullString(r.ChannelID), nullString(r.UpstreamID),
		nullString(r.TraceID), nullString(r.MsgID),
		nullString(r.Provider), r.Model, r.Streaming, r.Status,
		r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens,
		r.CreditsConsumed, r.LatencyMs, r.TTFTMs, nullString(r.ErrorMessage), r.ClientIP,
	).Scan(&r.ID, &r.CreatedAt)
}

// CompleteRequest updates a pending request row with final data, including
// the upstream/provider/routing fields that are unknown at creation time.
func (s *Store) CompleteRequest(id string, r *types.Request) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE requests
		SET status = $1, msg_id = $2, input_tokens = $3, output_tokens = $4,
			cache_creation_tokens = $5, cache_read_tokens = $6, credits_consumed = $7,
			latency_ms = $8, ttft_ms = $9, error_message = $10, client_ip = $11,
			upstream_id = COALESCE($12, upstream_id),
			channel_id = COALESCE($12, channel_id),
			provider = COALESCE(NULLIF($13, ''), provider),
			route_id = $14,
			upstream_group_id = $15,
			attempt = $16,
			retry_reason = $17,
			selection_ms = $18
		WHERE id = $19`,
		r.Status, nullString(r.MsgID), r.InputTokens, r.OutputTokens,
		r.CacheCreationTokens, r.CacheReadTokens, r.CreditsConsumed,
		r.LatencyMs, r.TTFTMs, nullString(r.ErrorMessage), r.ClientIP,
		nullString(r.UpstreamID), r.Provider,
		nullString(r.RouteID), nullString(r.GroupID),
		r.Attempt, nullString(r.RetryReason), r.SelectionMs,
		id,
	)
	return err
}

// BatchCreateRequests inserts multiple request logs efficiently.
func (s *Store) BatchCreateRequests(requests []types.Request) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	for i := range requests {
		r := &requests[i]
		err := tx.QueryRow(ctx, `
			INSERT INTO requests (project_id, api_key_id, channel_id, upstream_id, trace_id, msg_id, provider, model, streaming,
				status, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
				credits_consumed, latency_ms, ttft_ms, error_message, client_ip)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
			RETURNING id, created_at`,
			r.ProjectID, r.APIKeyID, nullString(r.ChannelID), nullString(r.UpstreamID),
			nullString(r.TraceID), nullString(r.MsgID),
			nullString(r.Provider), r.Model, r.Streaming, r.Status,
			r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens,
			r.CreditsConsumed, r.LatencyMs, r.TTFTMs, nullString(r.ErrorMessage), r.ClientIP,
		).Scan(&r.ID, &r.CreatedAt)
		if err != nil {
			tx.Rollback(ctx)
			return err
		}
	}
	return tx.Commit(ctx)
}

// ListRequests returns request logs for a project with pagination and filters.
func (s *Store) ListRequests(projectID string, p types.PaginationParams, filters RequestFilters) ([]types.Request, int, error) {
	ctx := context.Background()
	where, args, argN := buildRequestFilters(projectID, filters)

	var total int
	if err := s.pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM requests %s", where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, p.Limit(), p.Offset())
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, project_id, api_key_id, channel_id, COALESCE(trace_id::text, ''), COALESCE(msg_id, ''),
			provider, model, streaming, status, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			credits_consumed, latency_ms, ttft_ms, COALESCE(error_message, ''), client_ip, created_at
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
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.ChannelID, &r.TraceID, &r.MsgID,
			&r.Provider, &r.Model, &r.Streaming, &r.Status,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
			&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		requests = append(requests, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
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

// ListAllRequests returns request logs across all projects with pagination and filters (admin).
func (s *Store) ListAllRequests(p types.PaginationParams, filters RequestFilters) ([]types.Request, int, error) {
	ctx := context.Background()
	where, args, argN := buildGlobalRequestFilters(filters)

	var total int
	if err := s.pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM requests %s", where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, p.Limit(), p.Offset())
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, project_id, api_key_id, channel_id, COALESCE(trace_id::text, ''), COALESCE(msg_id, ''),
			provider, model, streaming, status, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			credits_consumed, latency_ms, ttft_ms, COALESCE(error_message, ''), client_ip, created_at
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
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.ChannelID, &r.TraceID, &r.MsgID,
			&r.Provider, &r.Model, &r.Streaming, &r.Status,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
			&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt); err != nil {
			return nil, 0, err
		}
		requests = append(requests, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return requests, total, nil
}

func buildGlobalRequestFilters(f RequestFilters) (string, []interface{}, int) {
	var conditions []string
	var args []interface{}
	n := 1

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

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + joinStrings(conditions, " AND ")
	}
	return where, args, n
}

// ListRequestsByTraceID returns all request logs associated with a trace ID.
func (s *Store) ListRequestsByTraceID(traceID string) ([]types.Request, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, project_id, api_key_id, channel_id, COALESCE(trace_id::text, ''), COALESCE(msg_id, ''),
			provider, model, streaming, status, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			credits_consumed, latency_ms, ttft_ms, COALESCE(error_message, ''), client_ip, created_at
		FROM requests WHERE trace_id = $1 ORDER BY created_at ASC`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []types.Request
	for rows.Next() {
		var r types.Request
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.ChannelID, &r.TraceID, &r.MsgID,
			&r.Provider, &r.Model, &r.Streaming, &r.Status,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
			&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt); err != nil {
			return nil, err
		}
		requests = append(requests, r)
	}
	return requests, rows.Err()
}
