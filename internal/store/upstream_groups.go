package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// UpstreamGroupWithMembers is an UpstreamGroup with its resolved member upstreams.
type UpstreamGroupWithMembers struct {
	types.UpstreamGroup
	Members []UpstreamGroupMemberDetail `json:"members"`
}

// UpstreamGroupMemberDetail is an UpstreamGroupMember with its resolved Upstream.
type UpstreamGroupMemberDetail struct {
	types.UpstreamGroupMember
	Upstream *types.Upstream `json:"upstream"`
}

// CreateUpstreamGroup inserts a new upstream group.
func (s *Store) CreateUpstreamGroup(g *types.UpstreamGroup) error {
	retryPolicyJSON, _ := json.Marshal(g.RetryPolicy)
	if g.RetryPolicy == nil {
		retryPolicyJSON = []byte("{}")
	}
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO upstream_groups (name, lb_policy, retry_policy, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at, updated_at`,
		g.Name, g.LBPolicy, retryPolicyJSON, g.Status,
	).Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt)
}

// GetUpstreamGroupByID returns an upstream group by ID.
func (s *Store) GetUpstreamGroupByID(id string) (*types.UpstreamGroup, error) {
	g := &types.UpstreamGroup{}
	var retryPolicyRaw []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, name, lb_policy, retry_policy, status, created_at, updated_at
		FROM upstream_groups WHERE id = $1`, id,
	).Scan(&g.ID, &g.Name, &g.LBPolicy, &retryPolicyRaw, &g.Status, &g.CreatedAt, &g.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get upstream group: %w", err)
	}
	g.RetryPolicy = unmarshalRetryPolicy(retryPolicyRaw)
	return g, nil
}

