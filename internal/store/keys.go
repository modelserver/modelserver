package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/modelserver/modelserver/internal/types"
)

// CreateAPIKey inserts a new API key.
func (s *Store) CreateAPIKey(k *types.APIKey) error {
	return s.db.QueryRow(`
		INSERT INTO api_keys (project_id, created_by, key_hash, key_prefix, name, description, status, rate_limit_policy_id, allowed_models)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`,
		k.ProjectID, k.CreatedBy, k.KeyHash, k.KeyPrefix, k.Name,
		nullString(k.Description), k.Status,
		nullString(k.RateLimitPolicyID), pq.Array(k.AllowedModels),
	).Scan(&k.ID, &k.CreatedAt, &k.UpdatedAt)
}

// GetAPIKeyByHash returns an API key by its hash (used for authentication).
func (s *Store) GetAPIKeyByHash(hash string) (*types.APIKey, error) {
	k := &types.APIKey{}
	var policyID sql.NullString
	err := s.db.QueryRow(`
		SELECT id, project_id, created_by, key_hash, key_prefix, name, COALESCE(description, ''),
			status, rate_limit_policy_id, allowed_models, expires_at, last_used_at, created_at, updated_at
		FROM api_keys WHERE key_hash = $1`, hash,
	).Scan(&k.ID, &k.ProjectID, &k.CreatedBy, &k.KeyHash, &k.KeyPrefix, &k.Name, &k.Description,
		&k.Status, &policyID, pq.Array(&k.AllowedModels), &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get api key by hash: %w", err)
	}
	if policyID.Valid {
		k.RateLimitPolicyID = policyID.String
	}
	return k, nil
}

// GetAPIKeyByID returns an API key by ID.
func (s *Store) GetAPIKeyByID(id string) (*types.APIKey, error) {
	k := &types.APIKey{}
	var policyID sql.NullString
	err := s.db.QueryRow(`
		SELECT id, project_id, created_by, key_prefix, name, COALESCE(description, ''),
			status, rate_limit_policy_id, allowed_models, expires_at, last_used_at, created_at, updated_at
		FROM api_keys WHERE id = $1`, id,
	).Scan(&k.ID, &k.ProjectID, &k.CreatedBy, &k.KeyPrefix, &k.Name, &k.Description,
		&k.Status, &policyID, pq.Array(&k.AllowedModels), &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get api key by id: %w", err)
	}
	if policyID.Valid {
		k.RateLimitPolicyID = policyID.String
	}
	return k, nil
}

// ListAPIKeys returns API keys for a project.
func (s *Store) ListAPIKeys(projectID string, p types.PaginationParams) ([]types.APIKey, int, error) {
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE project_id = $1", projectID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT id, project_id, created_by, key_prefix, name, COALESCE(description, ''),
			status, rate_limit_policy_id, allowed_models, expires_at, last_used_at, created_at, updated_at
		FROM api_keys WHERE project_id = $1
		ORDER BY %s %s LIMIT $2 OFFSET $3`,
		sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order)),
		projectID, p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var keys []types.APIKey
	for rows.Next() {
		var k types.APIKey
		var policyID sql.NullString
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.CreatedBy, &k.KeyPrefix, &k.Name, &k.Description,
			&k.Status, &policyID, pq.Array(&k.AllowedModels), &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if policyID.Valid {
			k.RateLimitPolicyID = policyID.String
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
	_, err := s.db.Exec(query, args...)
	return err
}

// UpdateAPIKeyLastUsed updates the last_used_at timestamp.
func (s *Store) UpdateAPIKeyLastUsed(id string) error {
	_, err := s.db.Exec("UPDATE api_keys SET last_used_at = NOW() WHERE id = $1", id)
	return err
}
