package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/modelserver/modelserver/internal/types"
)

// Sentinel errors returned by the extra-usage store. Callers distinguish these
// from generic DB errors to decide response shape (429 vs 500).
var (
	ErrExtraUsageNotEnabled       = errors.New("extra usage not enabled")
	ErrInsufficientBalance        = errors.New("extra usage balance insufficient")
	ErrMonthlyLimitReached        = errors.New("extra usage monthly limit reached")
)

// DeductExtraUsageReq carries the input for an atomic deduction.
type DeductExtraUsageReq struct {
	ProjectID   string
	AmountFen   int64
	RequestID   string
	Reason      string
	Description string
}

// TopUpExtraUsageReq carries the input for a top-up.
type TopUpExtraUsageReq struct {
	ProjectID   string
	AmountFen   int64
	OrderID     string
	Reason      string
	Description string
}

// GetExtraUsageSettings returns the settings row for a project, or nil when
// no row exists yet (project has never topped up or opened the dashboard).
func (s *Store) GetExtraUsageSettings(projectID string) (*types.ExtraUsageSettings, error) {
	row := s.pool.QueryRow(context.Background(), `
		SELECT project_id, enabled, balance_fen, monthly_limit_fen,
		       bypass_balance_check, created_at, updated_at
		FROM extra_usage_settings WHERE project_id = $1`, projectID)
	out := &types.ExtraUsageSettings{}
	err := row.Scan(&out.ProjectID, &out.Enabled, &out.BalanceFen,
		&out.MonthlyLimitFen, &out.BypassBalanceCheck,
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
// `balance_fen` is intentionally NOT writable here — it is mutated only by
// DeductExtraUsage / TopUpExtraUsage / admin adjust paths. Callers wanting
// to toggle enabled or set a monthly limit go through this function.
func (s *Store) UpsertExtraUsageSettings(projectID string, enabled bool, monthlyLimitFen int64) (*types.ExtraUsageSettings, error) {
	row := s.pool.QueryRow(context.Background(), `
		INSERT INTO extra_usage_settings (project_id, enabled, monthly_limit_fen)
		VALUES ($1, $2, $3)
		ON CONFLICT (project_id) DO UPDATE
			SET enabled           = EXCLUDED.enabled,
			    monthly_limit_fen = EXCLUDED.monthly_limit_fen,
			    updated_at        = NOW()
		RETURNING project_id, enabled, balance_fen, monthly_limit_fen,
		          bypass_balance_check, created_at, updated_at`,
		projectID, enabled, monthlyLimitFen)
	out := &types.ExtraUsageSettings{}
	err := row.Scan(&out.ProjectID, &out.Enabled, &out.BalanceFen,
		&out.MonthlyLimitFen, &out.BypassBalanceCheck,
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
		RETURNING project_id, enabled, balance_fen, monthly_limit_fen,
		          bypass_balance_check, created_at, updated_at`,
		projectID, bypass)
	out := &types.ExtraUsageSettings{}
	err := row.Scan(&out.ProjectID, &out.Enabled, &out.BalanceFen,
		&out.MonthlyLimitFen, &out.BypassBalanceCheck,
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
			SELECT COALESCE(SUM(-amount_fen), 0)::bigint AS spent
			FROM extra_usage_transactions
			WHERE project_id = $1
			  AND type = 'deduction'
			  AND created_at >= (date_trunc('month', NOW() AT TIME ZONE 'Asia/Shanghai')
			                   AT TIME ZONE 'Asia/Shanghai')
		)
		UPDATE extra_usage_settings s
		   SET balance_fen = balance_fen - $2, updated_at = NOW()
		  FROM month_spend
		 WHERE s.project_id = $1
		   AND (s.bypass_balance_check = TRUE OR s.enabled = TRUE)
		   AND (s.bypass_balance_check = TRUE OR s.balance_fen >= $2)
		   AND (s.monthly_limit_fen = 0 OR month_spend.spent + $2 <= s.monthly_limit_fen)
		RETURNING s.balance_fen`,
		req.ProjectID, req.AmountFen,
	).Scan(&newBalance)

	if err == pgx.ErrNoRows {
		// Distinguish enabled=false / balance too low / monthly limit hit.
		classifyErr := classifyDeductFailure(ctx, tx, req.ProjectID, req.AmountFen)
		return 0, classifyErr
	}
	if err != nil {
		return 0, fmt.Errorf("deduct extra_usage: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO extra_usage_transactions
		  (project_id, type, amount_fen, balance_after_fen, request_id, reason, description)
		VALUES ($1, 'deduction', -$2, $3, $4, $5, $6)`,
		req.ProjectID, req.AmountFen, newBalance,
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
func classifyDeductFailure(ctx context.Context, tx pgx.Tx, projectID string, amount int64) error {
	var enabled, bypass bool
	var balance, monthlyLimit int64
	err := tx.QueryRow(ctx, `
		SELECT enabled, balance_fen, monthly_limit_fen, bypass_balance_check
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
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(-amount_fen), 0)::bigint
			FROM extra_usage_transactions
			WHERE project_id = $1
			  AND type = 'deduction'
			  AND created_at >= (date_trunc('month', NOW() AT TIME ZONE 'Asia/Shanghai')
			                   AT TIME ZONE 'Asia/Shanghai')`,
			projectID,
		).Scan(&spent)
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
			SELECT s.balance_fen
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
		INSERT INTO extra_usage_settings (project_id, balance_fen)
		VALUES ($1, $2)
		ON CONFLICT (project_id) DO UPDATE
			SET balance_fen = extra_usage_settings.balance_fen + EXCLUDED.balance_fen,
			    updated_at  = NOW()
		RETURNING balance_fen`,
		req.ProjectID, req.AmountFen,
	).Scan(&newBalance)
	if err != nil {
		return 0, fmt.Errorf("apply topup to settings: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO extra_usage_transactions
		  (project_id, type, amount_fen, balance_after_fen, order_id, reason, description)
		VALUES ($1, 'topup', $2, $3, $4, $5, $6)`,
		req.ProjectID, req.AmountFen, newBalance,
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

// GetMonthlyExtraSpendFen returns the total spent (positive fen) in the
// current Asia/Shanghai month. Uses the same CTE as DeductExtraUsage so the
// boundary matches the atomic check.
func (s *Store) GetMonthlyExtraSpendFen(projectID string) (int64, error) {
	var spent int64
	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(-amount_fen), 0)::bigint
		FROM extra_usage_transactions
		WHERE project_id = $1
		  AND type = 'deduction'
		  AND created_at >= (date_trunc('month', NOW() AT TIME ZONE 'Asia/Shanghai')
		                   AT TIME ZONE 'Asia/Shanghai')`,
		projectID,
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
		SELECT id, project_id, type, amount_fen, balance_after_fen,
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
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Type, &t.AmountFen,
			&t.BalanceAfterFen, &t.RequestID, &t.OrderID,
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
		SELECT project_id, enabled, balance_fen, monthly_limit_fen,
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
		if err := rows.Scan(&s.ProjectID, &s.Enabled, &s.BalanceFen,
			&s.MonthlyLimitFen, &s.BypassBalanceCheck,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan settings: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SumRecentExtraUsageSpendFen returns total deductions for a project over the
// last `lookback` (e.g. 7 days). Used by admin overview.
func (s *Store) SumRecentExtraUsageSpendFen(projectID string, lookbackDays int) (int64, error) {
	var spent int64
	err := s.pool.QueryRow(context.Background(), fmt.Sprintf(`
		SELECT COALESCE(SUM(-amount_fen), 0)::bigint
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
