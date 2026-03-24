package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// CreateOAuthGrant inserts or updates an OAuth grant record.
// On re-authorization (same project + user + client), the scopes and timestamp are refreshed.
func (s *Store) CreateOAuthGrant(g *types.OAuthGrant) error {
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO oauth_grants (project_id, user_id, client_id, scopes)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (project_id, user_id, client_id) DO UPDATE SET
			scopes = EXCLUDED.scopes,
			created_at = NOW()
		RETURNING id, created_at`,
		g.ProjectID, g.UserID, g.ClientID, g.Scopes,
	).Scan(&g.ID, &g.CreatedAt)
}

// ListOAuthGrants returns all OAuth grants for a project, ordered by most recent first.
func (s *Store) ListOAuthGrants(projectID string) ([]types.OAuthGrant, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT g.id, g.project_id, g.user_id,
			COALESCE(u.nickname, ''), COALESCE(u.picture, ''),
			g.client_id, g.scopes, g.created_at
		FROM oauth_grants g
		LEFT JOIN users u ON u.id = g.user_id
		WHERE g.project_id = $1
		ORDER BY g.created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list oauth grants: %w", err)
	}
	defer rows.Close()

	var grants []types.OAuthGrant
	for rows.Next() {
		var g types.OAuthGrant
		if err := rows.Scan(&g.ID, &g.ProjectID, &g.UserID,
			&g.UserNickname, &g.UserPicture,
			&g.ClientID, &g.Scopes, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan oauth grant: %w", err)
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list oauth grants rows: %w", err)
	}
	return grants, nil
}

// GetOAuthGrantByID returns a single OAuth grant by its ID.
func (s *Store) GetOAuthGrantByID(id string) (*types.OAuthGrant, error) {
	g := &types.OAuthGrant{}
	err := s.pool.QueryRow(context.Background(), `
		SELECT g.id, g.project_id, g.user_id,
			COALESCE(u.nickname, ''), COALESCE(u.picture, ''),
			g.client_id, g.scopes, g.created_at
		FROM oauth_grants g
		LEFT JOIN users u ON u.id = g.user_id
		WHERE g.id = $1`, id,
	).Scan(&g.ID, &g.ProjectID, &g.UserID,
		&g.UserNickname, &g.UserPicture,
		&g.ClientID, &g.Scopes, &g.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get oauth grant by id: %w", err)
	}
	return g, nil
}

// DeleteOAuthGrant hard-deletes an OAuth grant by its ID.
func (s *Store) DeleteOAuthGrant(id string) error {
	_, err := s.pool.Exec(context.Background(), "DELETE FROM oauth_grants WHERE id = $1", id)
	return err
}