// ListUpstreamGroups returns all upstream groups ordered by name.
func (s *Store) ListUpstreamGroups() ([]types.UpstreamGroup, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, name, lb_policy, retry_policy, status, created_at, updated_at
		FROM upstream_groups ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []types.UpstreamGroup
	for rows.Next() {
		g, err := scanUpstreamGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}

// UpdateUpstreamGroup updates upstream group fields.
func (s *Store) UpdateUpstreamGroup(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("upstream_groups", "id", id, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// DeleteUpstreamGroup deletes an upstream group.
func (s *Store) DeleteUpstreamGroup(id string) error {
	_, err := s.pool.Exec(context.Background(), "DELETE FROM upstream_groups WHERE id = $1", id)
	return err
}

// ---------------------------------------------------------------------------
// Upstream Group Members
// ---------------------------------------------------------------------------

// AddUpstreamGroupMember inserts a member into an upstream group.
func (s *Store) AddUpstreamGroupMember(m *types.UpstreamGroupMember) error {
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO upstream_group_members (upstream_group_id, upstream_id, weight, is_backup)
		VALUES ($1, $2, $3, $4)`,
		m.UpstreamGroupID, m.UpstreamID, m.Weight, m.IsBackup,
	)
	return err
}

// RemoveUpstreamGroupMember removes a member from an upstream group.
func (s *Store) RemoveUpstreamGroupMember(groupID, upstreamID string) error {
	_, err := s.pool.Exec(context.Background(), `
		DELETE FROM upstream_group_members
		WHERE upstream_group_id = $1 AND upstream_id = $2`,
		groupID, upstreamID,
	)
	return err
}

// ListUpstreamGroupMembers returns all members of an upstream group.
func (s *Store) ListUpstreamGroupMembers(groupID string) ([]types.UpstreamGroupMember, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT upstream_group_id, upstream_id, weight, is_backup
		FROM upstream_group_members
		WHERE upstream_group_id = $1`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []types.UpstreamGroupMember
	for rows.Next() {
		var m types.UpstreamGroupMember
		if err := rows.Scan(&m.UpstreamGroupID, &m.UpstreamID, &m.Weight, &m.IsBackup); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return members, nil
}

// ListUpstreamGroupsPaginated returns upstream groups with pagination.
func (s *Store) ListUpstreamGroupsPaginated(p types.PaginationParams) ([]types.UpstreamGroup, int, error) {
	ctx := context.Background()
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM upstream_groups").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count upstream groups: %w", err)
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, name, lb_policy, retry_policy, status, created_at, updated_at
		FROM upstream_groups ORDER BY %s %s LIMIT $1 OFFSET $2`,
		sanitizeSort(p.Sort, "name"), sanitizeOrder(p.Order)),
		p.Limit(), p.Offset(),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list upstream groups: %w", err)
	}
	defer rows.Close()

	var groups []types.UpstreamGroup
	for rows.Next() {
		g, err := scanUpstreamGroup(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan upstream group: %w", err)
		}
		groups = append(groups, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate upstream groups: %w", err)
	}
	return groups, total, nil
}

// ListUpstreamGroupsWithMembersPaginated returns paginated upstream groups with their member upstreams resolved.
func (s *Store) ListUpstreamGroupsWithMembersPaginated(p types.PaginationParams) ([]UpstreamGroupWithMembers, int, error) {
	groups, total, err := s.ListUpstreamGroupsPaginated(p)
	if err != nil {
		return nil, 0, err
	}
	if len(groups) == 0 {
		return []UpstreamGroupWithMembers{}, total, nil
	}

	// Collect group IDs for the current page.
	ids := make([]string, len(groups))
	for i, g := range groups {
		ids[i] = g.ID
	}

	// Fetch members for just these groups.
	rows, err := s.pool.Query(context.Background(), `
		SELECT m.upstream_group_id, m.upstream_id, m.weight, m.is_backup,
			u.id, u.provider, u.name, u.base_url, u.api_key_encrypted, u.supported_models,
			u.model_map, u.weight, u.status, u.max_concurrent, u.test_model,
			u.health_check, u.dial_timeout, u.read_timeout, u.created_at, u.updated_at
		FROM upstream_group_members m
		JOIN upstreams u ON u.id = m.upstream_id
		WHERE m.upstream_group_id = ANY($1)
		ORDER BY m.upstream_group_id, u.name`, ids)
	if err != nil {
		return nil, 0, fmt.Errorf("list upstream group members: %w", err)
	}
	defer rows.Close()

	membersByGroup := make(map[string][]UpstreamGroupMemberDetail)
	for rows.Next() {
		var m types.UpstreamGroupMember
		u := &types.Upstream{}
		var modelMapRaw, healthCheckRaw []byte
		var dialTimeout, readTimeout *time.Duration
		if err := rows.Scan(
			&m.UpstreamGroupID, &m.UpstreamID, &m.Weight, &m.IsBackup,
			&u.ID, &u.Provider, &u.Name, &u.BaseURL, &u.APIKeyEncrypted, &u.SupportedModels,
			&modelMapRaw, &u.Weight, &u.Status, &u.MaxConcurrent, &u.TestModel,
			&healthCheckRaw, &dialTimeout, &readTimeout, &u.CreatedAt, &u.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan member with upstream: %w", err)
		}
		u.ModelMap = unmarshalModelMap(modelMapRaw)
		u.HealthCheck = unmarshalHealthCheck(healthCheckRaw)
		if dialTimeout != nil {
			u.DialTimeout = *dialTimeout
		}
		if readTimeout != nil {
			u.ReadTimeout = *readTimeout
		}
		membersByGroup[m.UpstreamGroupID] = append(membersByGroup[m.UpstreamGroupID], UpstreamGroupMemberDetail{
			UpstreamGroupMember: m,
			Upstream:            u,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate member rows: %w", err)
	}

	result := make([]UpstreamGroupWithMembers, len(groups))
	for i, g := range groups {
		result[i] = UpstreamGroupWithMembers{
			UpstreamGroup: g,
			Members:       membersByGroup[g.ID],
		}
		if result[i].Members == nil {
			result[i].Members = []UpstreamGroupMemberDetail{}
		}
	}
	return result, total, nil
}

// ListUpstreamGroupsWithMembers returns all upstream groups with their member upstreams resolved.
func (s *Store) ListUpstreamGroupsWithMembers() ([]UpstreamGroupWithMembers, error) {
	// First, fetch all groups.
	groups, err := s.ListUpstreamGroups()
	if err != nil {
		return nil, fmt.Errorf("list upstream groups: %w", err)
	}

	// Fetch all members with their upstream details via a JOIN.
	rows, err := s.pool.Query(context.Background(), `
		SELECT m.upstream_group_id, m.upstream_id, m.weight, m.is_backup,
			u.id, u.provider, u.name, u.base_url, u.api_key_encrypted, u.supported_models,
			u.model_map, u.weight, u.status, u.max_concurrent, u.test_model,
			u.health_check, u.dial_timeout, u.read_timeout, u.created_at, u.updated_at
		FROM upstream_group_members m
		JOIN upstreams u ON u.id = m.upstream_id
		ORDER BY m.upstream_group_id, u.name`)
	if err != nil {
		return nil, fmt.Errorf("list upstream group members with upstreams: %w", err)
	}
	defer rows.Close()

	// Index members by group ID.
	membersByGroup := make(map[string][]UpstreamGroupMemberDetail)
	for rows.Next() {
		var m types.UpstreamGroupMember
		u := &types.Upstream{}
		var modelMapRaw, healthCheckRaw []byte
		var dialTimeout, readTimeout *time.Duration
		if err := rows.Scan(
			&m.UpstreamGroupID, &m.UpstreamID, &m.Weight, &m.IsBackup,
			&u.ID, &u.Provider, &u.Name, &u.BaseURL, &u.APIKeyEncrypted, &u.SupportedModels,
			&modelMapRaw, &u.Weight, &u.Status, &u.MaxConcurrent, &u.TestModel,
			&healthCheckRaw, &dialTimeout, &readTimeout, &u.CreatedAt, &u.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan member with upstream: %w", err)
		}
		u.ModelMap = unmarshalModelMap(modelMapRaw)
		u.HealthCheck = unmarshalHealthCheck(healthCheckRaw)
		if dialTimeout != nil {
			u.DialTimeout = *dialTimeout
		}
		if readTimeout != nil {
			u.ReadTimeout = *readTimeout
		}
		membersByGroup[m.UpstreamGroupID] = append(membersByGroup[m.UpstreamGroupID], UpstreamGroupMemberDetail{
			UpstreamGroupMember: m,
			Upstream:            u,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate member rows: %w", err)
	}

	// Assemble the result.
	result := make([]UpstreamGroupWithMembers, len(groups))
	for i, g := range groups {
		result[i] = UpstreamGroupWithMembers{
			UpstreamGroup: g,
			Members:       membersByGroup[g.ID],
		}
		if result[i].Members == nil {
			result[i].Members = []UpstreamGroupMemberDetail{}
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// scanUpstreamGroup scans a single upstream group row from a Rows iterator.
func scanUpstreamGroup(rows pgx.Rows) (*types.UpstreamGroup, error) {
	g := &types.UpstreamGroup{}
	var retryPolicyRaw []byte
	if err := rows.Scan(&g.ID, &g.Name, &g.LBPolicy, &retryPolicyRaw, &g.Status, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	g.RetryPolicy = unmarshalRetryPolicy(retryPolicyRaw)
	return g, nil
}

// unmarshalRetryPolicy decodes a JSONB column value into a RetryPolicy.
func unmarshalRetryPolicy(data []byte) *types.RetryPolicy {
	if len(data) == 0 {
		return nil
	}
	var rp types.RetryPolicy
	if err := json.Unmarshal(data, &rp); err != nil {
		return nil
	}
	return &rp
}
