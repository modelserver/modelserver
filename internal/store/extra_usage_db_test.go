package store

import (
	"context"
	"log/slog"
	"os"
	"testing"
)

// openTestStore connects to TEST_DATABASE_URL, runs migrations, and returns
// a Store. Skips the test if the env var is unset so `go test ./...` stays
// green in CI environments without a database.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL to run (e.g. postgres://user:pass@localhost:5432/testdb?sslmode=disable)")
	}
	st, err := New(dbURL, slog.Default())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// seedUserAndProject returns (userID, projectID) strings for a freshly
// created user+project pair. Used by tests that need a valid FK target.
func seedUserAndProject(t *testing.T, st *Store) (string, string) {
	t.Helper()
	ctx := context.Background()
	var userID, projectID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO users (email) VALUES ('eubypass-' || gen_random_uuid()::text || '@test.local')
		RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO projects (name, created_by) VALUES ('eubypass-test', $1)
		RETURNING id`, userID).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return userID, projectID
}

// TestBalanceCreditsCheckDropped asserts that migration 022 actually dropped
// the CHECK (balance_credits >= 0) constraint — a negative balance must insert
// successfully. (Columns were renamed from _fen to _credits in migration 052.)
func TestBalanceCreditsCheckDropped(t *testing.T) {
	st := openTestStore(t)
	_, projectID := seedUserAndProject(t, st)

	_, err := st.pool.Exec(context.Background(), `
		INSERT INTO extra_usage_settings (project_id, balance_credits)
		VALUES ($1, -1)`, projectID)
	if err != nil {
		t.Fatalf("negative balance INSERT must succeed after migration 022, got: %v", err)
	}
}

// TestBalanceAfterCreditsCheckDropped asserts the same for the ledger's
// balance_after_credits CHECK.
func TestBalanceAfterCreditsCheckDropped(t *testing.T) {
	st := openTestStore(t)
	_, projectID := seedUserAndProject(t, st)

	_, err := st.pool.Exec(context.Background(), `
		INSERT INTO extra_usage_transactions
		  (project_id, type, amount_credits, balance_after_credits)
		VALUES ($1, 'deduction', -100, -50)`, projectID)
	if err != nil {
		t.Fatalf("negative balance_after_credits INSERT must succeed, got: %v", err)
	}
}

// TestSetExtraUsageBypassAndDeduct verifies the end-to-end bypass flow:
// SetExtraUsageBypass creates a row with enabled=false/balance=0, then
// DeductExtraUsage under bypass drives the balance negative. Turning
// bypass off should then block further deductions.
func TestSetExtraUsageBypassAndDeduct(t *testing.T) {
	st := openTestStore(t)
	_, projectID := seedUserAndProject(t, st)

	settings, err := st.SetExtraUsageBypass(projectID, true)
	if err != nil {
		t.Fatalf("SetExtraUsageBypass(true): %v", err)
	}
	if !settings.BypassBalanceCheck {
		t.Fatalf("BypassBalanceCheck=false, want true")
	}
	if settings.Enabled {
		t.Fatalf("Enabled=true on fresh bypass row, want false")
	}
	if settings.BalanceCredits != 0 {
		t.Fatalf("BalanceCredits=%d, want 0", settings.BalanceCredits)
	}

	newBal, err := st.DeductExtraUsage(DeductExtraUsageReq{
		ProjectID:        projectID,
		AmountCredits:    100,
		Reason:           "rate_limited",
		MonthWindowStart: MonthWindowStart(),
	})
	if err != nil {
		t.Fatalf("DeductExtraUsage under bypass: %v", err)
	}
	if newBal != -100 {
		t.Fatalf("newBal=%d, want -100", newBal)
	}

	if _, err := st.SetExtraUsageBypass(projectID, false); err != nil {
		t.Fatalf("SetExtraUsageBypass(false): %v", err)
	}
	_, err = st.DeductExtraUsage(DeductExtraUsageReq{
		ProjectID:        projectID,
		AmountCredits:    10,
		Reason:           "rate_limited",
		MonthWindowStart: MonthWindowStart(),
	})
	if err == nil {
		t.Fatalf("deduct after bypass off must fail (balance=-100), got nil")
	}
}
