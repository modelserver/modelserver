package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelserver/modelserver/internal/types"
)

// Sentinel errors returned by the extra-usage store. Callers distinguish these
// from generic DB errors to decide response shape (429 vs 500).
var (
	ErrExtraUsageNotEnabled       = errors.New("extra usage not enabled")
	ErrInsufficientBalance        = errors.New("extra usage balance insufficient")
	ErrMonthlyLimitReached        = errors.New("extra usage monthly limit reached")
)

// MonthWindowStart returns the first moment of the current month in
// the process's local timezone (i.e. what the Go runtime resolved
// time.Local to at startup, controlled by the standard `TZ`
// environment variable — e.g. TZ=Asia/Shanghai).
//
// Single source of truth for the boundary used by the monthly-limit
// check in DeductExtraUsage / classifyDeductFailure /
// GetMonthlyExtraSpendCredits and by the dashboard's "Period Paid" display.
// All four read the same time.Local, so they cannot disagree.
func MonthWindowStart() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
}

// DayWindowStart is the daily analogue used by SumDailyExtraUsageTopupCredits.
// Same time.Local contract as MonthWindowStart.
func DayWindowStart() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
}

// DeductExtraUsageReq carries the input for an atomic deduction.
type DeductExtraUsageReq struct {
	ProjectID     string
	AmountCredits int64 // was AmountFen
	RequestID     string
	Reason        string
	Description   string
	// MonthWindowStart is the inclusive lower bound of the current
	// month, in the process's local timezone (TZ env var → time.Local).
	// Used by the monthly-limit check. Compute via store.MonthWindowStart().
	MonthWindowStart time.Time
}

// TopUpExtraUsageReq carries the input for a top-up.
type TopUpExtraUsageReq struct {
	ProjectID     string
	AmountCredits int64 // was AmountFen
	OrderID       string
	Reason        string
	Description   string
}

// GetExtraUsageSettings returns the settings row for a project, or nil when
// no row exists yet (project has never topped up or opened the dashboard).
func (s *Store) GetExtraUsageSettings(projectID string) (*types.ExtraUsageSettings, error) {
	row := s.pool.QueryRow(context.Background(), `
		SELECT project_id, enabled, balance_credits, monthly_limit_credits,
		       bypass_balance_check, created_at, updated_at
		FROM extra_usage_settings WHERE project_id = $1`, projectID)
	out := &types.ExtraUsageSettings{}
	err := row.Scan(&out.ProjectID, &out.Enabled, &out.BalanceCredits,
		&out.MonthlyLimitCredits, &out.BypassBalanceCheck,
		&out.CreatedAt, &out.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get extra_usage_settings: %w", err)
	}
	return out, nil
}

// UpsertExtraUsageSettings creates or updates the user-controllable fields.
// `balance_credits` is intentionally NOT writable here — it is mutated only by
// DeductExtraUsage / TopUpExtraUsage / admin adjust paths. Callers wanting
// to toggle enabled or set a monthly limit go through this function.
func (s *Store) UpsertExtraUsageSettings(projectID string, enabled bool, monthlyLimitCredits int64) (*types.ExtraUsageSettings, error) {
	row := s.pool.QueryRow(context.Background(), `
		INSERT INTO extra_usage_settings (project_id, enabled, monthly_limit_credits)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id) DO UPDATE
			SET enabled               = EXCLUDED.enabled,
			    monthly_limit_credits = EXCLUDED.monthly_limit_credits,
			    updated_at            = NOW()
		RETURNING project_id, enabled, balance_credits, monthly_limit_credits,
		          bypass_balance_check, created_at, updated_at`,
		projectID, enabled, monthlyLimitCredits)
	out := &types.ExtraUsageSettings{}
	err := row.Scan(&out.ProjectID, &out.Enabled, &out.BalanceCredits,
		&out.MonthlyLimitCredits, &out.BypassBalanceCheck,
		&out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("upsert extra_usage_settings: %w", err)
	}
	return out, nil
}

