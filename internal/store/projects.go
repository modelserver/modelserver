package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

var (
	// ErrNotAMember: target user is not a member of the project.
	ErrNotAMember = errors.New("user is not a project member")
	// ErrAlreadyOwner: target user is already the project owner.
	ErrAlreadyOwner = errors.New("user is already the project owner")
	// ErrOwnerMustTransfer: tried to demote or remove the current owner
	// outside of the transfer-ownership flow.
	ErrOwnerMustTransfer = errors.New("owner cannot be demoted or removed directly; use transfer-ownership")
	// ErrInvalidRole: a role string is not in types.AssignableRoles
	// (or equals types.RoleOwner where owner is not permitted).
	ErrInvalidRole = errors.New("invalid role")
	// ErrInvariantViolated: a store transaction's post-state would have
	// owner count ≠ 1. Indicates concurrent writes or corrupt data.
	ErrInvariantViolated = errors.New("project owner-count invariant violated")
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

// ProjectListFilters narrows ListAllProjects. Empty fields are ignored
// (= no filter on that dimension). Used by the admin /projects list
// page to support project-id and created-by filtering. Both empty =
// behaves identically to today's no-filter call.
type ProjectListFilters struct {
	ProjectID string // exact match against projects.id (UUID as string)
	CreatedBy string // exact match against projects.created_by (UUID as string)
}

// ListAllProjects returns projects with pagination (for superadmin).
// `filters` narrows by project ID and/or creator; empty fields mean no
// filter. Both COUNT and the data SELECT share the WHERE so total
// reflects the filtered set.
func (s *Store) ListAllProjects(p types.PaginationParams, filters ProjectListFilters) ([]types.Project, int, error) {
	ctx := context.Background()

	// Build WHERE clause dynamically so pgx can use positional args
	// without struct-tag plumbing. Numbering starts at $1 and increments
	// per added clause.
	var where strings.Builder
	where.WriteString("WHERE 1=1")
	args := make([]any, 0, 4)
	if filters.ProjectID != "" {
		args = append(args, filters.ProjectID)
		fmt.Fprintf(&where, " AND id = $%d", len(args))
	}
	if filters.CreatedBy != "" {
		args = append(args, filters.CreatedBy)
		fmt.Fprintf(&where, " AND created_by = $%d", len(args))
	}

	var total int
	if err := s.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM projects %s`, where.String()),
		args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count projects: %w", err)
	}

	// Append LIMIT / OFFSET positional args AFTER the WHERE args so the
	// numbering stays contiguous.
	limitArg := len(args) + 1
	offsetArg := len(args) + 2
	args = append(args, p.Limit(), p.Offset())

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT `+projectColumns+`
		FROM projects
		%s
		ORDER BY %s %s LIMIT $%d OFFSET $%d`,
		where.String(),
		sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order),
		limitArg, offsetArg,
	), args...)
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
func (s *Store) UpdateProjectMember(
	projectID, userID string,
	role *string,
	creditQuotaPct **float64,
	deniedModels *[]string,
) error {
	if role == nil && creditQuotaPct == nil && deniedModels == nil {
		return nil
	}

	// Single-owner invariant: changing role to RoleOwner is never allowed
	// here (use TransferProjectOwnership). Changing role *away* from
	// RoleOwner is never allowed here either — same reason.
	if role != nil {
		if *role == types.RoleOwner {
			return ErrInvalidRole
		}
		if !types.IsAssignableRole(*role) {
			return ErrInvalidRole
		}
		// Check current role; if the caller is trying to demote the owner, reject.
		var current string
		if err := s.pool.QueryRow(context.Background(),
			`SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2`,
			projectID, userID).Scan(&current); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("get current role: %w", ErrNotAMember)
			}
			return fmt.Errorf("get current role: %w", err)
		}
		if current == types.RoleOwner {
			return ErrOwnerMustTransfer
		}
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
		if creditQuotaPct != nil {
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

// RemoveProjectMember atomically revokes the member's active API keys
// and deletes the member's OAuth grants for the project, then removes
// the membership row. Returns the revoked-key count and the list of
// deleted grants (so the caller can revoke matching Hydra consents).
//
// Ordering: revoke keys → delete grants → delete membership. All three
// run in one tx so concurrent in-flight requests cannot observe a
// (member-deleted, key-active) or (member-deleted, grant-present) state.
//
// If the user has no membership row (pre-migration zombie state), the
// keys and grants are still cleaned up.
func (s *Store) RemoveProjectMember(projectID, userID string) (int, []types.OAuthGrant, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after Commit

	// Single-owner invariant: refuse to delete the current owner.
	// SELECT FOR UPDATE locks the row so a concurrent UPDATE of role
	// (or a concurrent transfer-ownership) blocks until we commit or
	// rollback.
	var role string
	if err := tx.QueryRow(ctx, `
		SELECT role FROM project_members
		WHERE project_id=$1 AND user_id=$2
		FOR UPDATE`, projectID, userID,
	).Scan(&role); err != nil {
		if err == pgx.ErrNoRows {
			// No member row — fall through; the legacy "revoke orphan
			// keys" behavior below still applies.
			role = ""
		} else {
			return 0, nil, fmt.Errorf("lock member row: %w", err)
		}
	}
	if role == types.RoleOwner {
		return 0, nil, ErrOwnerMustTransfer
	}

	tag, err := tx.Exec(ctx, `
		UPDATE api_keys
		   SET status = 'revoked', updated_at = NOW()
		 WHERE project_id = $1 AND created_by = $2 AND status = 'active'`,
		projectID, userID)
	if err != nil {
		return 0, nil, fmt.Errorf("revoke keys: %w", err)
	}
	revokedCount := int(tag.RowsAffected())

	rows, err := tx.Query(ctx, `
		DELETE FROM oauth_grants
		 WHERE project_id = $1 AND user_id = $2
		 RETURNING id, project_id, user_id, client_id, client_name, scopes, created_at`,
		projectID, userID)
	if err != nil {
		return 0, nil, fmt.Errorf("delete grants: %w", err)
	}
	var deletedGrants []types.OAuthGrant
	for rows.Next() {
		var g types.OAuthGrant
		if err := rows.Scan(&g.ID, &g.ProjectID, &g.UserID,
			&g.ClientID, &g.ClientName, &g.Scopes, &g.CreatedAt); err != nil {
			rows.Close()
			return 0, nil, fmt.Errorf("scan deleted grant: %w", err)
		}
		deletedGrants = append(deletedGrants, g)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("delete grants rows: %w", err)
	}
	rows.Close()

	if _, err := tx.Exec(ctx,
		`DELETE FROM project_members WHERE project_id=$1 AND user_id=$2`,
		projectID, userID); err != nil {
		return 0, nil, fmt.Errorf("delete member: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, nil, fmt.Errorf("commit: %w", err)
	}
	return revokedCount, deletedGrants, nil
}

// CountUserCreatedProjects counts projects where the user is the
// original creator (projects.created_by = $1). Includes archived
// projects — archive is not a quota release. Used by the per-user
// max_projects check at project-creation time.
func (s *Store) CountUserCreatedProjects(userID string) (int, error) {
	var count int
	err := s.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM projects WHERE created_by = $1`, userID,
	).Scan(&count)
	return count, err
}

// TransferProjectOwnership atomically swaps the project owner from
// fromUID to toUID, demoting fromUID to demoteTo. The whole operation
// runs in a single transaction with row locks on the relevant
// project_members rows; concurrent writers either block or see the
// post-state.
//
// Pre-conditions checked inside the tx:
//   - demoteTo is a member of types.AssignableRoles.
//   - Exactly one row in this project has role='owner', and its user_id
//     equals fromUID (a stale fromUID returns ErrInvariantViolated).
//   - toUID has a membership row in this project (else ErrNotAMember).
//   - toUID != fromUID (else ErrAlreadyOwner).
//
// Post-conditions guaranteed at COMMIT:
//   - exactly one role='owner' row, owned by toUID.
//   - toUID's credit_quota_percent = NULL (owners never carry a quota).
//   - fromUID's role = demoteTo, credit_quota_percent = NULL.
func (s *Store) TransferProjectOwnership(projectID, fromUID, toUID, demoteTo string) error {
	if !types.IsAssignableRole(demoteTo) {
		return ErrInvalidRole
	}
	if fromUID == toUID {
		return ErrAlreadyOwner
	}

	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after Commit

	// Lock every member row in this project. Sorting by user_id avoids
	// deadlocks if two transfer ops on the same project race.
	rows, err := tx.Query(ctx, `
		SELECT user_id, role FROM project_members
		WHERE project_id = $1
		ORDER BY user_id
		FOR UPDATE`, projectID)
	if err != nil {
		return fmt.Errorf("lock members: %w", err)
	}
	var (
		currentOwner string
		ownerCount   int
		targetSeen   bool
		targetRole   string
	)
	for rows.Next() {
		var uid, role string
		if err := rows.Scan(&uid, &role); err != nil {
			rows.Close()
			return fmt.Errorf("scan member: %w", err)
		}
		if role == types.RoleOwner {
			currentOwner = uid
			ownerCount++
		}
		if uid == toUID {
			targetSeen = true
			targetRole = role
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate members: %w", err)
	}

	if ownerCount != 1 || currentOwner != fromUID {
		return ErrInvariantViolated
	}
	if !targetSeen {
		return ErrNotAMember
	}
	if targetRole == types.RoleOwner {
		// Defensive — we already checked fromUID != toUID, so this can
		// only happen if multiple owners exist (caught above) or there
		// is a fundamental bug.
		return ErrAlreadyOwner
	}

	if _, err := tx.Exec(ctx, `
		UPDATE project_members
		   SET role = $1, credit_quota_percent = NULL
		 WHERE project_id = $2 AND user_id = $3`,
		demoteTo, projectID, fromUID); err != nil {
		return fmt.Errorf("demote old owner: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE project_members
		   SET role = $1, credit_quota_percent = NULL
		 WHERE project_id = $2 AND user_id = $3`,
		types.RoleOwner, projectID, toUID); err != nil {
		return fmt.Errorf("promote new owner: %w", err)
	}

	// Post-check.
	var postOwners int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM project_members WHERE project_id=$1 AND role='owner'`,
		projectID).Scan(&postOwners); err != nil {
		return fmt.Errorf("post-count: %w", err)
	}
	if postOwners != 1 {
		return ErrInvariantViolated
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
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
