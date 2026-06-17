package store

import (
	"context"
	"testing"
)

// TestMigration046_CatalogRowPresent asserts that glm-5.2 was inserted
// into the models table with the expected official-rate JSONB payload.
// No long_context block — Z.AI prices the full 1M context window flat.
func TestMigration046_CatalogRowPresent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var input, output, cacheCreate, cacheRead float64
	var publisher string
	var ctxWindow int
	err := st.pool.QueryRow(ctx, `
		SELECT
		  (default_credit_rate->>'input_rate')::float8,
		  (default_credit_rate->>'output_rate')::float8,
		  (default_credit_rate->>'cache_creation_rate')::float8,
		  (default_credit_rate->>'cache_read_rate')::float8,
		  publisher,
		  (metadata->>'context_window')::int
		FROM models WHERE name = 'glm-5.2'`).
		Scan(&input, &output, &cacheCreate, &cacheRead, &publisher, &ctxWindow)
	if err != nil {
		t.Fatalf("query catalog glm-5.2: %v", err)
	}
	if input != 0.187 || output != 0.587 || cacheCreate != 0 || cacheRead != 0.035 {
		t.Fatalf("glm-5.2 catalog rates: input=%v output=%v cache_creation=%v cache_read=%v; want 0.187/0.587/0/0.035",
			input, output, cacheCreate, cacheRead)
	}
	if publisher != "zhipu" {
		t.Fatalf("glm-5.2 publisher = %q, want %q", publisher, "zhipu")
	}
	if ctxWindow != 1_000_000 {
		t.Fatalf("glm-5.2 context_window = %d, want 1000000", ctxWindow)
	}

	// Catalog rows MUST NOT carry a long_context block — GLM-5.2 is flat.
	var hasLC bool
	if err := st.pool.QueryRow(ctx, `
		SELECT default_credit_rate ? 'long_context' FROM models WHERE name = 'glm-5.2'`).
		Scan(&hasLC); err != nil {
		t.Fatalf("query long_context presence: %v", err)
	}
	if hasLC {
		t.Fatalf("glm-5.2 catalog has long_context block, want none")
	}
}

// TestMigration046_PlansSeeded asserts every plan now has glm-5.2 in
// model_credit_rates with the expected plan-rate values (catalog * 0.1).
func TestMigration046_PlansSeeded(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var missing int
	if err := st.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM plans WHERE NOT (model_credit_rates ? 'glm-5.2')`).
		Scan(&missing); err != nil {
		t.Fatalf("count missing glm-5.2: %v", err)
	}
	if missing != 0 {
		t.Fatalf("%d plan(s) missing glm-5.2 after migration", missing)
	}

	// Spot-check rate values on the well-known 'pro' plan.
	var input, output, cacheCreate, cacheRead float64
	err := st.pool.QueryRow(ctx, `
		SELECT
		  (model_credit_rates->'glm-5.2'->>'input_rate')::float8,
		  (model_credit_rates->'glm-5.2'->>'output_rate')::float8,
		  (model_credit_rates->'glm-5.2'->>'cache_creation_rate')::float8,
		  (model_credit_rates->'glm-5.2'->>'cache_read_rate')::float8
		FROM plans WHERE slug = 'pro'`).
		Scan(&input, &output, &cacheCreate, &cacheRead)
	if err != nil {
		t.Fatalf("query pro plan glm-5.2: %v", err)
	}
	if input != 0.0187 || output != 0.0587 || cacheCreate != 0 || cacheRead != 0.0035 {
		t.Fatalf("pro plan glm-5.2 rates: input=%v output=%v cache_creation=%v cache_read=%v; want 0.0187/0.0587/0/0.0035",
			input, output, cacheCreate, cacheRead)
	}
}

// TestMigration046_PoliciesSeeded mirrors TestMigration046_PlansSeeded
// but against rate_limit_policies. Skips silently if no policies exist
// (fresh installs have none).
func TestMigration046_PoliciesSeeded(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var totalPolicies int
	if err := st.pool.QueryRow(ctx, `SELECT COUNT(*) FROM rate_limit_policies`).
		Scan(&totalPolicies); err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if totalPolicies == 0 {
		t.Skip("no rate_limit_policies rows to verify (fresh install)")
	}

	var missing int
	if err := st.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM rate_limit_policies WHERE NOT (model_credit_rates ? 'glm-5.2')`).
		Scan(&missing); err != nil {
		t.Fatalf("count missing policy glm-5.2: %v", err)
	}
	if missing != 0 {
		t.Fatalf("%d policy/policies missing glm-5.2 after migration", missing)
	}
}