// SetExtraUsageBypass flips the bypass_balance_check flag for a project.
// Used by superadmins to keep a project served even when its balance has
// been depleted or `enabled` is false. Creates the settings row if none
// exists so the flag can be set on a project that has never topped up.
func (s *Store) SetExtraUsageBypass(projectID string, bypass bool) (*types.ExtraUsageSettings, error) {
	row := s.pool.QueryRow(context.Background(), `
		INSERT INTO extra_usage_settings (project_id, bypass_balance_check)
		VALUES ($1, $2)
		ON CONFLICT (project_id) DO UPDATE
			SET bypass_balance_check = EXCLUDED.bypass_balance_check,
			    updated_at           = NOW()
		RETURNING project_id, enabled, balance_credits, monthly_limit_credits,
		          bypass_balance_check, created_at, updated_at`,
		projectID, bypass)
	out := &types.ExtraUsageSettings{}
	err := row.Scan(&out.ProjectID, &out.Enabled, &out.BalanceCredits,
		&out.MonthlyLimitCredits, &out.BypassBalanceCheck,
		&out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("set extra_usage bypass: %w", err)
	}
	return out, nil
}

// DeductExtraUsage atomically checks eligibility (enabled + balance + monthly
// limit) and debits the balance, writing one ledger row inside the same
// transaction. The conditional UPDATE returns zero rows when any check fails;
// we then classify the failure by re-reading the settings row.
//
// The month window is computed Asia/Shanghai-anchored and reconverted to
// timestamptz so PG session TZ does not move the boundary.
func (s *Store) DeductExtraUsage(req DeductExtraUsageReq) (int64, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var newBalance int64
	err = tx.QueryRow(ctx, `
		WITH month_spend AS (
			SELECT COALESCE(SUM(-amount_credits), 0)::bigint AS spent
			FROM extra_usage_transactions
			WHERE project_id = $1
			  AND type = 'deduction'
			  AND created_at >= $3
		)
		UPDATE extra_usage_settings s
		   SET balance_credits = balance_credits - $2, updated_at = NOW()
		  FROM month_spend
		 WHERE s.project_id = $1
		   AND (s.bypass_balance_check = TRUE OR s.enabled = TRUE)
		   AND (s.bypass_balance_check = TRUE OR s.balance_credits >= $2)
		   AND (s.monthly_limit_credits = 0 OR month_spend.spent + $2 <= s.monthly_limit_credits)
		RETURNING s.balance_credits`,
		req.ProjectID, req.AmountCredits, req.MonthWindowStart,
	).Scan(&newBalance)

	if err == pgx.ErrNoRows {
		// Distinguish enabled=false / balance too low / monthly limit hit.
		classifyErr := classifyDeductFailure(ctx, tx, req.ProjectID, req.AmountCredits, req.MonthWindowStart)
		return 0, classifyErr
	}
	if err != nil {
		return 0, fmt.Errorf("deduct extra_usage: %w", err)
	}

	// Negate in Go rather than via SQL `-$2`. With pgx's extended query
	// protocol, the unary minus on an untyped parameter triggers
	// SQLSTATE 42725 (operator is not unique: - unknown) because
	// Postgres can't resolve which `-` operator (int2/int4/int8/numeric/
	// money/interval/…) to use before knowing the column type. Passing
	// the negated int64 directly bypasses operator resolution entirely.
	_, err = tx.Exec(ctx, `
		INSERT INTO extra_usage_transactions
		  (project_id, type, amount_credits, balance_after_credits, request_id, reason, description)
		VALUES ($1, 'deduction', $2, $3, $4, $5, $6)`,
		req.ProjectID, -req.AmountCredits, newBalance,
		nullString(req.RequestID), req.Reason, req.Description,
	)
	if err != nil {
		return 0, fmt.Errorf("insert ledger row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit tx: %w", err)
	}
	return newBalance, nil
}

