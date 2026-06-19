package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/modelserver/modelserver/services/payserver/internal/tenant"
)

var (
	ErrTenantNameTaken   = errors.New("tenant name already taken")
	ErrTenantHasPayments = errors.New("tenant has payments and cannot be deleted")
)

// updateAllowedFields enumerates the columns UpdateTenant may touch.
// name is immutable through this path (external systems may reference it
// in audit/log); secret_hash goes through RotateTenantSecret.
var updateAllowedFields = map[string]bool{
	"callback_url":    true,
	"callback_secret": true,
	"description":     true,
	"is_active":       true,
}

func (s *Store) CreateTenant(t *tenant.Tenant) error {
	err := s.pool.QueryRow(context.Background(), `
		INSERT INTO tenants (name, secret_hash, callback_url, callback_secret, description, is_active)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`,
		t.Name, t.SecretHash, t.CallbackURL, t.CallbackSecret, t.Description, t.IsActive,
	).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return ErrTenantNameTaken
		}
		return fmt.Errorf("insert tenant: %w", err)
	}
	return nil
}

const tenantSelectCols = `id, name, secret_hash, callback_url, callback_secret, description, is_active, created_at, updated_at`

func scanTenant(row pgx.Row) (*tenant.Tenant, error) {
	t := &tenant.Tenant{}
	err := row.Scan(&t.ID, &t.Name, &t.SecretHash, &t.CallbackURL, &t.CallbackSecret,
		&t.Description, &t.IsActive, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Store) GetTenantByID(id string) (*tenant.Tenant, error) {
	t, err := scanTenant(s.pool.QueryRow(context.Background(),
		`SELECT `+tenantSelectCols+` FROM tenants WHERE id = $1`, id))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant by id: %w", err)
	}
	return t, nil
}

func (s *Store) GetTenantByName(name string) (*tenant.Tenant, error) {
	t, err := scanTenant(s.pool.QueryRow(context.Background(),
		`SELECT `+tenantSelectCols+` FROM tenants WHERE name = $1`, name))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tenant by name: %w", err)
	}
	return t, nil
}

func (s *Store) ListTenants(limit, offset int) ([]tenant.Tenant, int, error) {
	ctx := context.Background()
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM tenants`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tenants: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+tenantSelectCols+` FROM tenants ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()
	var out []tenant.Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, *t)
	}
	return out, total, rows.Err()
}

func (s *Store) UpdateTenant(id string, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	setClauses := make([]string, 0, len(updates))
	args := make([]any, 0, len(updates)+1)
	i := 1
	for k, v := range updates {
		if !updateAllowedFields[k] {
			return fmt.Errorf("field %q is not updateable through UpdateTenant", k)
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", k, i))
		args = append(args, v)
		i++
	}
	args = append(args, id)
	q := fmt.Sprintf(`UPDATE tenants SET %s, updated_at = NOW() WHERE id = $%d`,
		joinComma(setClauses), i)
	_, err := s.pool.Exec(context.Background(), q, args...)
	if err != nil {
		return fmt.Errorf("update tenant: %w", err)
	}
	return nil
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func (s *Store) RotateTenantSecret(id, newHash string) error {
	_, err := s.pool.Exec(context.Background(),
		`UPDATE tenants SET secret_hash = $1, updated_at = NOW() WHERE id = $2`,
		newHash, id)
	if err != nil {
		return fmt.Errorf("rotate tenant secret: %w", err)
	}
	return nil
}

func (s *Store) DeleteTenant(id string) error {
	_, err := s.pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
			return ErrTenantHasPayments
		}
		return fmt.Errorf("delete tenant: %w", err)
	}
	return nil
}
