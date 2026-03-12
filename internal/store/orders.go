package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// CreateOrder inserts a new order.
func (s *Store) CreateOrder(o *types.Order) error {
	return s.pool.QueryRow(context.Background(), `
		INSERT INTO orders (project_id, plan_id, order_type, periods, unit_price, amount, currency,
			status, payment_ref, payment_url, existing_subscription_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id, created_at, updated_at`,
		o.ProjectID, o.PlanID, o.OrderType, o.Periods, o.UnitPrice, o.Amount, o.Currency,
		o.Status, o.PaymentRef, o.PaymentURL, nullString(o.ExistingSubscriptionID), o.Metadata,
	).Scan(&o.ID, &o.CreatedAt, &o.UpdatedAt)
}

// GetOrderByID returns an order by ID.
func (s *Store) GetOrderByID(id string) (*types.Order, error) {
	o := &types.Order{}
	var existSubID *string
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, project_id, plan_id, order_type, periods, unit_price, amount, currency,
			status, payment_ref, payment_url, existing_subscription_id, metadata,
			created_at, updated_at
		FROM orders WHERE id = $1`, id,
	).Scan(&o.ID, &o.ProjectID, &o.PlanID, &o.OrderType, &o.Periods, &o.UnitPrice, &o.Amount,
		&o.Currency, &o.Status, &o.PaymentRef, &o.PaymentURL, &existSubID, &o.Metadata,
		&o.CreatedAt, &o.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}
	if existSubID != nil {
		o.ExistingSubscriptionID = *existSubID
	}
	return o, nil
}

// ListOrdersByProject returns orders for a project with pagination.
func (s *Store) ListOrdersByProject(projectID string, p types.PaginationParams) ([]types.Order, int, error) {
	ctx := context.Background()
	var total int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM orders WHERE project_id = $1", projectID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count orders: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, plan_id, order_type, periods, unit_price, amount, currency,
			status, payment_ref, payment_url, existing_subscription_id, metadata,
			created_at, updated_at
		FROM orders WHERE project_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`, projectID, p.Limit(), p.Offset())
	if err != nil {
		return nil, 0, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()

	var orders []types.Order
	for rows.Next() {
		var o types.Order
		var existSubID *string
		if err := rows.Scan(&o.ID, &o.ProjectID, &o.PlanID, &o.OrderType, &o.Periods,
			&o.UnitPrice, &o.Amount, &o.Currency, &o.Status, &o.PaymentRef, &o.PaymentURL,
			&existSubID, &o.Metadata, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan order: %w", err)
		}
		if existSubID != nil {
			o.ExistingSubscriptionID = *existSubID
		}
		orders = append(orders, o)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate orders: %w", err)
	}
	return orders, total, nil
}

// HasPayingOrder returns true if the project has an order in "paying" status.
func (s *Store) HasPayingOrder(projectID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(context.Background(),
		"SELECT EXISTS(SELECT 1 FROM orders WHERE project_id = $1 AND status = 'paying')",
		projectID,
	).Scan(&exists)
	return exists, err
}

// CancelOrder sets order status to "cancelled" if it is currently "pending" or "paying".
func (s *Store) CancelOrder(id string) (bool, error) {
	res, err := s.pool.Exec(context.Background(), `
		UPDATE orders SET status = 'cancelled', updated_at = NOW()
		WHERE id = $1 AND status IN ('pending', 'paying')`, id)
	if err != nil {
		return false, err
	}
	return res.RowsAffected() > 0, nil
}

// UpdateOrderStatus updates the status of an order.
func (s *Store) UpdateOrderStatus(id, status string) error {
	_, err := s.pool.Exec(context.Background(), `UPDATE orders SET status = $1, updated_at = NOW() WHERE id = $2`, status, id)
	return err
}

// UpdateOrderPayment updates payment details and status on an order.
func (s *Store) UpdateOrderPayment(id, paymentRef, paymentURL, status string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE orders SET payment_ref = $1, payment_url = $2, status = $3, updated_at = NOW()
		WHERE id = $4`, paymentRef, paymentURL, status, id)
	return err
}
