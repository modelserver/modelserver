package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// CreateAPIKey inserts a new API key.
func (s *Store) CreateAPIKey(k *types.APIKey) error {
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO api_keys (project_id, created_by, key_hash, key_prefix, name, description, status, rate_limit_policy_id, allowed_models)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`,
		k.ProjectID, k.CreatedBy, k.KeyHash, k.KeyPrefix, k.Name,
		nullString(k.Description), k.Status,
		nullString(k.RateLimitPolicyID), k.AllowedModels,
	).Scan(&k.ID, &k.CreatedAt, &k.UpdatedAt)
}

// GetAPIKeyByHash returns an API key by its hash (used for authentication).
func (s *Store) GetAPIKeyByHash(hash string) (*types.APIKey, error) {
	k := &types.APIKey{}
	var policyID *string
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, project_id, created_by, key_hash, key_prefix, name, COALESCE(description, ''),
			status, rate_limit_policy_id, allowed_models, expires_at, last_used_at, created_at, updated_at
		FROM api_keys WHERE key_hash = $1`, hash,
	).Scan(&k.ID, &k.ProjectID, &k.CreatedBy, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Description,
		&k.Status, &policyID, &k.AllowedModels, &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get api key by hash: %w", err)
	}
	if policyID != nil {
		k.RateLimitPolicyID = *policyID
	}
	return k, nil
}

// GetAPIKeyByID returns an API key by ID.
func (s *Store) GetAPIKeyByID(id string) (*types.APIKey, error) {
	k := &types.APIKey{}
	var policyID *string
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, project_id, created_by, key_prefix, name, COALESCE(description, ''),
			status, rate_limit_policy_id, allowed_models, expires_at, last_used_at, created_at, updated_at
		FROM api_keys WHERE id = $1`, id,
	).Scan(&k.ID, &k.ProjectID, &k.CreatedBy, &k.KeyPrefix, &k.Name, &k.Description,
		&k.Status, &policyID, &k.AllowedModels, &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get api key by id: %w", err)
	}
	if policyID != nil {
		k.RateLimitPolicyID = *policyID
	}
	return k, nil
}

// ListAPIKeys returns API keys for a project.
func (s *Store) ListAPIKeys(projectID string, p types.PaginationParams) ([]types.APIKey, int, error) {
	return s.listAPIKeys(projectID, "", p)
}

// ListAPIKeysByCreator returns API keys for a project created by a specific user.
func (s *Store) ListAPIKeysByCreator(projectID, createdBy string, p types.PaginationParams) ([]types.APIKey, int, error) {
	return s.listAPIKeys(projectID, createdBy, p)
}

func (s *Store) listAPIKeys(projectID, createdBy string, p types.PaginationParams) ([]types.APIKey, int, error) {
	ctx := context.Background()
	where := "project_id = $1"
	args := []interface{}{projectID}
	if createdBy != "" {
		where += " AND created_by = $2"
		args = append(args, createdBy)
	}

	var total int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM api_keys WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limitIdx := len(args) + 1
	offsetIdx := len(args) + 2
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, project_id, created_by, key_prefix, name, COALESCE(description, ''),
			status, rate_limit_policy_id, allowed_models, expires_at, last_used_at, created_at, updated_at
		FROM api_keys WHERE %s
		ORDER BY %s %s LIMIT $%d OFFSET $%d`,
		where, sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order), limitIdx, offsetIdx),
		append(args, p.Limit(), p.Offset())...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var keys []types.APIKey
	for rows.Next() {
		var k types.APIKey
		var policyID *string
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.CreatedBy, &k.KeyPrefix, &k.Name, &k.Description,
			&k.Status, &policyID, &k.AllowedModels, &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if policyID != nil {
			k.RateLimitPolicyID = *policyID
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return keys, total, nil
}

// UpdateAPIKey updates API key fields.
func (s *Store) UpdateAPIKey(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("api_keys", "id", id, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// UpdateAPIKeyLastUsed updates the last_used_at timestamp.
func (s *Store) UpdateAPIKeyLastUsed(id string) error {
	_, err := s.pool.Exec(context.Background(), "UPDATE api_keys SET last_used_at = NOW() WHERE id = $1", id)
	return err
}
