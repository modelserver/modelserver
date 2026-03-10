package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

// CreateSubscription inserts a new subscription.
func (s *Store) CreateSubscription(sub *types.Subscription) error {
	return s.db.QueryRow(`
		INSERT INTO subscriptions (project_id, plan_name, policy_id, status, starts_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`,
		sub.ProjectID, sub.PlanName, sub.PolicyID, sub.Status, sub.StartsAt, sub.ExpiresAt,
	).Scan(&sub.ID, &sub.CreatedAt, &sub.UpdatedAt)
}

// GetActiveSubscription returns the active subscription for a project.
func (s *Store) GetActiveSubscription(projectID string) (*types.Subscription, error) {
	sub := &types.Subscription{}
	err := s.db.QueryRow(`
		SELECT id, project_id, plan_name, policy_id, status, starts_at, expires_at, created_at, updated_at
		FROM subscriptions
		WHERE project_id = $1 AND status = 'active' AND starts_at <= NOW() AND expires_at >= NOW()
		ORDER BY created_at DESC LIMIT 1`, projectID,
	).Scan(&sub.ID, &sub.ProjectID, &sub.PlanName, &sub.PolicyID, &sub.Status,
		&sub.StartsAt, &sub.ExpiresAt, &sub.CreatedAt, &sub.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active subscription: %w", err)
	}
	return sub, nil
}

// GetSubscriptionByID returns a subscription by ID.
func (s *Store) GetSubscriptionByID(id string) (*types.Subscription, error) {
	sub := &types.Subscription{}
	err := s.db.QueryRow(`
		SELECT id, project_id, plan_name, policy_id, status, starts_at, expires_at, created_at, updated_at
		FROM subscriptions WHERE id = $1`, id,
	).Scan(&sub.ID, &sub.ProjectID, &sub.PlanName, &sub.PolicyID, &sub.Status,
		&sub.StartsAt, &sub.ExpiresAt, &sub.CreatedAt, &sub.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	return sub, nil
}

// ListSubscriptions returns all subscriptions for a project.
func (s *Store) ListSubscriptions(projectID string) ([]types.Subscription, error) {
	rows, err := s.db.Query(`
		SELECT id, project_id, plan_name, policy_id, status, starts_at, expires_at, created_at, updated_at
		FROM subscriptions WHERE project_id = $1
		ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []types.Subscription
	for rows.Next() {
		var sub types.Subscription
		if err := rows.Scan(&sub.ID, &sub.ProjectID, &sub.PlanName, &sub.PolicyID, &sub.Status,
			&sub.StartsAt, &sub.ExpiresAt, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, err
		}
		subs = append(subs, sub)
	}
	return subs, nil
}

// UpdateSubscriptionStatus updates a subscription's status.
func (s *Store) UpdateSubscriptionStatus(id, status string) error {
	_, err := s.db.Exec(`
		UPDATE subscriptions SET status = $1, updated_at = NOW()
		WHERE id = $2`, status, id)
	return err
}

// ExpireSubscriptions marks all expired active subscriptions as expired.
func (s *Store) ExpireSubscriptions() (int64, error) {
	result, err := s.db.Exec(`
		UPDATE subscriptions SET status = 'expired', updated_at = NOW()
		WHERE status = 'active' AND expires_at < NOW()`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// CreateSubscriptionFromPlan creates a policy from a predefined plan and a subscription linking them.
func (s *Store) CreateSubscriptionFromPlan(projectID, planName string, plan types.PredefinedPlan, startsAt, expiresAt time.Time) (*types.Subscription, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}

	// Create the policy from the plan template.
	policy := &types.RateLimitPolicy{
		ProjectID:        projectID,
		Name:             plan.Name,
		CreditRules:      plan.CreditRules,
		ModelCreditRates: plan.ModelCreditRates,
		ClassicRules:     plan.ClassicRules,
		StartsAt:         &startsAt,
		ExpiresAt:        &expiresAt,
	}

	creditRulesJSON, _ := marshalJSON(policy.CreditRules)
	ratesJSON, _ := marshalJSON(policy.ModelCreditRates)
	classicJSON, _ := marshalJSON(policy.ClassicRules)

	err = tx.QueryRow(`
		INSERT INTO rate_limit_policies (project_id, name, is_default, credit_rules, model_credit_rates, classic_rules, starts_at, expires_at)
		VALUES ($1, $2, FALSE, $3, $4, $5, $6, $7)
		RETURNING id, created_at, updated_at`,
		policy.ProjectID, policy.Name, creditRulesJSON, ratesJSON, classicJSON,
		policy.StartsAt, policy.ExpiresAt,
	).Scan(&policy.ID, &policy.CreatedAt, &policy.UpdatedAt)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("create policy: %w", err)
	}

	// Create the subscription.
	sub := &types.Subscription{
		ProjectID: projectID,
		PlanName:  planName,
		PolicyID:  policy.ID,
		Status:    types.SubscriptionStatusActive,
		StartsAt:  startsAt,
		ExpiresAt: expiresAt,
	}

	err = tx.QueryRow(`
		INSERT INTO subscriptions (project_id, plan_name, policy_id, status, starts_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`,
		sub.ProjectID, sub.PlanName, sub.PolicyID, sub.Status, sub.StartsAt, sub.ExpiresAt,
	).Scan(&sub.ID, &sub.CreatedAt, &sub.UpdatedAt)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("create subscription: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return sub, nil
}

func marshalJSON(v interface{}) ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	return json.Marshal(v)
}
