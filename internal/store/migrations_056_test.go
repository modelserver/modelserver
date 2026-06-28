package store

import (
	"context"
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

// TestMigration056_AddsRouteClientsWithEmptyDefault asserts the
// migration adds clients as TEXT[] NOT NULL DEFAULT '{}', leaves
// existing rows with empty array, and round-trips populated values.
func TestMigration056_AddsRouteClientsWithEmptyDefault(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var groupID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO upstream_groups (name, lb_policy, status)
		VALUES ('mig056-test', 'weighted_random', 'active')
		RETURNING id`).Scan(&groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}

	// Insert using ONLY pre-056 columns — new column must accept the row
	// via its default.
	var oldRouteID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO routes (model_names, request_kinds, upstream_group_id, match_priority, status)
		VALUES (ARRAY['claude-sonnet'], ARRAY[$1::text], $2, 10, 'active')
		RETURNING id`, types.KindAnthropicMessages, groupID).Scan(&oldRouteID); err != nil {
		t.Fatalf("insert old-style route: %v", err)
	}

	var clients []string
	if err := st.pool.QueryRow(ctx,
		`SELECT clients FROM routes WHERE id = $1`, oldRouteID).
		Scan(&clients); err != nil {
		t.Fatalf("select clients: %v", err)
	}
	if len(clients) != 0 {
		t.Errorf("default clients = %v, want []", clients)
	}

	var newRouteID string
	if err := st.pool.QueryRow(ctx, `
		INSERT INTO routes (model_names, request_kinds, upstream_group_id, match_priority, status, clients)
		VALUES (ARRAY['claude-sonnet'], ARRAY[$1::text], $2, 20, 'active',
		        ARRAY['claude-code-cli','claude-desktop'])
		RETURNING id`, types.KindAnthropicMessages, groupID).Scan(&newRouteID); err != nil {
		t.Fatalf("insert new-style route: %v", err)
	}
	if err := st.pool.QueryRow(ctx,
		`SELECT clients FROM routes WHERE id = $1`, newRouteID).
		Scan(&clients); err != nil {
		t.Fatalf("select populated clients: %v", err)
	}
	want := []string{"claude-code-cli", "claude-desktop"}
	if !equalStringSlices(clients, want) {
		t.Errorf("populated clients = %v, want %v", clients, want)
	}
}

func TestMigration056_Idempotent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := st.pool.Exec(ctx, `
		ALTER TABLE routes ADD COLUMN IF NOT EXISTS clients TEXT[] NOT NULL DEFAULT '{}'`); err != nil {
		t.Fatalf("re-run migration: %v", err)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
