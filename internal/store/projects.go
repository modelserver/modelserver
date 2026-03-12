package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/modelserver/modelserver/internal/types"
)

const projectColumns = `id, name, COALESCE(description, ''), created_by, status, settings, billing_tags, created_at, updated_at`

func scanProject(row interface{ Scan(...interface{}) error }) (*types.Project, error) {
	p := &types.Project{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedBy, &p.Status,
		&p.Settings, pq.Array(&p.BillingTags), &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// CreateProject inserts a new project and adds the creator as owner.
func (s *Store) CreateProject(p *types.Project) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	settings := p.Settings
	if len(settings) == 0 {
		settings = json.RawMessage(`{}`)
	}

	err = tx.QueryRow(`
		INSERT INTO projects (name, description, created_by, status, settings, billing_tags)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`,
		p.Name, nullString(p.Description), p.CreatedBy, p.Status, settings, pq.Array(p.BillingTags),
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("insert project: %w", err)
	}

	_, err = tx.Exec(`
		INSERT INTO project_members (user_id, project_id, role)
		VALUES ($1, $2, 'owner')`, p.CreatedBy, p.ID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("add owner member: %w", err)
	}

	return tx.Commit()
}

// GetProjectByID returns a project by ID.
func (s *Store) GetProjectByID(id string) (*types.Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectColumns+` FROM projects WHERE id = $1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

// ListUserProjects returns projects the user is a member of.
func (s *Store) ListUserProjects(userID string, p types.PaginationParams) ([]types.Project, int, error) {
	var total int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM projects
		JOIN project_members ON projects.id = project_members.project_id
		WHERE project_members.user_id = $1`, userID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count projects: %w", err)
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT p.id, p.name, COALESCE(p.description, ''), p.created_by, p.status, p.settings, p.billing_tags, p.created_at, p.updated_at
		FROM projects p
		JOIN project_members pm ON p.id = pm.project_id
		WHERE pm.user_id = $1
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
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count projects: %w", err)
	}

	rows, err := s.db.Query(fmt.Sprintf(`
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
	_, err := s.db.Exec(query, args...)
	return err
}

// DeleteProject deletes a project by ID.
func (s *Store) DeleteProject(id string) error {
	_, err := s.db.Exec("DELETE FROM projects WHERE id = $1", id)
	return err
}

// --- Project Members ---

// AddProjectMember adds a member to a project.
func (s *Store) AddProjectMember(projectID, userID, role string) error {
	_, err := s.db.Exec(`
		INSERT INTO project_members (user_id, project_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, project_id) DO UPDATE SET role = EXCLUDED.role`,
		userID, projectID, role)
	return err
}

// GetProjectMember returns a single member.
func (s *Store) GetProjectMember(projectID, userID string) (*types.ProjectMember, error) {
	m := &types.ProjectMember{}
	err := s.db.QueryRow(`
		SELECT user_id, project_id, role, created_at
		FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	).Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}
	return m, nil
}

// ListProjectMembers returns all members of a project with user info.
func (s *Store) ListProjectMembers(projectID string) ([]types.ProjectMember, error) {
	rows, err := s.db.Query(`
		SELECT pm.user_id, pm.project_id, pm.role, pm.created_at,
			u.id, u.email, u.name, COALESCE(u.picture, '')
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
		if err := rows.Scan(&m.UserID, &m.ProjectID, &m.Role, &m.CreatedAt,
			&u.ID, &u.Email, &u.Name, &u.Picture); err != nil {
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
	_, err := s.db.Exec(`
		UPDATE project_members SET role = $1
		WHERE project_id = $2 AND user_id = $3`, role, projectID, userID)
	return err
}

// RemoveProjectMember removes a member from a project.
func (s *Store) RemoveProjectMember(projectID, userID string) error {
	_, err := s.db.Exec(`
		DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID)
	return err
}
