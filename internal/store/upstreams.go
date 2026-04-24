package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// unmarshalModelMap decodes a JSONB column value into a model map.
func unmarshalModelMap(data []byte) map[string]string {
	if len(data) == 0 {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

// CreateUpstream inserts a new upstream.
func (s *Store) CreateUpstream(u *types.Upstream) error {
	modelMapJSON, _ := json.Marshal(u.ModelMap)
	if u.ModelMap == nil {
		modelMapJSON = []byte("{}")
	}
	healthCheckJSON, _ := json.Marshal(u.HealthCheck)
	if u.HealthCheck == nil {
		healthCheckJSON = []byte(`{"enabled": true, "interval": 30000000000, "timeout": 5000000000}`)
	}
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO upstreams (provider, name, base_url, api_key_encrypted, supported_models, model_map,
			weight, status, max_concurrent, test_model, health_check, read_timeout)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, created_at, updated_at`,
		u.Provider, u.Name, u.BaseURL, u.APIKeyEncrypted,
		u.SupportedModels, modelMapJSON, u.Weight, u.Status, u.MaxConcurrent, u.TestModel,
		healthCheckJSON, durationToInterval(u.ReadTimeout),
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
}

// GetUpstreamByID returns an upstream by ID.
func (s *Store) GetUpstreamByID(id string) (*types.Upstream, error) {
	u := &types.Upstream{}
	var modelMapRaw, healthCheckRaw []byte
	var readTimeout *time.Duration
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, provider, name, base_url, api_key_encrypted, supported_models, model_map,
			weight, status, max_concurrent, test_model, health_check, read_timeout,
			created_at, updated_at
		FROM upstreams WHERE id = $1`, id,
	).Scan(&u.ID, &u.Provider, &u.Name, &u.BaseURL, &u.APIKeyEncrypted,
		&u.SupportedModels, &modelMapRaw, &u.Weight, &u.Status,
		&u.MaxConcurrent, &u.TestModel, &healthCheckRaw, &readTimeout,
		&u.CreatedAt, &u.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get upstream: %w", err)
	}
	u.ModelMap = unmarshalModelMap(modelMapRaw)
	u.HealthCheck = unmarshalHealthCheck(healthCheckRaw)
	if readTimeout != nil {
		u.ReadTimeout = *readTimeout
	}
	return u, nil
}

// ListUpstreams returns all upstreams ordered by name.
func (s *Store) ListUpstreams() ([]types.Upstream, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, provider, name, base_url, api_key_encrypted, supported_models, model_map,
			weight, status, max_concurrent, test_model, health_check, read_timeout,
			created_at, updated_at
		FROM upstreams ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var upstreams []types.Upstream
	for rows.Next() {
		u, err := scanUpstream(rows)
		if err != nil {
			return nil, err
		}
		upstreams = append(upstreams, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return upstreams, nil
}

// ListUpstreamsPaginated returns upstreams with pagination.
func (s *Store) ListUpstreamsPaginated(p types.PaginationParams) ([]types.Upstream, int, error) {
	ctx := context.Background()
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM upstreams").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count upstreams: %w", err)
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, provider, name, base_url, api_key_encrypted, supported_models, model_map,
			weight, status, max_concurrent, test_model, health_check, read_timeout,
			created_at, updated_at
		FROM upstreams ORDER BY %s %s LIMIT $1 OFFSET $2`,
		sanitizeSort(p.Sort, "name"), sanitizeOrder(p.Order)),
		p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list upstreams: %w", err)
	}
	defer rows.Close()

	var upstreams []types.Upstream
	for rows.Next() {
		u, err := scanUpstream(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan upstream: %w", err)
		}
		upstreams = append(upstreams, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate upstreams: %w", err)
	}
	return upstreams, total, nil
}

// ListActiveUpstreamsForModel returns active upstreams that support a given model,
// ordered by weight descending.
func (s *Store) ListActiveUpstreamsForModel(model string) ([]types.Upstream, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, provider, name, base_url, api_key_encrypted, supported_models, model_map,
			weight, status, max_concurrent, test_model, health_check, read_timeout,
			created_at, updated_at
		FROM upstreams
		WHERE status = 'active' AND $1 = ANY(supported_models)
		ORDER BY weight DESC`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var upstreams []types.Upstream
	for rows.Next() {
		u, err := scanUpstream(rows)
		if err != nil {
			return nil, err
		}
		upstreams = append(upstreams, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return upstreams, nil
}

// UpdateUpstream updates upstream fields.
func (s *Store) UpdateUpstream(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("upstreams", "id", id, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// DeleteUpstream deletes an upstream.
func (s *Store) DeleteUpstream(id string) error {
	_, err := s.pool.Exec(context.Background(), "DELETE FROM upstreams WHERE id = $1", id)
	return err
}

// scanUpstream scans a single upstream row from a Rows iterator.
func scanUpstream(rows pgx.Rows) (*types.Upstream, error) {
	u := &types.Upstream{}
	var modelMapRaw, healthCheckRaw []byte
	var readTimeout *time.Duration
	if err := rows.Scan(&u.ID, &u.Provider, &u.Name, &u.BaseURL, &u.APIKeyEncrypted,
		&u.SupportedModels, &modelMapRaw, &u.Weight, &u.Status,
		&u.MaxConcurrent, &u.TestModel, &healthCheckRaw, &readTimeout,
		&u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	u.ModelMap = unmarshalModelMap(modelMapRaw)
	u.HealthCheck = unmarshalHealthCheck(healthCheckRaw)
	if readTimeout != nil {
		u.ReadTimeout = *readTimeout
	}
	return u, nil
}

// unmarshalHealthCheck decodes a JSONB column value into a HealthCheckConfig.
func unmarshalHealthCheck(data []byte) *types.HealthCheckConfig {
	if len(data) == 0 {
		return nil
	}
	var hc types.HealthCheckConfig
	if err := json.Unmarshal(data, &hc); err != nil {
		return nil
	}
	return &hc
}

// durationToInterval converts a time.Duration to a value suitable for PostgreSQL INTERVAL columns.
// A zero duration is stored as NULL.
func durationToInterval(d time.Duration) interface{} {
	if d == 0 {
		return nil
	}
	return d
}
