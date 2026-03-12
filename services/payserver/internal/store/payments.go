package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Payment struct {
	ID              string     `json:"id"`
	OrderID         string     `json:"order_id"`
	Channel         string     `json:"channel"`
	TradeNo         string     `json:"trade_no"`
	PaymentURL      string     `json:"payment_url"`
	Amount          int64      `json:"amount"`
	Status          string     `json:"status"`
	CallbackStatus  string     `json:"callback_status"`
	CallbackRetries int        `json:"callback_retries"`
	RawNotify       *string    `json:"raw_notify,omitempty"`
	PaidAt          *time.Time `json:"paid_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// InsertOrGetPayment atomically inserts a payment record or returns the existing one.
// Returns (payment, created, error). If the order_id already exists, returns the existing
// record with created=false. This prevents TOCTOU races on concurrent requests.
func (s *Store) InsertOrGetPayment(p *Payment) (bool, error) {
	ctx := context.Background()
	// Try insert first. ON CONFLICT returns nothing, so we detect via RETURNING.
	err := s.pool.QueryRow(ctx, `
		INSERT INTO payments (order_id, channel, trade_no, payment_url, amount, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (order_id) DO NOTHING
		RETURNING id, callback_status, callback_retries, created_at, updated_at`,
		p.OrderID, p.Channel, p.TradeNo, p.PaymentURL, p.Amount, p.Status,
	).Scan(&p.ID, &p.CallbackStatus, &p.CallbackRetries, &p.CreatedAt, &p.UpdatedAt)
	if err == nil {
		return true, nil // inserted
	}
	if err != pgx.ErrNoRows {
		return false, fmt.Errorf("insert payment: %w", err)
	}
	// Conflict: fetch existing record.
	existing, err := s.GetPaymentByOrderID(p.OrderID)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, fmt.Errorf("payment disappeared after conflict on order_id %s", p.OrderID)
	}
	*p = *existing
	return false, nil
}

// UpdatePaymentGatewayResult updates the gateway result (trade_no, payment_url) for a pending payment.
func (s *Store) UpdatePaymentGatewayResult(id string, tradeNo, paymentURL string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE payments SET trade_no = $1, payment_url = $2, updated_at = NOW()
		WHERE id = $3 AND status = 'pending'`,
		tradeNo, paymentURL, id)
	return err
}

func (s *Store) GetPaymentByOrderID(orderID string) (*Payment, error) {
	p := &Payment{}
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, order_id, channel, trade_no, payment_url, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments WHERE order_id = $1`, orderID,
	).Scan(&p.ID, &p.OrderID, &p.Channel, &p.TradeNo, &p.PaymentURL, &p.Amount, &p.Status,
		&p.CallbackStatus, &p.CallbackRetries, &p.RawNotify, &p.PaidAt,
		&p.CreatedAt, &p.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get payment by order_id: %w", err)
	}
	return p, nil
}

func (s *Store) GetPaymentByID(id string) (*Payment, error) {
	p := &Payment{}
	err := s.pool.QueryRow(context.Background(), `
		SELECT id, order_id, channel, trade_no, payment_url, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments WHERE id = $1`, id,
	).Scan(&p.ID, &p.OrderID, &p.Channel, &p.TradeNo, &p.PaymentURL, &p.Amount, &p.Status,
		&p.CallbackStatus, &p.CallbackRetries, &p.RawNotify, &p.PaidAt,
		&p.CreatedAt, &p.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get payment by id: %w", err)
	}
	return p, nil
}

// MarkPaymentPaid atomically marks a pending payment as paid.
// Returns true if the row was actually updated (i.e., it was pending).
func (s *Store) MarkPaymentPaid(orderID string, tradeNo string, rawNotify string, paidAt time.Time) (bool, error) {
	result, err := s.pool.Exec(context.Background(), `
		UPDATE payments
		SET status = 'paid', trade_no = $1, raw_notify = $2, paid_at = $3, updated_at = NOW()
		WHERE order_id = $4 AND status = 'pending'`,
		tradeNo, rawNotify, paidAt, orderID)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

func (s *Store) MarkCallbackSuccess(orderID string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE payments SET callback_status = 'success', updated_at = NOW()
		WHERE order_id = $1`, orderID)
	return err
}

func (s *Store) IncrCallbackRetries(orderID string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE payments SET callback_retries = callback_retries + 1, updated_at = NOW()
		WHERE order_id = $1`, orderID)
	return err
}

func (s *Store) MarkCallbackFailed(orderID string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE payments SET callback_status = 'failed', updated_at = NOW()
		WHERE order_id = $1`, orderID)
	return err
}

// ListPendingCallbacks returns paid payments with pending callbacks, using FOR UPDATE SKIP LOCKED
// to prevent concurrent workers from processing the same rows.
func (s *Store) ListPendingCallbacks(limit int, maxRetries int) ([]Payment, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, order_id, channel, trade_no, payment_url, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments
		WHERE status = 'paid' AND callback_status = 'pending' AND callback_retries < $2
		ORDER BY updated_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit, maxRetries)
	if err != nil {
		return nil, fmt.Errorf("list pending callbacks: %w", err)
	}
	defer rows.Close()

	var payments []Payment
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.OrderID, &p.Channel, &p.TradeNo, &p.PaymentURL, &p.Amount, &p.Status,
			&p.CallbackStatus, &p.CallbackRetries, &p.RawNotify, &p.PaidAt,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan payment: %w", err)
		}
		payments = append(payments, p)
	}
	return payments, rows.Err()
}