// classifyDeductFailure reads the settings row inside the same transaction to
// produce a specific sentinel error instead of a generic "not updated" signal.
// The re-query is tolerant: if any step errors, we fall back to the balance
// error (the common case) rather than leak a confusing low-level error.
func classifyDeductFailure(ctx context.Context, tx pgx.Tx, projectID string, amount int64, monthStart time.Time) error {
	var enabled, bypass bool
	var balance, monthlyLimit int64
	err := tx.QueryRow(ctx, `
		SELECT enabled, balance_credits, monthly_limit_credits, bypass_balance_check
		FROM extra_usage_settings
		WHERE project_id = $1`, projectID,
	).Scan(&enabled, &balance, &monthlyLimit, &bypass)
	if err != nil {
		return ErrExtraUsageNotEnabled
	}
	if !bypass && !enabled {
		return ErrExtraUsageNotEnabled
	}
	if !bypass && balance < amount {
		return ErrInsufficientBalance
	}
	if monthlyLimit > 0 {
		var spent int64
		// Capture the err — silently discarding it (the previous
		// behavior with `_ = …`) made monthly-limit hits look like
		// insufficient-balance to the caller when this query failed
		// for unrelated reasons (lock timeout, etc.). Wrap and return.
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(-amount_credits), 0)::bigint
			FROM extra_usage_transactions
			WHERE project_id = $1
			  AND type = 'deduction'
			  AND created_at >= $2`,
			projectID, monthStart,
		).Scan(&spent); err != nil {
			return fmt.Errorf("classify deduct failure: month spend lookup: %w", err)
		}
		if spent+amount > monthlyLimit {
			return ErrMonthlyLimitReached
		}
	}
	return ErrInsufficientBalance
}

// TopUpExtraUsage applies a credit (positive amount) and records the ledger
// row. If no settings row exists, one is auto-created with enabled=false; the
// unique (order_id) partial index + ON CONFLICT make the call idempotent so
// duplicate webhook deliveries yield the current balance.
func (s *Store) TopUpExtraUsage(req TopUpExtraUsageReq) (int64, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Idempotency fast path: if this order already has a top-up row, return
	// the current balance without re-applying.
	if req.OrderID != "" {
		var existing int64
		err := tx.QueryRow(ctx, `
			SELECT s.balance_credits
			FROM extra_usage_transactions t
			JOIN extra_usage_settings s ON s.project_id = t.project_id
			WHERE t.order_id = $1 AND t.type = 'topup'`, req.OrderID,
		).Scan(&existing)
		if err == nil {
			_ = tx.Commit(ctx)
			return existing, nil
		}
		if err != pgx.ErrNoRows {
			return 0, fmt.Errorf("check existing topup: %w", err)
		}
	}

	var newBalance int64
	err = tx.QueryRow(ctx, `
		INSERT INTO extra_usage_settings (project_id, balance_credits)
		VALUES ($1, $2)
		ON CONFLICT (project_id) DO UPDATE
			SET balance_credits = extra_usage_settings.balance_credits + EXCLUDED.balance_credits,
			    updated_at      = NOW()
		RETURNING balance_credits`,
		req.ProjectID, req.AmountCredits,
	).Scan(&newBalance)
	if err != nil {
		return 0, fmt.Errorf("apply topup to settings: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO extra_usage_transactions
		  (project_id, type, amount_credits, balance_after_credits, order_id, reason, description)
		VALUES ($1, 'topup', $2, $3, $4, $5, $6)`,
		req.ProjectID, req.AmountCredits, newBalance,
		nullString(req.OrderID), req.Reason, req.Description,
	)
	if err != nil {
		return 0, fmt.Errorf("insert topup ledger row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit tx: %w", err)
	}
	return newBalance, nil
}

