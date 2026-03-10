package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// CreatePolicy inserts a new rate limit policy.
func (s *Store) CreatePolicy(p *types.RateLimitPolicy) error {
	creditRulesJSON, _ := json.Marshal(p.CreditRules)
	ratesJSON, _ := json.Marshal(p.ModelCreditRates)
	classicJSON, _ := json.Marshal(p.ClassicRules)

	return s.db.QueryRow(`
		INSERT INTO rate_limit_policies (project_id, name, is_default, credit_rules, model_credit_rates, classic_rules)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`,
		p.ProjectID, p.Name, p.IsDefault, creditRulesJSON, ratesJSON, classicJSON,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

// GetPolicyByID returns a policy by ID.
func (s *Store) GetPolicyByID(id string) (*types.RateLimitPolicy, error) {
	p := &types.RateLimitPolicy{}
	var creditRules, rates, classic []byte
	err := s.db.QueryRow(`
		SELECT id, project_id, name, is_default, credit_rules, model_credit_rates, classic_rules, created_at, updated_at
		FROM rate_limit_policies WHERE id = $1`, id,
	).Scan(&p.ID, &p.ProjectID, &p.Name, &p.IsDefault, &creditRules, &rates, &classic, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get policy: %w", err)
	}
	if creditRules != nil {
		json.Unmarshal(creditRules, &p.CreditRules)
	}
	if rates != nil {
		json.Unmarshal(rates, &p.ModelCreditRates)
	}
	if classic != nil {
		json.Unmarshal(classic, &p.ClassicRules)
	}
	return p, nil
}

// GetDefaultPolicy returns the default policy for a project.
func (s *Store) GetDefaultPolicy(projectID string) (*types.RateLimitPolicy, error) {
	p := &types.RateLimitPolicy{}
	var creditRules, rates, classic []byte
	err := s.db.QueryRow(`
		SELECT id, project_id, name, is_default, credit_rules, model_credit_rates, classic_rules, created_at, updated_at
		FROM rate_limit_policies WHERE project_id = $1 AND is_default = TRUE
		LIMIT 1`, projectID,
	).Scan(&p.ID, &p.ProjectID, &p.Name, &p.IsDefault, &creditRules, &rates, &classic, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get default policy: %w", err)
	}
	if creditRules != nil {
		json.Unmarshal(creditRules, &p.CreditRules)
	}
	if rates != nil {
		json.Unmarshal(rates, &p.ModelCreditRates)
	}
	if classic != nil {
		json.Unmarshal(classic, &p.ClassicRules)
	}
	return p, nil
}

// ListPolicies returns policies for a project.
func (s *Store) ListPolicies(projectID string) ([]types.RateLimitPolicy, error) {
	rows, err := s.db.Query(`
		SELECT id, project_id, name, is_default, credit_rules, model_credit_rates, classic_rules, created_at, updated_at
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
			&creditRules, &rates, &classic, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if creditRules != nil {
			json.Unmarshal(creditRules, &p.CreditRules)
		}
		if rates != nil {
			json.Unmarshal(rates, &p.ModelCreditRates)
		}
		if classic != nil {
			json.Unmarshal(classic, &p.ClassicRules)
		}
		policies = append(policies, p)
	}
	return policies, nil
}

// UpdatePolicy updates a policy.
func (s *Store) UpdatePolicy(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("rate_limit_policies", "id", id, updates)
	_, err := s.db.Exec(query, args...)
	return err
}

// DeletePolicy deletes a policy.
func (s *Store) DeletePolicy(id string) error {
	_, err := s.db.Exec("DELETE FROM rate_limit_policies WHERE id = $1", id)
	return err
}
