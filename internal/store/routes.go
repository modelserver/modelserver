package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// CreateRoute inserts a new route.
func (s *Store) CreateRoute(r *types.Route) error {
	conditionsJSON, _ := json.Marshal(r.Conditions)
	if r.Conditions == nil {
		conditionsJSON = []byte("{}")
	}
	modelNames := r.ModelNames
	if modelNames == nil {
		modelNames = []string{}
	}
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO routes (project_id, model_names, upstream_group_id, match_priority, conditions, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`,
		nullString(r.ProjectID), modelNames, r.UpstreamGroupID,
		r.MatchPriority, conditionsJSON, r.Status,
	).Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}

// GetRouteByID returns a route by ID.
func (s *Store) GetRouteByID(id string) (*types.Route, error) {
	r := &types.Route{}
	var conditionsRaw []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, COALESCE(project_id::text, ''), model_names, upstream_group_id,
			match_priority, conditions, status, created_at, updated_at
		FROM routes WHERE id = $1`, id,
	).Scan(&r.ID, &r.ProjectID, &r.ModelNames, &r.UpstreamGroupID,
		&r.MatchPriority, &conditionsRaw, &r.Status, &r.CreatedAt, &r.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get route: %w", err)
	}
	r.Conditions = unmarshalConditions(conditionsRaw)
	return r, nil
}

// ListRoutes returns all routes ordered by match_priority descending.
func (s *Store) ListRoutes() ([]types.Route, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, COALESCE(project_id::text, ''), model_names, upstream_group_id,
			match_priority, conditions, status, created_at, updated_at
		FROM routes ORDER BY match_priority DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []types.Route
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		routes = append(routes, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return routes, nil
}

// ListRoutesPaginated returns routes with pagination.
func (s *Store) ListRoutesPaginated(p types.PaginationParams) ([]types.Route, int, error) {
	ctx := context.Background()
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM routes").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count routes: %w", err)
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, COALESCE(project_id::text, ''), model_names, upstream_group_id,
			match_priority, conditions, status, created_at, updated_at
		FROM routes ORDER BY %s %s LIMIT $1 OFFSET $2`,
		sanitizeSort(p.Sort, "match_priority"), sanitizeOrder(p.Order)),
		p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list routes: %w", err)
	}
	defer rows.Close()

	var routes []types.Route
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan route: %w", err)
		}
		routes = append(routes, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate routes: %w", err)
	}
	return routes, total, nil
}

// ListRoutesForProject returns active routes for a specific project plus global routes.
// Project-specific routes are returned first, then global routes; within each group
// routes are ordered by match_priority descending.
func (s *Store) ListRoutesForProject(projectID string) ([]types.Route, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, COALESCE(project_id::text, ''), model_names, upstream_group_id,
			match_priority, conditions, status, created_at, updated_at
		FROM routes
		WHERE (project_id = $1 OR project_id IS NULL) AND status = 'active'
		ORDER BY
			CASE WHEN project_id IS NOT NULL THEN 0 ELSE 1 END,
			match_priority DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []types.Route
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		routes = append(routes, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return routes, nil
}

// UpdateRoute updates route fields.
func (s *Store) UpdateRoute(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("routes", "id", id, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// DeleteRoute deletes a route.
func (s *Store) DeleteRoute(id string) error {
	_, err := s.pool.Exec(context.Background(), "DELETE FROM routes WHERE id = $1", id)
	return err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// scanRoute scans a single route row from a Rows iterator.
func scanRoute(rows pgx.Rows) (*types.Route, error) {
	r := &types.Route{}
	var conditionsRaw []byte
	if err := rows.Scan(&r.ID, &r.ProjectID, &r.ModelNames, &r.UpstreamGroupID,
		&r.MatchPriority, &conditionsRaw, &r.Status, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Conditions = unmarshalConditions(conditionsRaw)
	return r, nil
}

// unmarshalConditions decodes a JSONB column value into a conditions map.
func unmarshalConditions(data []byte) map[string]string {
	if len(data) == 0 {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}
