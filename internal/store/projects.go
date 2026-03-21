package store

import (
	"context"
	"encoding/json"
	"fmt"
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
		SELECT user_id, project_id, role, created_at, credit_quota_percent
		FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreatedAt, &m.CreditQuotaPct)
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
		SELECT pm.user_id, pm.project_id, pm.role, pm.credit_quota_percent, pm.created_at,
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
		if err := rows.Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreditQuotaPct, &m.CreatedAt,
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

// UpdateProjectMemberRole updates a member's role.
func (s *Store) UpdateProjectMemberRole(projectID, userID, role string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE project_members SET role = $1
		WHERE project_id = $2 AND user_id = $3`, role, projectID, userID)
	return err
}

// UpdateProjectMember updates a member's role and/or credit quota.
// Pass nil pointers to leave fields unchanged.
// If role is set to "owner", credit_quota_percent is forced to NULL.
func (s *Store) UpdateProjectMember(projectID, userID string, role *string, creditQuotaPct **float64) error {
	if role == nil && creditQuotaPct == nil {
		return nil
	}

	// If promoting to owner, clear quota.
	if role != nil && *role == types.RoleOwner {
		_, err := s.pool.Exec(context.Background(), `
			UPDATE project_members SET role = $1, credit_quota_percent = NULL
			WHERE project_id = $2 AND user_id = $3`, *role, projectID, userID)
		return err
	}

	if role != nil && creditQuotaPct != nil {
		_, err := s.pool.Exec(context.Background(), `
			UPDATE project_members SET role = $1, credit_quota_percent = $2
			WHERE project_id = $3 AND user_id = $4`, *role, *creditQuotaPct, projectID, userID)
		return err
	}

	if role != nil {
		_, err := s.pool.Exec(context.Background(), `
			UPDATE project_members SET role = $1
			WHERE project_id = $2 AND user_id = $3`, *role, projectID, userID)
		return err
	}

	// creditQuotaPct only
	_, err := s.pool.Exec(context.Background(), `
		UPDATE project_members SET credit_quota_percent = $1
		WHERE project_id = $2 AND user_id = $3`, *creditQuotaPct, projectID, userID)
	return err
}

// RemoveProjectMember removes a member from a project.
func (s *Store) RemoveProjectMember(projectID, userID string) error {
	_, err := s.pool.Exec(context.Background(), `
		DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID)
	return err
}
