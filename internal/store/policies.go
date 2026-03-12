package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// CreatePolicy inserts a new rate limit policy.
func (s *Store) CreatePolicy(p *types.RateLimitPolicy) error {
	creditRulesJSON, _ := json.Marshal(p.CreditRules)
	ratesJSON, _ := json.Marshal(p.ModelCreditRates)
	classicJSON, _ := json.Marshal(p.ClassicRules)

	return s.pool.QueryRow(context.Background(), `
		INSERT INTO rate_limit_policies (project_id, name, is_default, credit_rules, model_credit_rates, classic_rules, starts_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`,
		p.ProjectID, p.Name, p.IsDefault, creditRulesJSON, ratesJSON, classicJSON,
		p.StartsAt, p.ExpiresAt,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

// GetPolicyByID returns a policy by ID.
func (s *Store) GetPolicyByID(id string) (*types.RateLimitPolicy, error) {
	p := &types.RateLimitPolicy{}
	var creditRules, rates, classic []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, project_id, name, is_default, credit_rules, model_credit_rates, classic_rules,
			starts_at, expires_at, created_at, updated_at
		FROM rate_limit_policies WHERE id = $1`, id,
	).Scan(&p.ID, &p.ProjectID, &p.Name, &p.IsDefault, &creditRules, &rates, &classic,
		&p.StartsAt, &p.ExpiresAt, &p.CreatedAt, &p.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get policy: %w", err)
	}
	if err := unmarshalPolicyJSON(p, creditRules, rates, classic); err != nil {
		return nil, fmt.Errorf("get policy: %w", err)
	}
	return p, nil
}

// GetDefaultPolicy returns the default policy for a project.
func (s *Store) GetDefaultPolicy(projectID string) (*types.RateLimitPolicy, error) {
	p := &types.RateLimitPolicy{}
	var creditRules, rates, classic []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, project_id, name, is_default, credit_rules, model_credit_rates, classic_rules,
			starts_at, expires_at, created_at, updated_at
		FROM rate_limit_policies WHERE project_id = $1 AND is_default = TRUE
		LIMIT 1`, projectID,
	).Scan(&p.ID, &p.ProjectID, &p.Name, &p.IsDefault, &creditRules, &rates, &classic,
		&p.StartsAt, &p.ExpiresAt, &p.CreatedAt, &p.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get default policy: %w", err)
	}
	if err := unmarshalPolicyJSON(p, creditRules, rates, classic); err != nil {
		return nil, fmt.Errorf("get default policy: %w", err)
	}
	return p, nil
}

// ListPolicies returns policies for a project.
func (s *Store) ListPolicies(projectID string) ([]types.RateLimitPolicy, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, project_id, name, is_default, credit_rules, model_credit_rates, classic_rules,
			starts_at, expires_at, created_at, updated_at
		FROM rate_limit_policies WHERE project_id = $1
		ORDER BY is_default DESC, name ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var policies []types.RateLimitPolicy
	for rows.Next() {
		var p types.RateLimitPolicy
		var creditRules, rates, classic []byte
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Name, &p.IsDefault,
			&creditRules, &rates, &classic,
			&p.StartsAt, &p.ExpiresAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if err := unmarshalPolicyJSON(&p, creditRules, rates, classic); err != nil {
			return nil, err
		}
		policies = append(policies, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return policies, nil
}

// UpdatePolicy updates a policy.
func (s *Store) UpdatePolicy(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("rate_limit_policies", "id", id, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// DeletePolicy deletes a policy.
func (s *Store) DeletePolicy(id string) error {
	_, err := s.pool.Exec(context.Background(), "DELETE FROM rate_limit_policies WHERE id = $1", id)
	return err
}

func unmarshalPolicyJSON(p *types.RateLimitPolicy, creditRules, rates, classic []byte) error {
	if creditRules != nil {
		if err := json.Unmarshal(creditRules, &p.CreditRules); err != nil {
			return fmt.Errorf("unmarshal credit_rules: %w", err)
		}
	}
	if rates != nil {
		if err := json.Unmarshal(rates, &p.ModelCreditRates); err != nil {
			return fmt.Errorf("unmarshal model_credit_rates: %w", err)
		}
	}
	if classic != nil {
		if err := json.Unmarshal(classic, &p.ClassicRules); err != nil {
			return fmt.Errorf("unmarshal classic_rules: %w", err)
		}
	}
	return nil
}
