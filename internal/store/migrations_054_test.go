package store

import (
	"context"
	"testing"
)

// TestMigration054_ModelIndexExists verifies the single-column index on
// requests(model) is in place after the migration runs. Without this index,
// the dashboard's per-project model filter degrades to a sequential scan
// of `requests` on any reasonably-sized project. Skips without a live
// TEST_DATABASE_URL, same convention as the other migration tests.
func TestMigration054_ModelIndexExists(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var exists bool
	err := st.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'public'
			  AND tablename = 'requests'
			  AND indexname = 'idx_requests_model'
		)`).Scan(&exists)
	if err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	if !exists {
		t.Fatal("idx_requests_model not found after migration 054")
	}
}
