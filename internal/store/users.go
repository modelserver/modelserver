package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// scanUser scans a user row (without auth fields).
func scanUser(row pgx.Row) (*types.User, error) {
	u := &types.User{}
	err := row.Scan(&u.ID, &u.Email, &u.Nickname, &u.Picture,
		&u.IsSuperadmin, &u.MaxProjects, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

const userColumns = `id, email, nickname, COALESCE(picture, ''), is_superadmin, max_projects, status, created_at, updated_at`

// CreateUser inserts a new user.
func (s *Store) CreateUser(u *types.User) error {
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO users (email, nickname, picture, is_superadmin, max_projects, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`,
		u.Email, u.Nickname, nullString(u.Picture),
		u.IsSuperadmin, u.MaxProjects, u.Status,
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
}

// GetUserByID returns a user by ID.
func (s *Store) GetUserByID(id string) (*types.User, error) {
	u, err := scanUser(s.pool.QueryRow(context.Background(), `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

// GetUserByEmail returns a user by email.
func (s *Store) GetUserByEmail(email string) (*types.User, error) {
	u, err := scanUser(s.pool.QueryRow(context.Background(), `SELECT `+userColumns+` FROM users WHERE email = $1`, email))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

// GetUserByOAuth returns a user who has an OAuth connection with the given provider and provider ID.
func (s *Store) GetUserByOAuth(provider, providerID string) (*types.User, error) {
	u, err := scanUser(s.pool.QueryRow(context.Background(), `
		SELECT u.id, u.email, u.nickname, COALESCE(u.picture, ''), u.is_superadmin, u.max_projects, u.status, u.created_at, u.updated_at
		FROM users u
		JOIN user_oauth_connections oc ON oc.user_id = u.id
		WHERE oc.provider = $1 AND oc.provider_id = $2`, provider, providerID))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by oauth: %w", err)
	}
	return u, nil
}

// ListUsers returns all users with pagination.
func (s *Store) ListUsers(p types.PaginationParams) ([]types.User, int, error) {
	ctx := context.Background()
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT `+userColumns+`
		FROM users ORDER BY %s %s LIMIT $1 OFFSET $2`,
		sanitizeSort(p.Sort, "created_at"), sanitizeOrder(p.Order)),
		p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []types.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate users: %w", err)
	}
	return users, total, nil
}

// UpdateUser updates user fields.
func (s *Store) UpdateUser(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("users", "id", id, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// CountUserOwnedProjects returns the number of projects a user owns.
func (s *Store) CountUserOwnedProjects(userID string) (int, error) {
	var count int
	err := s.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM project_members
		WHERE user_id = $1 AND role = 'owner'`, userID,
	).Scan(&count)
	return count, err
}

// UserExists returns true if any user exists in the system.
func (s *Store) UserExists() (bool, error) {
	var exists bool
	err := s.pool.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM users LIMIT 1)").Scan(&exists)
	return exists, err
}

// ---------------------------------------------------------------------------
// OAuth connections
// ---------------------------------------------------------------------------

// CreateOAuthConnection links an OAuth identity to a user.
func (s *Store) CreateOAuthConnection(userID, provider, providerID string) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO user_oauth_connections (user_id, provider, provider_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (provider, provider_id) DO NOTHING`,
		userID, provider, providerID)
	return err
}

// GetOAuthConnections returns all OAuth connections for a user.
func (s *Store) GetOAuthConnections(userID string) ([]types.OAuthConnection, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT user_id, provider, provider_id, created_at
		FROM user_oauth_connections WHERE user_id = $1
		ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conns []types.OAuthConnection
	for rows.Next() {
		var c types.OAuthConnection
		if err := rows.Scan(&c.UserID, &c.Provider, &c.ProviderID, &c.CreatedAt); err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, rows.Err()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func sanitizeSort(input, fallback string) string {
	allowed := map[string]bool{
		"created_at": true, "updated_at": true, "nickname": true, "email": true, "status": true,
	}
	if allowed[input] {
		return input
	}
	return fallback
}

func sanitizeOrder(input string) string {
	if input == "asc" || input == "ASC" {
		return "ASC"
	}
	return "DESC"
}

func buildUpdateQuery(table, pkCol, pkVal string, updates map[string]interface{}) (string, []interface{}) {
	setClauses := make([]string, 0, len(updates))
	args := make([]interface{}, 0, len(updates)+1)
	i := 1
	for col, val := range updates {
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, i))
		args = append(args, val)
		i++
	}
	args = append(args, pkVal)
	query := fmt.Sprintf("UPDATE %s SET %s WHERE %s = $%d",
		table, joinStrings(setClauses, ", "), pkCol, i)
	return query, args
}

func joinStrings(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
