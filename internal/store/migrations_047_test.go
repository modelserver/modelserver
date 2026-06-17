package store

import (
	"context"
	"testing"
)

// Plan-rate matrix that must appear in every plan / policy after 047.
// Catalog is 10x these values for the gpt entries (the migration's whole point).
var migration047PlanRates = map[string]struct {
	Input, Output, CacheRead float64
	HasLongContext           bool
}{
	"gpt-5.5":            {0.0667, 0.4, 0.0067, false},
	"gpt-5.4":            {0.0333, 0.2, 0.0033, false},
	"gpt-5.4-mini":       {0.0033, 0.0267, 0.0003, true},
	"gpt-5.4-nano":       {0.0007, 0.0053, 0.0001, true},
	"gpt-5.3-codex":      {0.0233, 0.1867, 0.0023, false},
	"gpt-5.2":            {0.0233, 0.1867, 0.0023, false},
	"gpt-5.2-codex":      {0.0233, 0.1867, 0.0023, false},
	"gpt-5.1":            {0.0167, 0.1333, 0.0017, false},
	"gpt-5.1-codex":      {0.0167, 0.1333, 0.0017, false},
	"gpt-5.1-codex-max":  {0.0167, 0.1333, 0.0017, false},
	"gpt-5.1-codex-mini": {0.0033, 0.0267, 0.0003, false},
	"codex-auto-review":  {0.0233, 0.1867, 0.0023, false},
}

// TestMigration047_CatalogBackfilled asserts that the 6 NULL catalog rows
// for legacy gpt-5.1 / gpt-5.2 variants are now populated with the
// expected USD/7.5 rates.
func TestMigration047_CatalogBackfilled(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name                                 string
		wantInput, wantOutput, wantCacheRead float64
	}{
		{"gpt-5.1", 0.167, 1.333, 0.017},
		{"gpt-5.1-codex", 0.167, 1.333, 0.017},
		{"gpt-5.1-codex-max", 0.167, 1.333, 0.017},
		{"gpt-5.1-codex-mini", 0.033, 0.267, 0.003},
		{"gpt-5.2", 0.233, 1.867, 0.023},
		{"gpt-5.2-codex", 0.233, 1.867, 0.023},
	}

	for _, tc := range cases {
		var input, output, cacheRead float64
		err := st.pool.QueryRow(ctx, `
			SELECT
			  (default_credit_rate->>'input_rate')::float8,
			  (default_credit_rate->>'output_rate')::float8,
			  (default_credit_rate->>'cache_read_rate')::float8
			FROM models WHERE name = $1`, tc.name).
			Scan(&input, &output, &cacheRead)
		if err != nil {
			t.Fatalf("%s: query catalog: %v", tc.name, err)
		}
		if input != tc.wantInput || output != tc.wantOutput || cacheRead != tc.wantCacheRead {
			t.Fatalf("%s catalog: input=%v output=%v cache_read=%v; want %v/%v/%v",
				tc.name, input, output, cacheRead, tc.wantInput, tc.wantOutput, tc.wantCacheRead)
		}
	}
}

// TestMigration047_PlansRebased asserts every plan now has the 13 gpt
// entries set to the plan-rate matrix.
func TestMigration047_PlansRebased(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for name, want := range migration047PlanRates {
		// Every plan must define the key.
		var missing int
		if err := st.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM plans WHERE NOT (model_credit_rates ? $1)`, name).
			Scan(&missing); err != nil {
			t.Fatalf("count missing %s in plans: %v", name, err)
		}
		if missing != 0 {
			t.Fatalf("%d plan(s) missing %s after migration 047", missing, name)
		}

		// Spot-check rates on the 'pro' plan.
		var input, output, cacheRead float64
		err := st.pool.QueryRow(ctx, `
			SELECT
			  (model_credit_rates->$1->>'input_rate')::float8,
			  (model_credit_rates->$1->>'output_rate')::float8,
			  (model_credit_rates->$1->>'cache_read_rate')::float8
			FROM plans WHERE slug = 'pro'`, name).
			Scan(&input, &output, &cacheRead)
		if err != nil {
			t.Fatalf("pro plan %s query: %v", name, err)
		}
		if input != want.Input || output != want.Output || cacheRead != want.CacheRead {
			t.Fatalf("pro plan %s: input=%v output=%v cache_read=%v; want %v/%v/%v",
				name, input, output, cacheRead, want.Input, want.Output, want.CacheRead)
		}

		// long_context presence check.
		var hasLC bool
		if err := st.pool.QueryRow(ctx, `
			SELECT model_credit_rates->$1 ? 'long_context' FROM plans WHERE slug = 'pro'`, name).
			Scan(&hasLC); err != nil {
			t.Fatalf("pro plan %s long_context check: %v", name, err)
		}
		if hasLC != want.HasLongContext {
			t.Fatalf("pro plan %s long_context present = %v, want %v", name, hasLC, want.HasLongContext)
		}
	}
}

// TestMigration047_PoliciesRebased mirrors the plans check against
// rate_limit_policies. Skips silently if no policies exist.
func TestMigration047_PoliciesRebased(t *testing.T) {
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

	for name, want := range migration047PlanRates {
		var missing int
		if err := st.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM rate_limit_policies WHERE NOT (model_credit_rates ? $1)`, name).
			Scan(&missing); err != nil {
			t.Fatalf("count missing %s in policies: %v", name, err)
		}
		if missing != 0 {
			t.Fatalf("%d polic(y/ies) missing %s after migration 047", missing, name)
		}
		_ = want // shape parity with plans test
	}
}

// TestMigration047_DoesNotTouchNonGPT asserts the rebase did not stomp
// on claude-*, deepseek-*, glm-* or the _default fallback.
func TestMigration047_DoesNotTouchNonGPT(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Claude opus rate on the 'pro' plan should still be its 001_init value.
	var input, output float64
	err := st.pool.QueryRow(ctx, `
		SELECT
		  (model_credit_rates->'claude-opus-4-7'->>'input_rate')::float8,
		  (model_credit_rates->'claude-opus-4-7'->>'output_rate')::float8
		FROM plans WHERE slug = 'pro'`).
		Scan(&input, &output)
	if err != nil {
		t.Fatalf("query pro plan claude-opus-4-7: %v", err)
	}
	if input != 0.667 || output != 3.333 {
		t.Fatalf("pro plan claude-opus-4-7 changed: input=%v output=%v; want 0.667/3.333", input, output)
	}

	// _default entry should still be the Claude-Sonnet-equivalent fallback.
	var defInput, defOutput float64
	err = st.pool.QueryRow(ctx, `
		SELECT
		  (model_credit_rates->'_default'->>'input_rate')::float8,
		  (model_credit_rates->'_default'->>'output_rate')::float8
		FROM plans WHERE slug = 'pro'`).
		Scan(&defInput, &defOutput)
	if err != nil {
		t.Fatalf("query pro plan _default: %v", err)
	}
	if defInput != 0.4 || defOutput != 2.0 {
		t.Fatalf("pro plan _default changed: input=%v output=%v; want 0.4/2.0", defInput, defOutput)
	}
}
