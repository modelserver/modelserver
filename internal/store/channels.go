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

// CreateChannel inserts a new channel.
func (s *Store) CreateChannel(c *types.Channel) error {
	modelMapJSON, _ := json.Marshal(c.ModelMap)
	if c.ModelMap == nil {
		modelMapJSON = []byte("{}")
	}
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO channels (provider, name, base_url, api_key_encrypted, supported_models, model_map, weight, selection_priority, status, max_concurrent, test_model)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at, updated_at`,
		c.Provider, c.Name, c.BaseURL, c.APIKeyEncrypted,
		c.SupportedModels, modelMapJSON, c.Weight, c.SelectionPriority, c.Status, c.MaxConcurrent, c.TestModel,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
}

// GetChannelByID returns a channel by ID.
func (s *Store) GetChannelByID(id string) (*types.Channel, error) {
	c := &types.Channel{}
	var modelMapRaw []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, provider, name, base_url, api_key_encrypted, supported_models, model_map,
			weight, selection_priority, status, max_concurrent, test_model, created_at, updated_at
		FROM channels WHERE id = $1`, id,
	).Scan(&c.ID, &c.Provider, &c.Name, &c.BaseURL, &c.APIKeyEncrypted,
		&c.SupportedModels, &modelMapRaw, &c.Weight, &c.SelectionPriority, &c.Status,
		&c.MaxConcurrent, &c.TestModel, &c.CreatedAt, &c.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel: %w", err)
	}
	c.ModelMap = unmarshalModelMap(modelMapRaw)
	return c, nil
}

// ListChannels returns all channels (including encrypted API keys for decryption at load time).
func (s *Store) ListChannels() ([]types.Channel, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, provider, name, base_url, api_key_encrypted, supported_models, model_map,
			weight, selection_priority, status, max_concurrent, test_model, created_at, updated_at
		FROM channels ORDER BY selection_priority DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []types.Channel
	for rows.Next() {
		var c types.Channel
		var modelMapRaw []byte
		if err := rows.Scan(&c.ID, &c.Provider, &c.Name, &c.BaseURL, &c.APIKeyEncrypted,
			&c.SupportedModels, &modelMapRaw, &c.Weight, &c.SelectionPriority, &c.Status,
			&c.MaxConcurrent, &c.TestModel, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.ModelMap = unmarshalModelMap(modelMapRaw)
		channels = append(channels, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return channels, nil
}

// ListActiveChannelsForModel returns active channels that support a given model.
func (s *Store) ListActiveChannelsForModel(model string) ([]types.Channel, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, provider, name, base_url, api_key_encrypted, supported_models, model_map,
			weight, selection_priority, status, max_concurrent, test_model, created_at, updated_at
		FROM channels
		WHERE status = 'active' AND $1 = ANY(supported_models)
		ORDER BY selection_priority DESC, weight DESC`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []types.Channel
	for rows.Next() {
		var c types.Channel
		var modelMapRaw []byte
		if err := rows.Scan(&c.ID, &c.Provider, &c.Name, &c.BaseURL, &c.APIKeyEncrypted,
			&c.SupportedModels, &modelMapRaw, &c.Weight, &c.SelectionPriority, &c.Status,
			&c.MaxConcurrent, &c.TestModel, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.ModelMap = unmarshalModelMap(modelMapRaw)
		channels = append(channels, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return channels, nil
}

// UpdateChannel updates channel fields.
func (s *Store) UpdateChannel(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("channels", "id", id, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// DeleteChannel deletes a channel.
func (s *Store) DeleteChannel(id string) error {
	_, err := s.pool.Exec(context.Background(), "DELETE FROM channels WHERE id = $1", id)
	return err
}

// --- Channel Routes ---

// CreateChannelRoute inserts a new channel route.
func (s *Store) CreateChannelRoute(r *types.ChannelRoute) error {
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO channel_routes (project_id, model_pattern, channel_ids, match_priority, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at`,
		nullString(r.ProjectID), r.ModelPattern, r.ChannelIDs, r.MatchPriority, r.Status,
	).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}

// ListChannelRoutes returns all channel routes, ordered by match_priority DESC.
func (s *Store) ListChannelRoutes() ([]types.ChannelRoute, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, COALESCE(project_id::text, ''), model_pattern, channel_ids, match_priority, status, created_at, updated_at
		FROM channel_routes ORDER BY match_priority DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []types.ChannelRoute
	for rows.Next() {
		var r types.ChannelRoute
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.ModelPattern,
			&r.ChannelIDs, &r.MatchPriority, &r.Status, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return routes, nil
}

// ListChannelRoutesForProject returns routes for a specific project + global routes.
func (s *Store) ListChannelRoutesForProject(projectID string) ([]types.ChannelRoute, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, COALESCE(project_id::text, ''), model_pattern, channel_ids, match_priority, status, created_at, updated_at
		FROM channel_routes
		WHERE (project_id = $1 OR project_id IS NULL) AND status = 'active'
		ORDER BY
			CASE WHEN project_id IS NOT NULL THEN 0 ELSE 1 END,
			match_priority DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []types.ChannelRoute
	for rows.Next() {
		var r types.ChannelRoute
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.ModelPattern,
			&r.ChannelIDs, &r.MatchPriority, &r.Status, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return routes, nil
}

// UpdateChannelRoute updates a route.
func (s *Store) UpdateChannelRoute(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("channel_routes", "id", id, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// DeleteChannelRoute deletes a route.
func (s *Store) DeleteChannelRoute(id string) error {
	_, err := s.pool.Exec(context.Background(), "DELETE FROM channel_routes WHERE id = $1", id)
	return err
}