// GetMonthlyExtraSpendCredits returns the total spent (positive credits) in the
// month containing monthStart. Callers compute monthStart via
// store.MonthWindowStart() (which reads time.Local, set from the TZ
// env var) so the boundary matches DeductExtraUsage's atomic check
// exactly.
func (s *Store) GetMonthlyExtraSpendCredits(projectID string, monthStart time.Time) (int64, error) {
	var spent int64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(-amount_credits), 0)::bigint
		FROM extra_usage_transactions
		WHERE project_id = $1
		  AND type = 'deduction'
		  AND created_at >= $2`,
		projectID, monthStart,
	).Scan(&spent)
	if err != nil {
		return 0, fmt.Errorf("sum monthly spend: %w", err)
	}
	return spent, nil
}

// ListExtraUsageTransactions returns a page of ledger rows, newest first. A
// non-empty `typeFilter` limits rows to a single transaction type.
func (s *Store) ListExtraUsageTransactions(projectID string, p types.PaginationParams, typeFilter string) ([]types.ExtraUsageTransaction, int, error) {
	ctx := context.Background()
	args := []interface{}{projectID}
	where := "WHERE project_id = $1"
	if typeFilter != "" {
		args = append(args, typeFilter)
		where += " AND type = $2"
	}

	var total int
	if err := s.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM extra_usage_transactions "+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count transactions: %w", err)
	}

	args = append(args, p.Limit(), p.Offset())
	limitN := len(args) - 1
	offsetN := len(args)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT id, project_id, type, amount_credits, balance_after_credits,
		       COALESCE(request_id::text, ''), COALESCE(order_id::text, ''),
		       reason, description, created_at
		FROM extra_usage_transactions
		%s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d`, where, limitN, offsetN), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list transactions: %w", err)
	}
	defer rows.Close()

	var out []types.ExtraUsageTransaction
	for rows.Next() {
		var t types.ExtraUsageTransaction
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Type, &t.AmountCredits,
			&t.BalanceAfterCredits, &t.RequestID, &t.OrderID,
			&t.Reason, &t.Description, &t.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan transaction: %w", err)
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// ListExtraUsageSettings returns every settings row (admin overview).
func (s *Store) ListExtraUsageSettings() ([]types.ExtraUsageSettings, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT project_id, enabled, balance_credits, monthly_limit_credits,
		       bypass_balance_check, created_at, updated_at
		FROM extra_usage_settings
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list extra_usage_settings: %w", err)
	}
	defer rows.Close()

	var out []types.ExtraUsageSettings
	for rows.Next() {
		var s types.ExtraUsageSettings
		if err := rows.Scan(&s.ProjectID, &s.Enabled, &s.BalanceCredits,
			&s.MonthlyLimitCredits, &s.BypassBalanceCheck,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan settings: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SumRecentExtraUsageSpendCredits returns total deductions (in credits) for a
// project over the last `lookbackDays` (e.g. 7). Used by admin overview.
func (s *Store) SumRecentExtraUsageSpendCredits(projectID string, lookbackDays int) (int64, error) {
	var spent int64
	err := s.pool.QueryRow(context.Background(), fmt.Sprintf(`
		SELECT COALESCE(SUM(-amount_credits), 0)::bigint
		FROM extra_usage_transactions
		WHERE project_id = $1
		  AND type = 'deduction'
		  AND created_at >= NOW() - INTERVAL '%d days'`, lookbackDays),
		projectID,
	).Scan(&spent)
	if err != nil {
		return 0, fmt.Errorf("sum recent spend: %w", err)
	}
	return spent, nil
}

// RefundExtraUsageTopup reverses a previously-applied topup by inserting a
// 'refund' ledger row with the negated credits and decrementing the
// project's balance. The refund credits value mirrors what the original
// topup added: any subsequent unit-price changes don't affect the reversal
// amount.
//
// Idempotent: the uniq_eut_refund_order partial unique index causes the
// INSERT to fail with PG unique-violation on retry; this method maps that
// to a no-op return (current balance, nil error). A caller that wants to
// distinguish "already refunded" from "newly refunded" can compare the
// returned balance with a prior read.
//
// Balance may go negative if the user spent the credits before the refund
// landed. The extra-usage guard's BalanceCredits <= 0 check rejects further
// requests until rectified via TopUp or admin_adjust.
func (s *Store) RefundExtraUsageTopup(orderID string) (int64, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Locate the original topup row.
	var (
		projectID   string
		creditsOrig int64
	)
	err = tx.QueryRow(ctx, `
		SELECT project_id, amount_credits
		FROM extra_usage_transactions
		WHERE order_id = $1 AND type = 'topup'`, orderID,
	).Scan(&projectID, &creditsOrig)
	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("refund: no topup for order %s", orderID)
	}
	if err != nil {
		return 0, fmt.Errorf("refund: lookup topup: %w", err)
	}

	// Decrement balance (allow negative — no CHECK constraint after PR migration 022 dropped them).
	var newBalance int64
	err = tx.QueryRow(ctx, `
		UPDATE extra_usage_settings
		   SET balance_credits = balance_credits - $1, updated_at = NOW()
		 WHERE project_id = $2
		 RETURNING balance_credits`,
		creditsOrig, projectID,
	).Scan(&newBalance)
	if err != nil {
		return 0, fmt.Errorf("refund: decrement balance: %w", err)
	}

	// Insert refund ledger row. Negate the credits in Go (per the PR #52
	// lesson — SQL `-$N` triggers SQLSTATE 42725 on untyped params).
	_, err = tx.Exec(ctx, `
		INSERT INTO extra_usage_transactions
		  (project_id, type, amount_credits, balance_after_credits, order_id, reason, description)
		VALUES ($1, 'refund', $2, $3, $4, $5, $6)`,
		projectID, -creditsOrig, newBalance, orderID,
		types.ExtraUsageReasonAdminRefund,
		fmt.Sprintf("refund of topup order %s (credits=%d)", orderID, creditsOrig),
	)
	if err != nil {
		// Check for the partial-unique-index violation (idempotent re-run).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Already refunded — roll back the balance decrement and report current.
			tx.Rollback(ctx)
			var curBalance int64
			_ = s.pool.QueryRow(ctx, `SELECT balance_credits FROM extra_usage_settings WHERE project_id = $1`, projectID).Scan(&curBalance)
			return curBalance, nil
		}
		return 0, fmt.Errorf("refund: insert ledger row: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("refund: commit tx: %w", err)
	}
	return newBalance, nil
}
