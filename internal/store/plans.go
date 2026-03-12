package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// CreatePlan inserts a new plan.
func (s *Store) CreatePlan(p *types.Plan) error {
	creditRulesJSON, _ := marshalJSON(p.CreditRules)
	ratesJSON, _ := marshalJSON(p.ModelCreditRates)
	classicJSON, _ := marshalJSON(p.ClassicRules)

	return s.pool.QueryRow(context.Background(), `
		INSERT INTO plans (name, slug, display_name, description, tier_level, group_tag,
			price_per_period, period_months, credit_rules, model_credit_rates, classic_rules, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, created_at, updated_at`,
		p.Name, p.Slug, p.DisplayName, p.Description, p.TierLevel, p.GroupTag,
		p.PricePerPeriod, p.PeriodMonths, creditRulesJSON, ratesJSON, classicJSON, p.IsActive,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

// GetPlanByID returns a plan by ID.
func (s *Store) GetPlanByID(id string) (*types.Plan, error) {
	p := &types.Plan{}
	var creditRules, rates, classic []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, name, slug, display_name, description, tier_level, group_tag,
			price_per_period, period_months, credit_rules, model_credit_rates, classic_rules,
			is_active, created_at, updated_at
		FROM plans WHERE id = $1`, id,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.DisplayName, &p.Description, &p.TierLevel, &p.GroupTag,
		&p.PricePerPeriod, &p.PeriodMonths, &creditRules, &rates, &classic,
		&p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get plan: %w", err)
	}
	if err := unmarshalPlanJSON(p, creditRules, rates, classic); err != nil {
		return nil, fmt.Errorf("get plan: %w", err)
	}
	return p, nil
}

// GetPlanBySlug returns a plan by slug.
func (s *Store) GetPlanBySlug(slug string) (*types.Plan, error) {
	p := &types.Plan{}
	var creditRules, rates, classic []byte
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, name, slug, display_name, description, tier_level, group_tag,
			price_per_period, period_months, credit_rules, model_credit_rates, classic_rules,
			is_active, created_at, updated_at
		FROM plans WHERE slug = $1`, slug,
	).Scan(&p.ID, &p.Name, &p.Slug, &p.DisplayName, &p.Description, &p.TierLevel, &p.GroupTag,
		&p.PricePerPeriod, &p.PeriodMonths, &creditRules, &rates, &classic,
		&p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get plan by slug: %w", err)
	}
	if err := unmarshalPlanJSON(p, creditRules, rates, classic); err != nil {
		return nil, fmt.Errorf("get plan by slug: %w", err)
	}
	return p, nil
}

// ListPlans returns all plans, optionally filtered to active-only.
func (s *Store) ListPlans(activeOnly bool) ([]types.Plan, error) {
	query := `
		SELECT id, name, slug, display_name, description, tier_level, group_tag,
			price_per_period, period_months, credit_rules, model_credit_rates, classic_rules,
			is_active, created_at, updated_at
		FROM plans`
	if activeOnly {
		query += ` WHERE is_active = TRUE`
	}
	query += ` ORDER BY tier_level ASC, name ASC`

	rows, err := s.pool.Query(context.Background(), query)
	if err != nil {
		return nil, fmt.Errorf("list plans: %w", err)
	}
	defer rows.Close()
	return scanPlans(rows)
}

// ListPlansForProject returns active plans matching the project's billing_tags.
func (s *Store) ListPlansForProject(projectID string) ([]types.Plan, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT pl.id, pl.name, pl.slug, pl.display_name, pl.description, pl.tier_level, pl.group_tag,
			pl.price_per_period, pl.period_months, pl.credit_rules, pl.model_credit_rates, pl.classic_rules,
			pl.is_active, pl.created_at, pl.updated_at
		FROM plans pl
		JOIN projects pr ON pr.id = $1
		WHERE pl.is_active = TRUE
			AND (pl.group_tag = '' OR pl.group_tag = ANY(pr.billing_tags))
		ORDER BY pl.tier_level ASC, pl.name ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list plans for project: %w", err)
	}
	defer rows.Close()
	return scanPlans(rows)
}

// UpdatePlan updates plan fields.
func (s *Store) UpdatePlan(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	query, args := buildUpdateQuery("plans", "id", id, updates)
	_, err := s.pool.Exec(context.Background(), query, args...)
	return err
}

// DeletePlan soft-deletes a plan by setting is_active to FALSE.
func (s *Store) DeletePlan(id string) error {
	_, err := s.pool.Exec(context.Background(), `UPDATE plans SET is_active = FALSE, updated_at = NOW() WHERE id = $1`, id)
	return err
}

func scanPlans(rows pgx.Rows) ([]types.Plan, error) {
	var plans []types.Plan
	for rows.Next() {
		var p types.Plan
		var creditRules, rates, classic []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.Slug, &p.DisplayName, &p.Description,
			&p.TierLevel, &p.GroupTag, &p.PricePerPeriod, &p.PeriodMonths,
			&creditRules, &rates, &classic,
			&p.IsActive, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan plan: %w", err)
		}
		if err := unmarshalPlanJSON(&p, creditRules, rates, classic); err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plans: %w", err)
	}
	return plans, nil
}

func unmarshalPlanJSON(p *types.Plan, creditRules, rates, classic []byte) error {
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
