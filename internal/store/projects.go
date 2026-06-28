package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

const projectColumns = `id, name, COALESCE(description, ''), created_by, status, settings, billing_tags, created_at, updated_at`

func scanProject(row pgx.Row) (*types.Project, error) {
	p := &types.Project{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedBy, &p.Status,
		&p.Settings, &p.BillingTags, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// CreateProject inserts a new project and adds the creator as owner.
func (s *Store) CreateProject(p *types.Project) error {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	settings := p.Settings
	if len(settings) == 0 {
		settings = json.RawMessage(`{}`)
	}
	billingTags := p.BillingTags
	if billingTags == nil {
		billingTags = []string{}
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO projects (name, description, created_by, status, settings, billing_tags)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`,
		p.Name, nullString(p.Description), p.CreatedBy, p.Status, settings, billingTags,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		tx.Rollback(ctx)
		return fmt.Errorf("insert project: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO project_members (user_id, project_id, role)
		VALUES ($1, $2, 'owner')`, p.CreatedBy, p.ID)
	if err != nil {
		tx.Rollback(ctx)
		return fmt.Errorf("add owner member: %w", err)
	}

	return tx.Commit(ctx)
}

// GetProjectByID returns a project by ID.
func (s *Store) GetProjectByID(id string) (*types.Project, error) {
	p, err := scanProject(s.pool.QueryRow(context.Background(), `SELECT `+projectColumns+` FROM projects WHERE id = $1`, id))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

// ListUserProjects returns projects the user is a member of (excludes archived by default).
func (s *Store) ListUserProjects(userID string, p types.PaginationParams) ([]types.Project, int, error) {
	ctx := context.Background()
	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM projects
		JOIN project_members ON projects.id = project_members.project_id
		WHERE project_members.user_id = $1 AND projects.status != 'archived'`, userID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count projects: %w", err)
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT p.id, p.name, COALESCE(p.description, ''), p.created_by, p.status, p.settings, p.billing_tags, p.created_at, p.updated_at
		FROM projects p
		JOIN project_members pm ON p.id = pm.project_id
		WHERE pm.user_id = $1 AND p.status != 'archived'
		ORDER BY p.%s %s LIMIT $2 OFFSET $3`,
		sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order)),
		userID, p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var projects []types.Project
	for rows.Next() {
		proj, err := scanProject(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, *proj)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, total, nil
}

// ListAllProjects returns all projects with pagination (for superadmin).
func (s *Store) ListAllProjects(p types.PaginationParams) ([]types.Project, int, error) {
	ctx := context.Background()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM projects`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count projects: %w", err)
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT `+projectColumns+`
		FROM projects
		ORDER BY %s %s LIMIT $1 OFFSET $2`,
		sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order)),
		p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list all projects: %w", err)
	}
	defer rows.Close()

	var projects []types.Project
	for rows.Next() {
		proj, err := scanProject(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, *proj)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, total, nil
}

// UpdateProject updates project fields.
func (s *Store) UpdateProject(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("projects", "id", id, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}


// --- Project Members ---

// AddProjectMember adds a member to a project.
func (s *Store) AddProjectMember(projectID, userID, role string) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO project_members (user_id, project_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, project_id) DO UPDATE SET role = EXCLUDED.role`,
		userID, projectID, role)
	return err
}

// GetProjectMember returns a single member.
func (s *Store) GetProjectMember(projectID, userID string) (*types.ProjectMember, error) {
	m := &types.ProjectMember{}
	err := s.pool.QueryRow(context.Background(), `
		SELECT user_id, project_id, role, created_at, credit_quota_percent, denied_models
		FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreatedAt, &m.CreditQuotaPct, &m.DeniedModels)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}
	return m, nil
}

// ListProjectMembers returns all members of a project with user info.
func (s *Store) ListProjectMembers(projectID string) ([]types.ProjectMember, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT pm.user_id, pm.project_id, pm.role, pm.credit_quota_percent, pm.denied_models, pm.created_at,
			u.id, u.email, u.nickname, COALESCE(u.picture, '')
		FROM project_members pm
		JOIN users u ON pm.user_id = u.id
		WHERE pm.project_id = $1
		ORDER BY pm.created_at ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var members []types.ProjectMember
	for rows.Next() {
		var m types.ProjectMember
		var u types.User
		if err := rows.Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreditQuotaPct, &m.DeniedModels, &m.CreatedAt,
			&u.ID, &u.Email, &u.Nickname, &u.Picture); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		m.User = &u
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}
	return members, nil
}

