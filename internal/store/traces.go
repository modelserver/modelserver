package store

import (
	"database/sql"
	"fmt"

	"github.com/modelserver/modelserver/internal/types"
)

// CreateTrace inserts a new trace.
func (s *Store) CreateTrace(t *types.Trace) error {
	threadID := nullString(t.ThreadID)
	return s.db.QueryRow(`
		INSERT INTO traces (project_id, thread_id, source)
		VALUES ($1, $2, $3)
		RETURNING id, created_at, updated_at`,
		t.ProjectID, threadID, t.Source,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

// GetTraceByID returns a trace by ID.
func (s *Store) GetTraceByID(id string) (*types.Trace, error) {
	t := &types.Trace{}
	var threadID sql.NullString
	err := s.db.QueryRow(`
		SELECT id, project_id, COALESCE(thread_id::text, ''), source, created_at, updated_at
		FROM traces WHERE id = $1`, id,
	).Scan(&t.ID, &t.ProjectID, &t.ThreadID, &t.Source, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get trace: %w", err)
	}
	if threadID.Valid {
		t.ThreadID = threadID.String
	}
	return t, nil
}

// ListTraces returns traces for a project with pagination.
func (s *Store) ListTraces(projectID string, p types.PaginationParams) ([]types.Trace, int, error) {
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM traces WHERE project_id = $1", projectID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT id, project_id, COALESCE(thread_id::text, ''), source, created_at, updated_at
		FROM traces WHERE project_id = $1
		ORDER BY %s %s LIMIT $2 OFFSET $3`,
		sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order)),
		projectID, p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var traces []types.Trace
	for rows.Next() {
		var t types.Trace
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.ThreadID, &t.Source, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, 0, err
		}
		traces = append(traces, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return traces, total, nil
}

// CreateThread inserts a new thread.
func (s *Store) CreateThread(t *types.Thread) error {
	return s.db.QueryRow(`
		INSERT INTO threads (project_id)
		VALUES ($1)
		RETURNING id, created_at, updated_at`,
		t.ProjectID,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
}

// GetThreadByID returns a thread by ID.
func (s *Store) GetThreadByID(id string) (*types.Thread, error) {
	t := &types.Thread{}
	err := s.db.QueryRow(`
		SELECT id, project_id, created_at, updated_at
		FROM threads WHERE id = $1`, id,
	).Scan(&t.ID, &t.ProjectID, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get thread: %w", err)
	}
	return t, nil
}

// ListThreads returns threads for a project with pagination.
func (s *Store) ListThreads(projectID string, p types.PaginationParams) ([]types.Thread, int, error) {
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM threads WHERE project_id = $1", projectID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT id, project_id, created_at, updated_at
		FROM threads WHERE project_id = $1
		ORDER BY %s %s LIMIT $2 OFFSET $3`,
		sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order)),
		projectID, p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var threads []types.Thread
	for rows.Next() {
		var t types.Thread
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, 0, err
		}
		threads = append(threads, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return threads, total, nil
}

// EnsureTrace creates a trace record if it doesn't already exist.
// This is used by the proxy to lazily register traces on first use.
func (s *Store) EnsureTrace(projectID, traceID, threadID, source string) error {
	tidArg := nullString(threadID)
	_, err := s.db.Exec(`
		INSERT INTO traces (id, project_id, thread_id, source)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING`,
		traceID, projectID, tidArg, source,
	)
	return err
}
