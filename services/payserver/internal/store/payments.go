package store

import (
	"database/sql"
	"fmt"
	"time"
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

func (s *Store) CreatePayment(p *Payment) error {
	return s.db.QueryRow(`
		INSERT INTO payments (order_id, channel, trade_no, payment_url, amount, status)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, callback_status, callback_retries, created_at, updated_at`,
		p.OrderID, p.Channel, p.TradeNo, p.PaymentURL, p.Amount, p.Status,
	).Scan(&p.ID, &p.CallbackStatus, &p.CallbackRetries, &p.CreatedAt, &p.UpdatedAt)
}

func (s *Store) GetPaymentByOrderID(orderID string) (*Payment, error) {
	p := &Payment{}
	err := s.db.QueryRow(`
		SELECT id, order_id, channel, trade_no, payment_url, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments WHERE order_id = $1`, orderID,
	).Scan(&p.ID, &p.OrderID, &p.Channel, &p.TradeNo, &p.PaymentURL, &p.Amount, &p.Status,
		&p.CallbackStatus, &p.CallbackRetries, &p.RawNotify, &p.PaidAt,
		&p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get payment by order_id: %w", err)
	}
	return p, nil
}

func (s *Store) GetPaymentByID(id string) (*Payment, error) {
	p := &Payment{}
	err := s.db.QueryRow(`
		SELECT id, order_id, channel, trade_no, payment_url, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments WHERE id = $1`, id,
	).Scan(&p.ID, &p.OrderID, &p.Channel, &p.TradeNo, &p.PaymentURL, &p.Amount, &p.Status,
		&p.CallbackStatus, &p.CallbackRetries, &p.RawNotify, &p.PaidAt,
		&p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get payment by id: %w", err)
	}
	return p, nil
}

func (s *Store) MarkPaymentPaid(orderID string, tradeNo string, rawNotify string, paidAt time.Time) error {
	_, err := s.db.Exec(`
		UPDATE payments
		SET status = 'paid', trade_no = $1, raw_notify = $2, paid_at = $3, updated_at = NOW()
		WHERE order_id = $4 AND status = 'pending'`,
		tradeNo, rawNotify, paidAt, orderID)
	return err
}

func (s *Store) MarkCallbackSuccess(orderID string) error {
	_, err := s.db.Exec(`
		UPDATE payments SET callback_status = 'success', updated_at = NOW()
		WHERE order_id = $1`, orderID)
	return err
}

func (s *Store) IncrCallbackRetries(orderID string) error {
	_, err := s.db.Exec(`
		UPDATE payments SET callback_retries = callback_retries + 1, updated_at = NOW()
		WHERE order_id = $1`, orderID)
	return err
}

func (s *Store) MarkCallbackFailed(orderID string) error {
	_, err := s.db.Exec(`
		UPDATE payments SET callback_status = 'failed', updated_at = NOW()
		WHERE order_id = $1`, orderID)
	return err
}

func (s *Store) ListPendingCallbacks(limit int) ([]Payment, error) {
	rows, err := s.db.Query(`
		SELECT id, order_id, channel, trade_no, payment_url, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments
		WHERE status = 'paid' AND callback_status = 'pending' AND callback_retries < 10
		ORDER BY updated_at ASC
		LIMIT $1`, limit)
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
