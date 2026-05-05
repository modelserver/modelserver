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

func TestGetExtraUsageSpendInWindow(t *testing.T) {
	st := openTestStore(t)
	_, projectID := seedUserAndProject(t, st)

	ctx := context.Background()
	now := time.Now()
	insertEU := func(isExtra bool, costFen int64, at time.Time) {
		t.Helper()
		_, err := st.pool.Exec(ctx, `
			INSERT INTO requests (project_id, model, status, input_tokens, output_tokens,
				cache_creation_tokens, cache_read_tokens, credits_consumed,
				is_extra_usage, extra_usage_cost_fen, created_at)
			VALUES ($1, 'claude-sonnet-4-6', 'success', 0, 0, 0, 0, 0, $2, $3, $4)`,
			projectID, isExtra, costFen, at)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	insertEU(true, 1234, now.Add(-1*time.Hour))
	insertEU(true, 5000, now.Add(-2*time.Hour))
	insertEU(false, 9999, now.Add(-3*time.Hour))         // not extra usage
	insertEU(true, 7777, now.Add(-72*time.Hour))         // outside window

	got, err := st.GetExtraUsageSpendInWindow(projectID, now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatalf("GetExtraUsageSpendInWindow: %v", err)
	}
	if want := int64(6234); got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}
