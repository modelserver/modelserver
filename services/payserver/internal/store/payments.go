package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Payment struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id"`
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
		INSERT INTO payments (tenant_id, order_id, channel, trade_no, payment_url, amount, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (order_id) DO NOTHING
		RETURNING id, callback_status, callback_retries, created_at, updated_at`,
		p.TenantID, p.OrderID, p.Channel, p.TradeNo, p.PaymentURL, p.Amount, p.Status,
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
		SELECT id, tenant_id, order_id, channel, trade_no, payment_url, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments WHERE order_id = $1`, orderID,
	).Scan(&p.ID, &p.TenantID, &p.OrderID, &p.Channel, &p.TradeNo, &p.PaymentURL, &p.Amount, &p.Status,
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
		SELECT id, tenant_id, order_id, channel, trade_no, payment_url, amount, status,
			callback_status, callback_retries, raw_notify, paid_at,
			created_at, updated_at
		FROM payments WHERE id = $1`, id,
	).Scan(&p.ID, &p.TenantID, &p.OrderID, &p.Channel, &p.TradeNo, &p.PaymentURL, &p.Amount, &p.Status,
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
// The tenantID guard prevents a tenant-A caller from mutating a tenant-B
// payment if order_id collisions ever arose (defense in depth — the
// payments.order_id UNIQUE constraint makes collisions impossible today,
// but the guard is load-bearing if the schema ever changes).
func (s *Store) MarkPaymentPaid(tenantID, orderID, tradeNo, rawNotify string, paidAt time.Time) (bool, error) {
	result, err := s.pool.Exec(context.Background(), `
		UPDATE payments
		SET status = 'paid', trade_no = $1, raw_notify = $2, paid_at = $3, updated_at = NOW()
		WHERE order_id = $4 AND tenant_id = $5 AND status = 'pending'`,
		tradeNo, rawNotify, paidAt, orderID, tenantID)
	if err != nil {
		return false, err
	}
	return result.RowsAffected() > 0, nil
}

func (s *Store) MarkCallbackSuccess(tenantID, orderID string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE payments SET callback_status = 'success', updated_at = NOW()
		WHERE order_id = $1 AND tenant_id = $2`, orderID, tenantID)
	return err
}

func (s *Store) IncrCallbackRetries(tenantID, orderID string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE payments SET callback_retries = callback_retries + 1, updated_at = NOW()
		WHERE order_id = $1 AND tenant_id = $2`, orderID, tenantID)
	return err
}

func (s *Store) MarkCallbackFailed(tenantID, orderID string) error {
	_, err := s.pool.Exec(context.Background(), `
		UPDATE payments SET callback_status = 'failed', updated_at = NOW()
		WHERE order_id = $1 AND tenant_id = $2`, orderID, tenantID)
	return err
}

// PaymentFilters holds optional filter criteria for ListPayments.
type PaymentFilters struct {
	TenantID string
	Status   string
	Channel  string
}

// ListPayments returns a paginated list of payments matching the given filters.
// All filter values are bound as query parameters (no string interpolation of user data).
func (s *Store) ListPayments(limit, offset int, f PaymentFilters) ([]Payment, int, error) {
	ctx := context.Background()
	where := "WHERE 1=1"
	args := []any{}
	i := 1
	if f.TenantID != "" {
		where += fmt.Sprintf(" AND tenant_id = $%d", i)
		args = append(args, f.TenantID)
		i++
	}
	if f.Status != "" {
		where += fmt.Sprintf(" AND status = $%d", i)
		args = append(args, f.Status)
		i++
	}
	if f.Channel != "" {
		where += fmt.Sprintf(" AND channel = $%d", i)
		args = append(args, f.Channel)
		i++
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM payments `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count payments: %w", err)
	}

	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := s.pool.Query(ctx,
		`SELECT id, tenant_id, order_id, channel, trade_no, payment_url, amount, status,
			callback_status, callback_retries, raw_notify, paid_at, created_at, updated_at
		FROM payments `+where+
			fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, i, i+1),
		queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list payments: %w", err)
	}
	defer rows.Close()

	var out []Payment
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.TenantID, &p.OrderID, &p.Channel, &p.TradeNo,
			&p.PaymentURL, &p.Amount, &p.Status, &p.CallbackStatus, &p.CallbackRetries,
			&p.RawNotify, &p.PaidAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan payment: %w", err)
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// ListPendingCallbacks returns paid payments with pending callbacks, using FOR UPDATE SKIP LOCKED
// to prevent concurrent workers from processing the same rows.
func (s *Store) ListPendingCallbacks(limit int, maxRetries int) ([]Payment, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT id, tenant_id, order_id, channel, trade_no, payment_url, amount, status,
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
		if err := rows.Scan(&p.ID, &p.TenantID, &p.OrderID, &p.Channel, &p.TradeNo, &p.PaymentURL, &p.Amount, &p.Status,
			&p.CallbackStatus, &p.CallbackRetries, &p.RawNotify, &p.PaidAt,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan payment: %w", err)
		}
		payments = append(payments, p)
	}
	return payments, rows.Err()
}
