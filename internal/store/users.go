package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// CreateUser inserts a new user.
func (s *Store) CreateUser(u *types.User) error {
	return s.db.QueryRow(`
		INSERT INTO users (email, password_hash, name, avatar_url, oauth_provider, oauth_id, is_superadmin, max_projects, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`,
		u.Email, nullString(u.PasswordHash), u.Name, nullString(u.AvatarURL),
		nullString(u.OAuthProvider), nullString(u.OAuthID),
		u.IsSuperadmin, u.MaxProjects, u.Status,
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
}

// GetUserByID returns a user by ID.
func (s *Store) GetUserByID(id string) (*types.User, error) {
	u := &types.User{}
	err := s.db.QueryRow(`
		SELECT id, email, password_hash, name, COALESCE(avatar_url, ''), COALESCE(oauth_provider, ''), COALESCE(oauth_id, ''),
			is_superadmin, max_projects, status, created_at, updated_at
		FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.AvatarURL, &u.OAuthProvider, &u.OAuthID,
		&u.IsSuperadmin, &u.MaxProjects, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

// GetUserByEmail returns a user by email.
func (s *Store) GetUserByEmail(email string) (*types.User, error) {
	u := &types.User{}
	err := s.db.QueryRow(`
		SELECT id, email, password_hash, name, COALESCE(avatar_url, ''), COALESCE(oauth_provider, ''), COALESCE(oauth_id, ''),
			is_superadmin, max_projects, status, created_at, updated_at
		FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.AvatarURL, &u.OAuthProvider, &u.OAuthID,
		&u.IsSuperadmin, &u.MaxProjects, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

// GetUserByOAuth returns a user by OAuth provider and ID.
func (s *Store) GetUserByOAuth(provider, oauthID string) (*types.User, error) {
	u := &types.User{}
	err := s.db.QueryRow(`
		SELECT id, email, password_hash, name, COALESCE(avatar_url, ''), COALESCE(oauth_provider, ''), COALESCE(oauth_id, ''),
			is_superadmin, max_projects, status, created_at, updated_at
		FROM users WHERE oauth_provider = $1 AND oauth_id = $2`, provider, oauthID,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.AvatarURL, &u.OAuthProvider, &u.OAuthID,
		&u.IsSuperadmin, &u.MaxProjects, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by oauth: %w", err)
	}
	return u, nil
}

// ListUsers returns all users with pagination.
func (s *Store) ListUsers(p types.PaginationParams) ([]types.User, int, error) {
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT id, email, name, COALESCE(avatar_url, ''), COALESCE(oauth_provider, ''),
			is_superadmin, max_projects, status, created_at, updated_at
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
		var u types.User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.OAuthProvider,
			&u.IsSuperadmin, &u.MaxProjects, &u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
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
	_, err := s.db.Exec(query, args...)
	return err
}

// CountUserOwnedProjects returns the number of projects a user owns.
func (s *Store) CountUserOwnedProjects(userID string) (int, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM project_members
		WHERE user_id = $1 AND role = 'owner'`, userID,
	).Scan(&count)
	return count, err
}

// UserExists returns true if any user exists in the system.
func (s *Store) UserExists() (bool, error) {
	var exists bool
	err := s.db.QueryRow("SELECT EXISTS(SELECT 1 FROM users LIMIT 1)").Scan(&exists)
	return exists, err
}

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func sanitizeSort(input, fallback string) string {
	allowed := map[string]bool{
		"created_at": true, "updated_at": true, "name": true, "email": true, "status": true,
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
