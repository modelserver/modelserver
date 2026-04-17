package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

const modelColumns = `name, display_name, COALESCE(description, ''), aliases, default_credit_rate, status, metadata, created_at, updated_at`

func scanModel(row pgx.Row) (*types.Model, error) {
	m := &types.Model{}
	var rateRaw, metadataRaw []byte
	if err := row.Scan(&m.Name, &m.DisplayName, &m.Description, &m.Aliases,
		&rateRaw, &m.Status, &metadataRaw, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	if m.Aliases == nil {
		m.Aliases = []string{}
	}
	if len(rateRaw) > 0 {
		var rate types.CreditRate
		if err := json.Unmarshal(rateRaw, &rate); err == nil {
			m.DefaultCreditRate = &rate
		}
	}
	if len(metadataRaw) > 0 {
		_ = json.Unmarshal(metadataRaw, &m.Metadata)
	}
	return m, nil
}

// CreateModel inserts a new model row. Returns a unique-violation or
// trigger-reported error for invalid names/aliases.
func (s *Store) CreateModel(m *types.Model) error {
	aliases := m.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	var rateJSON []byte
	if m.DefaultCreditRate != nil {
		rateJSON, _ = json.Marshal(m.DefaultCreditRate)
	}
	metadataJSON, _ := json.Marshal(m.Metadata)
	if len(metadataJSON) == 0 {
		metadataJSON = []byte("{}")
	}
	status := m.Status
	if status == "" {
		status = types.ModelStatusActive
	}
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO models (name, display_name, description, aliases, default_credit_rate, status, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at, updated_at`,
		m.Name, m.DisplayName, nullString(m.Description), aliases, nullableJSON(rateJSON), status, metadataJSON,
	).Scan(&m.CreatedAt, &m.UpdatedAt)
}

// GetModelByName returns a single model by canonical name.
func (s *Store) GetModelByName(name string) (*types.Model, error) {
	row := s.pool.QueryRow(context.Background(),
		`SELECT `+modelColumns+` FROM models WHERE name = $1`, name)
	m, err := scanModel(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get model: %w", err)
	}
	return m, nil
}

// ListModels returns every catalog row ordered by name.
func (s *Store) ListModels() ([]types.Model, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT `+modelColumns+` FROM models ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()

	var models []types.Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}
		models = append(models, *m)
	}
	return models, rows.Err()
}

// ListModelsByStatus returns every catalog row with the given status, ordered by name.
func (s *Store) ListModelsByStatus(status string) ([]types.Model, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT `+modelColumns+` FROM models WHERE status = $1 ORDER BY name ASC`, status)
	if err != nil {
		return nil, fmt.Errorf("list models by status: %w", err)
	}
	defer rows.Close()

	var models []types.Model
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}
		models = append(models, *m)
	}
	return models, rows.Err()
}

// UpdateModel applies a partial update. Callers pre-validate the keys;
// `name` (the primary key) is intentionally not updatable.
func (s *Store) UpdateModel(name string, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("models", "name", name, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// DeleteModel removes a model row. The reader-side trigger rejects delete
// attempts while upstreams, routes, or api_keys still reference the name.
func (s *Store) DeleteModel(name string) error {
	_, err := s.pool.Exec(context.Background(), "DELETE FROM models WHERE name = $1", name)
	return err
}

// ModelReferenceCounts summarises how many rows reference a given model name
// across the tables the catalog governs. It feeds the admin LIST response so
// the UI can disable delete when any count is non-zero.
type ModelReferenceCounts struct {
	Upstreams int `json:"upstreams"`
	Routes    int `json:"routes"`
	Plans     int `json:"plans"`
	Policies  int `json:"policies"`
	APIKeys   int `json:"api_keys"`
}

// Total returns the sum across every reference table.
func (c ModelReferenceCounts) Total() int {
	return c.Upstreams + c.Routes + c.Plans + c.Policies + c.APIKeys
}

// ModelReferenceCountsFor runs the five COUNT queries for a single name.
// Acceptable at catalog sizes ≪ 1000 and LIST frequencies ≪ 1 Hz.
func (s *Store) ModelReferenceCountsFor(name string) (ModelReferenceCounts, error) {
	ctx := context.Background()
	var c ModelReferenceCounts
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM upstreams WHERE $1 = ANY(supported_models)`, name).Scan(&c.Upstreams); err != nil {
		return c, fmt.Errorf("count upstream refs: %w", err)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM routes WHERE $1 = ANY(model_names)`, name).Scan(&c.Routes); err != nil {
		return c, fmt.Errorf("count route refs: %w", err)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM plans WHERE model_credit_rates ? $1`, name).Scan(&c.Plans); err != nil {
		return c, fmt.Errorf("count plan refs: %w", err)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM rate_limit_policies WHERE model_credit_rates ? $1`, name).Scan(&c.Policies); err != nil {
		return c, fmt.Errorf("count policy refs: %w", err)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM api_keys WHERE $1 = ANY(allowed_models)`, name).Scan(&c.APIKeys); err != nil {
		return c, fmt.Errorf("count api_key refs: %w", err)
	}
	return c, nil
}

// nullableJSON returns nil (SQL NULL) for an empty byte slice, or the slice itself.
func nullableJSON(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	return b
}
