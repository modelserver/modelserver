package store

import (
	"context"
	"log/slog"
	"os"
	"testing"
)

// TestExtraUsageDataMigration_HappyPath walks the full convert flow on a
// fresh test DB. Requires TEST_DATABASE_URL (skips otherwise).
func TestExtraUsageDataMigration_HappyPath(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	st, err := New(dbURL, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	_, projectID := seedUserAndProject(t, st)
	ctx := context.Background()

	// Seed: pre-migration shape simulated by inserting rows AFTER the
	// columns have already been renamed (since migrations have run).
	// Use the NEW column names but assign values as if they were in fen.
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO extra_usage_settings (project_id, balance_credits)
		VALUES ($1, 2000) ON CONFLICT (project_id) DO UPDATE SET balance_credits = 2000`,
		projectID); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO extra_usage_transactions (project_id, type, amount_credits, balance_after_credits)
		VALUES ($1, 'topup', 2000, 2000)`, projectID); err != nil {
		t.Fatalf("seed topup tx: %v", err)
	}
	// Seed a deduction row to verify negative amounts are converted correctly.
	// PR #52 confirmed deduction rows store negative amount_credits.
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO extra_usage_transactions (project_id, type, amount_credits, balance_after_credits)
		VALUES ($1, 'deduction', -500, 1500)`, projectID); err != nil {
		t.Fatalf("seed deduction: %v", err)
	}

	// Wipe any audit row from a prior run so the converter actually runs.
	if _, err := st.pool.Exec(ctx, `TRUNCATE extra_usage_credit_migration_audit`); err != nil {
		t.Fatalf("truncate audit: %v", err)
	}

	t.Setenv("MODELSERVER_MIGRATION_052_CREDIT_PRICE_CNY_FEN", "5438")
	if err := st.convertExtraUsageDataToCredits(ctx); err != nil {
		t.Fatalf("convert: %v", err)
	}

	// 2000 fen × 1_000_000 / 5438 = 367,782 (integer division)
	var got int64
	err = st.pool.QueryRow(ctx, `SELECT balance_credits FROM extra_usage_settings WHERE project_id = $1`, projectID).Scan(&got)
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if got != 367782 {
		t.Errorf("balance_credits = %d, want 367782", got)
	}

	// Assert the topup transaction row was converted correctly.
	// 2000 fen × 1_000_000 / 5438 = 367_782 (integer division, same as balance)
	var gotAmount, gotBalanceAfter int64
	err = st.pool.QueryRow(ctx, `
		SELECT amount_credits, balance_after_credits
		FROM extra_usage_transactions
		WHERE project_id = $1 AND type = 'topup'
		LIMIT 1`, projectID).Scan(&gotAmount, &gotBalanceAfter)
	if err != nil {
		t.Fatalf("read transaction: %v", err)
	}
	if gotAmount != 367782 {
		t.Errorf("amount_credits = %d, want 367782", gotAmount)
	}
	if gotBalanceAfter != 367782 {
		t.Errorf("balance_after_credits = %d, want 367782", gotBalanceAfter)
	}

	// Assert the deduction row was converted correctly (negative amount).
	// -500 fen × 1_000_000 / 5438 = -91_945 (integer div; truncation toward zero in pg)
	// 1500 × 1_000_000 / 5438 = 275_836
	var negAmount, negBalanceAfter int64
	err = st.pool.QueryRow(ctx, `
		SELECT amount_credits, balance_after_credits
		FROM extra_usage_transactions
		WHERE project_id = $1 AND type = 'deduction'`, projectID).Scan(&negAmount, &negBalanceAfter)
	if err != nil {
		t.Fatalf("read deduction tx: %v", err)
	}
	if negAmount != -91945 {
		t.Errorf("deduction amount_credits = %d, want -91945", negAmount)
	}
	if negBalanceAfter != 275836 {
		t.Errorf("deduction balance_after_credits = %d, want 275836", negBalanceAfter)
	}

	// Audit row written.
	var auditDivisor int64
	if err := st.pool.QueryRow(ctx, `SELECT credit_price_cny_fen FROM extra_usage_credit_migration_audit ORDER BY id DESC LIMIT 1`).Scan(&auditDivisor); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if auditDivisor != 5438 {
		t.Errorf("audit divisor = %d, want 5438", auditDivisor)
	}

	// Idempotency: second call should no-op (no error, no further changes).
	if err := st.convertExtraUsageDataToCredits(ctx); err != nil {
		t.Fatalf("second convert: %v", err)
	}
	var auditCount int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM extra_usage_credit_migration_audit`).Scan(&auditCount); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("audit count = %d, want 1 (idempotent)", auditCount)
	}
}

// TestExtraUsageDataMigration_MissingEnvVar verifies the runner refuses to
// proceed when MODELSERVER_MIGRATION_052_CREDIT_PRICE_CNY_FEN is unset.
func TestExtraUsageDataMigration_MissingEnvVar(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	st, err := New(dbURL, slog.Default())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if _, err := st.pool.Exec(context.Background(), `TRUNCATE extra_usage_credit_migration_audit`); err != nil {
		t.Fatalf("truncate audit: %v", err)
	}
	t.Setenv("MODELSERVER_MIGRATION_052_CREDIT_PRICE_CNY_FEN", "")

	err = st.convertExtraUsageDataToCredits(context.Background())
	if err == nil {
		t.Fatal("expected error when env unset")
	}
}
