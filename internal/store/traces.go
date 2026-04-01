package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// CreateTrace inserts a new trace.
func (s *Store) CreateTrace(t *types.Trace) error {
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO traces (project_id, source)
		VALUES ($1, $2)
		RETURNING id, created_at, updated_at`,
		t.ProjectID, t.Source,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

// GetTraceByID returns a trace by ID.
func (s *Store) GetTraceByID(id string) (*types.Trace, error) {
	t := &types.Trace{}
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, project_id, source, created_at, updated_at
		FROM traces WHERE id = $1`, id,
	).Scan(&t.ID, &t.ProjectID, &t.Source, &t.CreatedAt, &t.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get trace: %w", err)
	}
	return t, nil
}

// ListTraces returns traces for a project with pagination.
// When createdBy is non-empty, only traces containing at least one request
// by that user are returned (used to scope developer visibility).
func (s *Store) ListTraces(projectID string, p types.PaginationParams, createdBy string) ([]types.Trace, int, error) {
	ctx := context.Background()

	where := "WHERE t.project_id = $1"
	args := []interface{}{projectID}
	n := 2
	if createdBy != "" {
		where += fmt.Sprintf(" AND EXISTS (SELECT 1 FROM requests r WHERE r.trace_id = t.id AND r.created_by = $%d)", n)
		args = append(args, createdBy)
		n++
	}

	var total int
	if err := s.pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM traces t %s", where), args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, p.Limit(), p.Offset())
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT t.id, t.project_id, t.source, t.created_at, t.updated_at
		FROM traces t %s
		ORDER BY %s %s LIMIT $%d OFFSET $%d`,
		where, sanitizeSort(p.Sort, "t.created_at"), sanitizeOrder(p.Order), n, n+1),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var traces []types.Trace
	for rows.Next() {
		var t types.Trace
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Source, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, 0, err
		}
		traces = append(traces, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return traces, total, nil
}

// EnsureTrace creates a trace record if it doesn't already exist.
// This is used by the proxy to lazily register traces on first use.
func (s *Store) EnsureTrace(projectID, traceID, source string) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO traces (id, project_id, source)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO NOTHING`,
		traceID, projectID, source,
	)
	return err
}
