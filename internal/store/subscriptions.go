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
		INSERT INTO subscriptions (project_id, plan_id, plan_name, status, starts_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, updated_at`,
		sub.ProjectID, nullString(sub.PlanID), sub.PlanName, sub.Status, sub.StartsAt, sub.ExpiresAt,
	).Scan(&sub.ID, &sub.CreatedAt, &sub.UpdatedAt)
}

// GetActiveSubscription returns the active subscription for a project.
func (s *Store) GetActiveSubscription(projectID string) (*types.Subscription, error) {
	sub := &types.Subscription{}
	var planID sql.NullString
	err := s.db.QueryRow(`
		SELECT id, project_id, COALESCE(plan_id::text, ''), plan_name, status, starts_at, expires_at, created_at, updated_at
		FROM subscriptions
		WHERE project_id = $1 AND status = 'active' AND starts_at <= NOW() AND expires_at >= NOW()
		ORDER BY created_at DESC LIMIT 1`, projectID,
	).Scan(&sub.ID, &sub.ProjectID, &planID, &sub.PlanName, &sub.Status,
		&sub.StartsAt, &sub.ExpiresAt, &sub.CreatedAt, &sub.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active subscription: %w", err)
	}
	if planID.Valid {
		sub.PlanID = planID.String
	}
	return sub, nil
}

// GetSubscriptionByID returns a subscription by ID.
func (s *Store) GetSubscriptionByID(id string) (*types.Subscription, error) {
	sub := &types.Subscription{}
	var planID sql.NullString
	err := s.db.QueryRow(`
		SELECT id, project_id, COALESCE(plan_id::text, ''), plan_name, status, starts_at, expires_at, created_at, updated_at
		FROM subscriptions WHERE id = $1`, id,
	).Scan(&sub.ID, &sub.ProjectID, &planID, &sub.PlanName, &sub.Status,
		&sub.StartsAt, &sub.ExpiresAt, &sub.CreatedAt, &sub.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	if planID.Valid {
		sub.PlanID = planID.String
	}
	return sub, nil
}

// ListSubscriptions returns all subscriptions for a project.
func (s *Store) ListSubscriptions(projectID string) ([]types.Subscription, error) {
	rows, err := s.db.Query(`
		SELECT id, project_id, COALESCE(plan_id::text, ''), plan_name, status, starts_at, expires_at, created_at, updated_at
		FROM subscriptions WHERE project_id = $1
		ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []types.Subscription
	for rows.Next() {
		var sub types.Subscription
		var planID sql.NullString
		if err := rows.Scan(&sub.ID, &sub.ProjectID, &planID, &sub.PlanName, &sub.Status,
			&sub.StartsAt, &sub.ExpiresAt, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, err
		}
		if planID.Valid {
			sub.PlanID = planID.String
		}
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, err
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

// ExpireAndFallbackToFree marks expired paid subscriptions as expired and
// creates a new free-tier subscription for each affected project.
func (s *Store) ExpireAndFallbackToFree() (int64, error) {
	// Fetch the free plan once.
	freePlan, err := s.GetPlanBySlug("free")
	if err != nil {
		return 0, fmt.Errorf("look up free plan: %w", err)
	}
	if freePlan == nil || !freePlan.IsActive {
		// No free plan configured — just expire without fallback.
		result, err := s.db.Exec(`
			UPDATE subscriptions SET status = 'expired', updated_at = NOW()
			WHERE status = 'active' AND plan_name != 'free' AND expires_at < NOW()`)
		if err != nil {
			return 0, err
		}
		return result.RowsAffected()
	}

	// Find expired paid subscriptions.
	rows, err := s.db.Query(`
		SELECT id, project_id FROM subscriptions
		WHERE status = 'active' AND plan_name != 'free' AND expires_at < NOW()`)
	if err != nil {
		return 0, fmt.Errorf("query expired subscriptions: %w", err)
	}
	defer rows.Close()

	type expired struct {
		id        string
		projectID string
	}
	var items []expired
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.id, &e.projectID); err != nil {
			return 0, err
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	now := time.Now()
	freeExpiry := now.AddDate(100, 0, 0)
	var count int64

	for _, e := range items {
		tx, err := s.db.Begin()
		if err != nil {
			return count, fmt.Errorf("begin tx: %w", err)
		}
		// Mark as expired.
		if _, err := tx.Exec(`UPDATE subscriptions SET status = 'expired', updated_at = NOW() WHERE id = $1`, e.id); err != nil {
			tx.Rollback()
			return count, fmt.Errorf("expire subscription %s: %w", e.id, err)
		}
		// Create free plan subscription.
		if _, err := tx.Exec(`
			INSERT INTO subscriptions (project_id, plan_id, plan_name, status, starts_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			e.projectID, freePlan.ID, freePlan.Slug, "active", now, freeExpiry,
		); err != nil {
			tx.Rollback()
			return count, fmt.Errorf("create free fallback for project %s: %w", e.projectID, err)
		}
		if err := tx.Commit(); err != nil {
			return count, fmt.Errorf("commit fallback for project %s: %w", e.projectID, err)
		}
		count++
	}
	return count, nil
}

// CreateSubscriptionFromPlan creates a subscription linking a project to a plan.
// Rate limits are resolved from the plan at runtime; no policy snapshot is created.
func (s *Store) CreateSubscriptionFromPlan(projectID string, plan *types.Plan, startsAt, expiresAt time.Time) (*types.Subscription, error) {
	sub := &types.Subscription{
		ProjectID: projectID,
		PlanID:    plan.ID,
		PlanName:  plan.Slug,
		Status:    types.SubscriptionStatusActive,
		StartsAt:  startsAt,
		ExpiresAt: expiresAt,
	}
	if err := s.CreateSubscription(sub); err != nil {
		return nil, fmt.Errorf("create subscription from plan: %w", err)
	}
	return sub, nil
}

// RenewSubscription extends a subscription's expires_at by additionalMonths.
func (s *Store) RenewSubscription(subID string, additionalMonths int) error {
	_, err := s.db.Exec(`
		UPDATE subscriptions SET expires_at = expires_at + ($1 || ' months')::interval, updated_at = NOW()
		WHERE id = $2`, additionalMonths, subID)
	return err
}

// DeliverOrder processes a paid order within a transaction, creating or updating subscriptions as needed.
func (s *Store) DeliverOrder(orderID string, plan *types.Plan, project *types.Project) (*types.Subscription, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}

	// Fetch the order inside the tx.
	var order types.Order
	var existSubID sql.NullString
	err = tx.QueryRow(`
		SELECT id, project_id, plan_id, order_type, periods, unit_price, amount, currency,
			status, payment_ref, payment_url, existing_subscription_id, metadata, created_at, updated_at
		FROM orders WHERE id = $1 FOR UPDATE`, orderID,
	).Scan(&order.ID, &order.ProjectID, &order.PlanID, &order.OrderType, &order.Periods,
		&order.UnitPrice, &order.Amount, &order.Currency, &order.Status, &order.PaymentRef,
		&order.PaymentURL, &existSubID, &order.Metadata, &order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("fetch order: %w", err)
	}
	if existSubID.Valid {
		order.ExistingSubscriptionID = existSubID.String
	}

	if order.Status == types.OrderStatusDelivered {
		// Already delivered (concurrent webhook retry). Return existing subscription.
		tx.Rollback()
		if order.ExistingSubscriptionID != "" {
			sub, err := s.GetSubscriptionByID(order.ExistingSubscriptionID)
			if err == nil && sub != nil {
				return sub, nil
			}
		}
		// For new orders, find the subscription created by the previous delivery.
		sub, err := s.GetActiveSubscription(order.ProjectID)
		if err == nil && sub != nil {
			return sub, nil
		}
		return nil, fmt.Errorf("order already delivered but subscription not found")
	}
	if order.Status != types.OrderStatusPaying {
		tx.Rollback()
		return nil, fmt.Errorf("order status is %s, expected paying", order.Status)
	}

	now := time.Now()
	var sub *types.Subscription

	switch order.OrderType {
	case types.OrderTypeUpgrade:
		if order.ExistingSubscriptionID == "" {
			tx.Rollback()
			return nil, fmt.Errorf("upgrade order has no existing subscription ID")
		}

		// Get old subscription's expires_at and plan_name.
		var oldExpiresAt time.Time
		var oldPlanName string
		err = tx.QueryRow(`SELECT expires_at, plan_name FROM subscriptions WHERE id = $1`, order.ExistingSubscriptionID).Scan(&oldExpiresAt, &oldPlanName)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("get old subscription: %w", err)
		}

		// Revoke old subscription.
		_, err = tx.Exec(`UPDATE subscriptions SET status = $1, updated_at = NOW() WHERE id = $2`,
			types.SubscriptionStatusRevoked, order.ExistingSubscriptionID)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("revoke old subscription: %w", err)
		}

		// Determine expiry: if upgrading from free plan, calculate fresh expiry from now.
		// Otherwise use old expires_at (prorated upgrade preserves remaining time).
		var newExpiresAt time.Time
		if oldPlanName == "free" {
			newExpiresAt = now.AddDate(0, plan.PeriodMonths*order.Periods, 0)
		} else {
			newExpiresAt = oldExpiresAt
		}

		sub = &types.Subscription{ProjectID: order.ProjectID, PlanID: plan.ID, PlanName: plan.Slug, Status: types.SubscriptionStatusActive, StartsAt: now, ExpiresAt: newExpiresAt}
		err = tx.QueryRow(`
			INSERT INTO subscriptions (project_id, plan_id, plan_name, status, starts_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, created_at, updated_at`,
			sub.ProjectID, sub.PlanID, sub.PlanName, sub.Status, sub.StartsAt, sub.ExpiresAt,
		).Scan(&sub.ID, &sub.CreatedAt, &sub.UpdatedAt)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("create subscription for upgrade: %w", err)
		}

	default:
		tx.Rollback()
		return nil, fmt.Errorf("unknown order type: %s", order.OrderType)
	}

	// Mark order as delivered.
	_, err = tx.Exec(`UPDATE orders SET status = $1, updated_at = NOW() WHERE id = $2`,
		types.OrderStatusDelivered, orderID)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("mark order delivered: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit delivery: %w", err)
	}
	return sub, nil
}

func marshalJSON(v interface{}) ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	return json.Marshal(v)
}