// ListProjectMembersPaginated returns a paginated list of project members with user info.
func (s *Store) ListProjectMembersPaginated(projectID string, p types.PaginationParams) ([]types.ProjectMember, int, error) {
	ctx := context.Background()
	var total int
	if err := s.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM project_members WHERE project_id = $1", projectID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count members: %w", err)
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT pm.user_id, pm.project_id, pm.role, pm.credit_quota_percent, pm.denied_models, pm.created_at,
			u.id, u.email, u.nickname, COALESCE(u.picture, '')
		FROM project_members pm
		JOIN users u ON pm.user_id = u.id
		WHERE pm.project_id = $1
		ORDER BY pm.%s %s LIMIT $2 OFFSET $3`,
		sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order)),
		projectID, p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list members paginated: %w", err)
	}
	defer rows.Close()

	var members []types.ProjectMember
	for rows.Next() {
		var m types.ProjectMember
		var u types.User
		if err := rows.Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreditQuotaPct, &m.DeniedModels, &m.CreatedAt,
			&u.ID, &u.Email, &u.Nickname, &u.Picture); err != nil {
			return nil, 0, fmt.Errorf("scan member: %w", err)
		}
		m.User = &u
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate members: %w", err)
	}
	return members, total, nil
}

// UpdateProjectMemberRole updates a member's role.
func (s *Store) UpdateProjectMemberRole(projectID, userID, role string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE project_members SET role = $1
		WHERE project_id = $2 AND user_id = $3`, role, projectID, userID)
	return err
}

// UpdateProjectMember updates a member's role, credit quota, and/or
// denied models. Pass nil pointers to leave fields unchanged.
//
//   - role:           *string. nil = unchanged.
//   - creditQuotaPct: **float64. nil = unchanged; non-nil pointer whose
//     value is nil = set NULL; non-nil pointer whose value is non-nil =
//     set that float. (Same convention the previous signature used.)
//   - deniedModels:   *[]string. nil = unchanged; non-nil pointer (including
//     empty slice) = replace the column with that slice. Empty slice means
//     "no model is denied".
//
// If role is set to "owner", credit_quota_percent is forced to NULL
// regardless of the creditQuotaPct argument.
func (s *Store) UpdateProjectMember(
	projectID, userID string,
	role *string,
	creditQuotaPct **float64,
	deniedModels *[]string,
) error {
	if role == nil && creditQuotaPct == nil && deniedModels == nil {
		return nil
	}

	sets := make([]string, 0, 3)
	args := make([]any, 0, 5)
	next := 1
	add := func(col string, val any) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, next))
		args = append(args, val)
		next++
	}

	if role != nil {
		add("role", *role)
		// Owner promotion always clears the quota, even if the caller
		// also passed an explicit creditQuotaPct.
		if *role == types.RoleOwner {
			sets = append(sets, "credit_quota_percent = NULL")
		} else if creditQuotaPct != nil {
			add("credit_quota_percent", *creditQuotaPct) // *float64 (may be nil → SQL NULL)
		}
	} else if creditQuotaPct != nil {
		add("credit_quota_percent", *creditQuotaPct)
	}

	if deniedModels != nil {
		// pgx encodes a non-nil []string as TEXT[]. Empty slice → '{}'.
		add("denied_models", *deniedModels)
	}

	args = append(args, projectID, userID)
	query := fmt.Sprintf(
		"UPDATE project_members SET %s WHERE project_id = $%d AND user_id = $%d",
		strings.Join(sets, ", "), next, next+1,
	)

	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// RemoveProjectMember atomically revokes every active API key the member
// created in this project, then removes the member. Returns the number
// of keys flipped from 'active' to 'revoked'.
//
// The revocation UPDATE runs FIRST so a concurrent in-flight request can
// never observe (member-deleted, key-still-active) during the tx window.
// Postgres rolls both statements back on any failure.
//
// If the user has no membership row (pre-migration zombie state), the
// keys are still revoked and the DELETE of zero rows is not an error.
func (s *Store) RemoveProjectMember(projectID, userID string) (int, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after Commit

	tag, err := tx.Exec(ctx, `
		UPDATE api_keys
		   SET status = 'revoked', updated_at = NOW()
		 WHERE project_id = $1 AND created_by = $2 AND status = 'active'`,
		projectID, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke keys: %w", err)
	}
	revokedCount := int(tag.RowsAffected())

	if _, err := tx.Exec(ctx,
		`DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`,
		projectID, userID); err != nil {
		return 0, fmt.Errorf("delete member: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return revokedCount, nil
}

// CountActiveKeysForMember returns the number of api_keys with
// status='active' that the user created in the project. Used by the
// dashboard's pre-removal confirmation dialog.
func (s *Store) CountActiveKeysForMember(projectID, userID string) (int, error) {
	var n int
	err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM api_keys
		 WHERE project_id = $1 AND created_by = $2 AND status = 'active'`,
		projectID, userID).Scan(&n)
	return n, err
}
