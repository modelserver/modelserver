package store

import (
	"context"
	"testing"
	"time"
)

func TestGetPerModelTokenSums(t *testing.T) {
	st := openTestStore(t)
	_, projectID := seedUserAndProject(t, st)

	ctx := context.Background()
	// Seed three requests across two models inside the window, plus one
	// outside the window that must NOT be counted.
	now := time.Now()
	insert := func(model string, in, out, cc, cr int64, at time.Time) {
		t.Helper()
		_, err := st.pool.Exec(ctx, `
			INSERT INTO requests (project_id, model, status, input_tokens, output_tokens,
				cache_creation_tokens, cache_read_tokens, credits_consumed, created_at)
			VALUES ($1, $2, 'success', $3, $4, $5, $6, 0, $7)`,
			projectID, model, in, out, cc, cr, at)
		if err != nil {
			t.Fatalf("insert request: %v", err)
		}
	}
	insert("claude-sonnet-4-6", 100, 200, 10, 20, now.Add(-1*time.Hour))
	insert("claude-sonnet-4-6", 50, 75, 0, 5, now.Add(-2*time.Hour))
	insert("gpt-5", 1000, 500, 0, 0, now.Add(-3*time.Hour))
	insert("claude-sonnet-4-6", 999, 999, 0, 0, now.Add(-48*time.Hour)) // outside window

	got, err := st.GetPerModelTokenSums(projectID, now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatalf("GetPerModelTokenSums: %v", err)
	}

	byModel := make(map[string]PerModelTokenSums)
	for _, s := range got {
		byModel[s.Model] = s
	}
	if got, want := byModel["claude-sonnet-4-6"].RequestCount, int64(2); got != want {
		t.Errorf("claude RequestCount = %d, want %d", got, want)
	}
	if got, want := byModel["claude-sonnet-4-6"].InputTokens, int64(150); got != want {
		t.Errorf("claude InputTokens = %d, want %d", got, want)
	}
	if got, want := byModel["claude-sonnet-4-6"].OutputTokens, int64(275); got != want {
		t.Errorf("claude OutputTokens = %d, want %d", got, want)
	}
	if got, want := byModel["claude-sonnet-4-6"].CacheCreationTokens, int64(10); got != want {
		t.Errorf("claude CacheCreationTokens = %d, want %d", got, want)
	}
	if got, want := byModel["claude-sonnet-4-6"].CacheReadTokens, int64(25); got != want {
		t.Errorf("claude CacheReadTokens = %d, want %d", got, want)
	}
	if got, want := byModel["gpt-5"].InputTokens, int64(1000); got != want {
		t.Errorf("gpt-5 InputTokens = %d, want %d", got, want)
	}
	if _, present := byModel["claude-sonnet-4-6"]; !present {
		t.Errorf("claude row missing")
	}
}

// TestGetExtraUsageSpendInWindow pins the contract: the function SUMs
// the extra_usage_transactions ledger (type='deduction'), NOT
// requests.extra_usage_cost_fen. That distinction is load-bearing —
// see the PR #52 / PR #54 history: the two sources can diverge when
// settle fails, and "Period Paid" must reflect actually-charged money,
// not would-have-charged money. Inserting into requests.cost_fen here
// (the old test's data shape) must NOT influence the result.
func TestGetExtraUsageSpendInWindow(t *testing.T) {
	st := openTestStore(t)
	_, projectID := seedUserAndProject(t, st)

	ctx := context.Background()
	now := time.Now()

	// Inject ledger rows. amount_fen is stored negative for deductions
	// (the source-of-truth sign convention); the SUM negates it back.
	// balance_after_fen is required NOT NULL but its exact value doesn't
	// matter for this test — pick something nonzero.
	insertDeduction := func(amountFen int64, at time.Time) {
		t.Helper()
		_, err := st.pool.Exec(ctx, `
			INSERT INTO extra_usage_transactions
			  (project_id, type, amount_fen, balance_after_fen, created_at)
			VALUES ($1, 'deduction', $2, 0, $3)`,
			projectID, -amountFen, at)
		if err != nil {
			t.Fatalf("insert deduction: %v", err)
		}
	}
	insertOther := func(txType string, amountFen int64, at time.Time) {
		t.Helper()
		_, err := st.pool.Exec(ctx, `
			INSERT INTO extra_usage_transactions
			  (project_id, type, amount_fen, balance_after_fen, created_at)
			VALUES ($1, $2, $3, 0, $4)`,
			projectID, txType, amountFen, at)
		if err != nil {
			t.Fatalf("insert %s: %v", txType, err)
		}
	}

	insertDeduction(1234, now.Add(-1*time.Hour))
	insertDeduction(5000, now.Add(-2*time.Hour))
	insertOther("topup", 100000, now.Add(-3*time.Hour))   // wrong type, must be ignored
	insertDeduction(7777, now.Add(-72*time.Hour))         // outside window

	// Regression guard: planting a requests-table row with
	// is_extra_usage=true MUST NOT contribute to the SUM, because the
	// query no longer reads that column.
	_, err := st.pool.Exec(ctx, `
		INSERT INTO requests (project_id, model, status, input_tokens, output_tokens,
			cache_creation_tokens, cache_read_tokens, credits_consumed,
			is_extra_usage, extra_usage_cost_fen, created_at)
		VALUES ($1, 'claude-sonnet-4-6', 'success', 0, 0, 0, 0, 0, true, 99999, $2)`,
		projectID, now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("insert request: %v", err)
	}

	got, err := st.GetExtraUsageSpendInWindow(projectID, now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatalf("GetExtraUsageSpendInWindow: %v", err)
	}
	if want := int64(6234); got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}
