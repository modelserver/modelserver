package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// CreateRequest inserts a new request log.
// UpstreamID may be empty at creation time (set later via CompleteRequest).
func (s *Store) CreateRequest(r *types.Request) error {
	metadataJSON := []byte("{}")
	if r.Metadata != nil {
		metadataJSON, _ = json.Marshal(r.Metadata)
	}
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO requests (project_id, api_key_id, oauth_grant_id, upstream_id, trace_id, msg_id, provider, model, streaming,
			status, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			credits_consumed, latency_ms, ttft_ms, error_message, client_ip, created_by, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		RETURNING id, created_at`,
		r.ProjectID, nullString(r.APIKeyID), nullString(r.OAuthGrantID), nullString(r.UpstreamID),
		nullString(r.TraceID), nullString(r.MsgID),
		r.Provider, r.Model, r.Streaming, r.Status,
		r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens,
		r.CreditsConsumed, r.LatencyMs, r.TTFTMs, nullString(r.ErrorMessage), r.ClientIP, nullString(r.CreatedBy),
		metadataJSON,
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
			provider = COALESCE(NULLIF($13, ''), provider),
			route_id = $14,
			upstream_group_id = $15,
			attempt = $16,
			retry_reason = $17,
			selection_ms = $18,
			is_extra_usage = $19,
			extra_usage_cost_fen = $20,
			extra_usage_reason = $21
		WHERE id = $22`,
		r.Status, nullString(r.MsgID), r.InputTokens, r.OutputTokens,
		r.CacheCreationTokens, r.CacheReadTokens, r.CreditsConsumed,
		r.LatencyMs, r.TTFTMs, nullString(r.ErrorMessage), r.ClientIP,
		nullString(r.UpstreamID), r.Provider,
		nullString(r.RouteID), nullString(r.GroupID),
		r.Attempt, nullString(r.RetryReason), r.SelectionMs,
		r.IsExtraUsage, r.ExtraUsageCostFen, r.ExtraUsageReason,
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
		metadataJSON, _ := json.Marshal(r.Metadata)
		if metadataJSON == nil {
			metadataJSON = []byte("{}")
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO requests (project_id, api_key_id, oauth_grant_id, upstream_id, trace_id, msg_id, provider, model, streaming,
				status, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
				credits_consumed, latency_ms, ttft_ms, error_message, client_ip, created_by, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
			RETURNING id, created_at`,
			r.ProjectID, nullString(r.APIKeyID), nullString(r.OAuthGrantID), nullString(r.UpstreamID),
			nullString(r.TraceID), nullString(r.MsgID),
			r.Provider, r.Model, r.Streaming, r.Status,
			r.InputTokens, r.OutputTokens, r.CacheCreationTokens, r.CacheReadTokens,
			r.CreditsConsumed, r.LatencyMs, r.TTFTMs, nullString(r.ErrorMessage), r.ClientIP, nullString(r.CreatedBy),
			metadataJSON,
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
	if err := s.pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM requests r %s", where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, p.Limit(), p.Offset())
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT r.id, r.project_id, COALESCE(r.api_key_id::text, ''), COALESCE(r.oauth_grant_id::text, ''),
			COALESCE(r.upstream_id::text, ''), COALESCE(r.trace_id::text, ''), COALESCE(r.msg_id, ''),
			r.provider, r.model, r.streaming, r.status, r.input_tokens, r.output_tokens, r.cache_creation_tokens, r.cache_read_tokens,
			r.credits_consumed, r.latency_ms, r.ttft_ms, COALESCE(r.error_message, ''), r.client_ip, r.created_at,
			COALESCE(og.client_name, '') as oauth_grant_client_name,
			r.metadata,
			COALESCE(r.http_log_path, '')
		FROM requests r
		LEFT JOIN oauth_grants og ON og.id = r.oauth_grant_id
		%s ORDER BY %s %s LIMIT $%d OFFSET $%d`,
		where, sanitizeSort(p.Sort, "r.created_at"), sanitizeOrder(p.Order), argN, argN+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var requests []types.Request
	for rows.Next() {
		var r types.Request
		var metadataJSON []byte
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.OAuthGrantID, &r.UpstreamID, &r.TraceID, &r.MsgID,
			&r.Provider, &r.Model, &r.Streaming, &r.Status,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
			&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt,
			&r.OAuthGrantClientName, &metadataJSON, &r.HttpLogPath); err != nil {
			return nil, 0, err
		}
		scanMetadata(&r, metadataJSON)
		requests = append(requests, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return requests, total, nil
}

// RequestFilters holds optional filters for listing requests.
type RequestFilters struct {
	Model     string
	Status    string
	APIKeyID  string
	CreatedBy string
	Since     time.Time
	Until     time.Time
}

func buildRequestFilters(projectID string, f RequestFilters) (string, []interface{}, int) {
	conditions := []string{"r.project_id = $1"}
	args := []interface{}{projectID}
	n := 2

	if f.Model != "" {
		conditions = append(conditions, fmt.Sprintf("r.model = $%d", n))
		args = append(args, f.Model)
		n++
	}
	if f.Status != "" {
		conditions = append(conditions, fmt.Sprintf("r.status = $%d", n))
		args = append(args, f.Status)
		n++
	}
	if f.APIKeyID != "" {
		conditions = append(conditions, fmt.Sprintf("r.api_key_id = $%d", n))
		args = append(args, f.APIKeyID)
		n++
	}
	if f.CreatedBy != "" {
		conditions = append(conditions, fmt.Sprintf("r.created_by = $%d", n))
		args = append(args, f.CreatedBy)
		n++
	}
	if !f.Since.IsZero() {
		conditions = append(conditions, fmt.Sprintf("r.created_at >= $%d", n))
		args = append(args, f.Since)
		n++
	}
	if !f.Until.IsZero() {
		conditions = append(conditions, fmt.Sprintf("r.created_at <= $%d", n))
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
	if err := s.pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM requests r %s", where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, p.Limit(), p.Offset())
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT r.id, r.project_id, COALESCE(r.api_key_id::text, ''), COALESCE(r.oauth_grant_id::text, ''),
			COALESCE(r.upstream_id::text, ''), COALESCE(r.trace_id::text, ''), COALESCE(r.msg_id, ''),
			r.provider, r.model, r.streaming, r.status, r.input_tokens, r.output_tokens, r.cache_creation_tokens, r.cache_read_tokens,
			r.credits_consumed, r.latency_ms, r.ttft_ms, COALESCE(r.error_message, ''), r.client_ip, r.created_at,
			COALESCE(og.client_name, '') as oauth_grant_client_name,
			r.metadata,
			COALESCE(r.http_log_path, '')
		FROM requests r
		LEFT JOIN oauth_grants og ON og.id = r.oauth_grant_id
		%s ORDER BY %s %s LIMIT $%d OFFSET $%d`,
		where, sanitizeSort(p.Sort, "r.created_at"), sanitizeOrder(p.Order), argN, argN+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var requests []types.Request
	for rows.Next() {
		var r types.Request
		var metadataJSON []byte
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.OAuthGrantID, &r.UpstreamID, &r.TraceID, &r.MsgID,
			&r.Provider, &r.Model, &r.Streaming, &r.Status,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
			&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt,
			&r.OAuthGrantClientName, &metadataJSON, &r.HttpLogPath); err != nil {
			return nil, 0, err
		}
		scanMetadata(&r, metadataJSON)
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
		conditions = append(conditions, fmt.Sprintf("r.model = $%d", n))
		args = append(args, f.Model)
		n++
	}
	if f.Status != "" {
		conditions = append(conditions, fmt.Sprintf("r.status = $%d", n))
		args = append(args, f.Status)
		n++
	}
	if f.APIKeyID != "" {
		conditions = append(conditions, fmt.Sprintf("r.api_key_id = $%d", n))
		args = append(args, f.APIKeyID)
		n++
	}
	if f.CreatedBy != "" {
		conditions = append(conditions, fmt.Sprintf("r.created_by = $%d", n))
		args = append(args, f.CreatedBy)
		n++
	}
	if !f.Since.IsZero() {
		conditions = append(conditions, fmt.Sprintf("r.created_at >= $%d", n))
		args = append(args, f.Since)
		n++
	}
	if !f.Until.IsZero() {
		conditions = append(conditions, fmt.Sprintf("r.created_at <= $%d", n))
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
		SELECT r.id, r.project_id, COALESCE(r.api_key_id::text, ''), COALESCE(r.oauth_grant_id::text, ''),
			COALESCE(r.upstream_id::text, ''), COALESCE(r.trace_id::text, ''), COALESCE(r.msg_id, ''),
			r.provider, r.model, r.streaming, r.status, r.input_tokens, r.output_tokens, r.cache_creation_tokens, r.cache_read_tokens,
			r.credits_consumed, r.latency_ms, r.ttft_ms, COALESCE(r.error_message, ''), r.client_ip, r.created_at,
			COALESCE(og.client_name, '') as oauth_grant_client_name,
			r.metadata,
			COALESCE(r.http_log_path, '')
		FROM requests r
		LEFT JOIN oauth_grants og ON og.id = r.oauth_grant_id
		WHERE r.trace_id = $1 ORDER BY r.created_at ASC`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var requests []types.Request
	for rows.Next() {
		var r types.Request
		var metadataJSON []byte
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.OAuthGrantID, &r.UpstreamID, &r.TraceID, &r.MsgID,
			&r.Provider, &r.Model, &r.Streaming, &r.Status,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
			&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt,
			&r.OAuthGrantClientName, &metadataJSON, &r.HttpLogPath); err != nil {
			return nil, err
		}
		scanMetadata(&r, metadataJSON)
		requests = append(requests, r)
	}
	return requests, rows.Err()
}

// UpdateRequestUpstream records the selected upstream on an in-flight request
// so processing-state logs reveal which upstream is currently being attempted.
// Guarded by status='processing' to avoid racing with CompleteRequest.
func (s *Store) UpdateRequestUpstream(reqID, upstreamID, provider string, attempt int) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE requests
		SET upstream_id = $1, provider = $2, attempt = $3
		WHERE id = $4 AND status = 'processing'`,
		nullString(upstreamID), provider, attempt, reqID,
	)
	return err
}

// UpdateHttpLogPath sets the S3 key for the http log on a request row.
func (s *Store) UpdateHttpLogPath(requestID, path string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE requests SET http_log_path = $1 WHERE id = $2`,
		path, requestID,
	)
	return err
}

// GetRequest returns a single request by ID.
func (s *Store) GetRequest(id string) (*types.Request, error) {
	var r types.Request
	var metadataJSON []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT r.id, r.project_id, COALESCE(r.api_key_id::text, ''), COALESCE(r.oauth_grant_id::text, ''),
			COALESCE(r.upstream_id::text, ''), COALESCE(r.trace_id::text, ''), COALESCE(r.msg_id, ''),
			r.provider, r.model, r.streaming, r.status, r.input_tokens, r.output_tokens, r.cache_creation_tokens, r.cache_read_tokens,
			r.credits_consumed, r.latency_ms, r.ttft_ms, COALESCE(r.error_message, ''), r.client_ip, r.created_at,
			COALESCE(og.client_name, '') as oauth_grant_client_name,
			r.metadata,
			COALESCE(r.http_log_path, '')
		FROM requests r
		LEFT JOIN oauth_grants og ON og.id = r.oauth_grant_id
		WHERE r.id = $1`, id,
	).Scan(&r.ID, &r.ProjectID, &r.APIKeyID, &r.OAuthGrantID, &r.UpstreamID, &r.TraceID, &r.MsgID,
		&r.Provider, &r.Model, &r.Streaming, &r.Status,
		&r.InputTokens, &r.OutputTokens, &r.CacheCreationTokens, &r.CacheReadTokens,
		&r.CreditsConsumed, &r.LatencyMs, &r.TTFTMs, &r.ErrorMessage, &r.ClientIP, &r.CreatedAt,
		&r.OAuthGrantClientName, &metadataJSON, &r.HttpLogPath)
	if err != nil {
		return nil, err
	}
	scanMetadata(&r, metadataJSON)
	return &r, nil
}

// scanMetadata parses JSONB metadata into the Request's Metadata map.
func scanMetadata(r *types.Request, data []byte) {
	if len(data) > 2 {
		json.Unmarshal(data, &r.Metadata)
	}
}
