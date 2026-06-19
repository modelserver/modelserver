package store

import (
	"context"
	"testing"
)

// TestMigration050_SubscriptionsCurrencyColumn asserts the currency column
// was added to the subscriptions table.
func TestMigration050_SubscriptionsCurrencyColumn(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var exists bool
	if err := st.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'subscriptions' AND column_name = 'currency'
		)`).Scan(&exists); err != nil {
		t.Fatalf("check currency column: %v", err)
	}
	if !exists {
		t.Fatal("subscriptions.currency missing after migration 050")
	}

	// NOT NULL constraint, default empty string.
	var isNullable, columnDefault string
	if err := st.pool.QueryRow(ctx, `
		SELECT is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_name = 'subscriptions' AND column_name = 'currency'`).
		Scan(&isNullable, &columnDefault); err != nil {
		t.Fatalf("inspect currency column: %v", err)
	}
	if isNullable != "NO" {
		t.Errorf("currency is_nullable = %q, want NO", isNullable)
	}
	if columnDefault != "''::text" && columnDefault != "''" {
		t.Errorf("currency default = %q, want ''::text", columnDefault)
	}
}
