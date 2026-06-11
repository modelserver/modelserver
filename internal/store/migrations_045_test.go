package store

import (
	"context"
	"testing"
)

// TestMigration045_CatalogRowsPresent asserts that gpt-5.4-mini and
// gpt-5.4-nano were inserted into the models table with the expected
// official-rate JSONB payload (input_rate, output_rate, cache_read_rate,
// long_context).
func TestMigration045_CatalogRowsPresent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name             string
		wantInput        float64
		wantOutput       float64
		wantCacheRead    float64
		wantLCInputMult  float64
		wantLCOutputMult float64
		wantLCThreshold  int
	}{
		{"gpt-5.4-mini", 0.033, 0.267, 0.003, 2.0, 1.5, 272000},
		{"gpt-5.4-nano", 0.007, 0.053, 0.001, 2.0, 1.5, 272000},
	}

	for _, tc := range cases {
		var input, output, cacheRead, lcIn, lcOut float64
		var lcThresh int
		err := st.pool.QueryRow(ctx, `
			SELECT
			  (default_credit_rate->>'input_rate')::float8,
			  (default_credit_rate->>'output_rate')::float8,
			  (default_credit_rate->>'cache_read_rate')::float8,
			  (default_credit_rate->'long_context'->>'input_multiplier')::float8,
			  (default_credit_rate->'long_context'->>'output_multiplier')::float8,
			  (default_credit_rate->'long_context'->>'threshold_input_tokens')::int
			FROM models WHERE name = $1`, tc.name).
			Scan(&input, &output, &cacheRead, &lcIn, &lcOut, &lcThresh)
		if err != nil {
			t.Fatalf("%s: query catalog: %v", tc.name, err)
		}
		if input != tc.wantInput || output != tc.wantOutput || cacheRead != tc.wantCacheRead {
			t.Fatalf("%s catalog rates: input=%v output=%v cache_read=%v; want %v/%v/%v",
				tc.name, input, output, cacheRead, tc.wantInput, tc.wantOutput, tc.wantCacheRead)
		}
		if lcIn != tc.wantLCInputMult || lcOut != tc.wantLCOutputMult || lcThresh != tc.wantLCThreshold {
			t.Fatalf("%s long_context: in_mult=%v out_mult=%v thresh=%v; want %v/%v/%v",
				tc.name, lcIn, lcOut, lcThresh,
				tc.wantLCInputMult, tc.wantLCOutputMult, tc.wantLCThreshold)
		}
	}
}

// TestMigration045_PlansSeeded asserts that every pre-existing plan now has
// both gpt-5.4-mini and gpt-5.4-nano keys in model_credit_rates with the
// expected plan-rate values (0.0654 * catalog).
func TestMigration045_PlansSeeded(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cases := []struct {
		key           string
		wantInput     float64
		wantOutput    float64
		wantCacheRead float64
	}{
		{"gpt-5.4-mini", 0.0022, 0.0175, 0.0002},
		{"gpt-5.4-nano", 0.0005, 0.0035, 0.0001},
	}

	for _, tc := range cases {
		// Every plan should have the key (NOT (rates ? key) guard means any
		// plan touched by the migration set it).
		var missing int
		if err := st.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM plans WHERE NOT (model_credit_rates ? $1)`, tc.key).
			Scan(&missing); err != nil {
			t.Fatalf("count missing %s: %v", tc.key, err)
		}
		if missing != 0 {
			t.Fatalf("%d plan(s) missing %s after migration", missing, tc.key)
		}

		// Spot-check the rate values on the well-known 'pro' plan. Includes
		// cache_read_rate and the long_context payload so a future edit that
		// drops either at the plan level is caught.
		var input, output, cacheRead, lcIn, lcOut float64
		var lcThresh int
		err := st.pool.QueryRow(ctx, `
			SELECT
			  (model_credit_rates->$1->>'input_rate')::float8,
			  (model_credit_rates->$1->>'output_rate')::float8,
			  (model_credit_rates->$1->>'cache_read_rate')::float8,
			  (model_credit_rates->$1->'long_context'->>'input_multiplier')::float8,
			  (model_credit_rates->$1->'long_context'->>'output_multiplier')::float8,
			  (model_credit_rates->$1->'long_context'->>'threshold_input_tokens')::int
			FROM plans WHERE slug = 'pro'`, tc.key).
			Scan(&input, &output, &cacheRead, &lcIn, &lcOut, &lcThresh)
		if err != nil {
			t.Fatalf("query pro plan %s: %v", tc.key, err)
		}
		if input != tc.wantInput || output != tc.wantOutput || cacheRead != tc.wantCacheRead {
			t.Fatalf("pro plan %s rates: input=%v output=%v cache_read=%v; want %v/%v/%v",
				tc.key, input, output, cacheRead,
				tc.wantInput, tc.wantOutput, tc.wantCacheRead)
		}
		if lcIn != 2.0 || lcOut != 1.5 || lcThresh != 272000 {
			t.Fatalf("pro plan %s long_context: in_mult=%v out_mult=%v thresh=%v; want 2/1.5/272000",
				tc.key, lcIn, lcOut, lcThresh)
		}
	}
}

// TestMigration045_PoliciesSeeded mirrors TestMigration045_PlansSeeded but
// against rate_limit_policies. Skips silently if no policies exist yet
// (fresh installs have none — the seed is for live deploys with custom
// policies attached to projects).
func TestMigration045_PoliciesSeeded(t *testing.T) {
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

	for _, key := range []string{"gpt-5.4-mini", "gpt-5.4-nano"} {
		var missing int
		if err := st.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM rate_limit_policies WHERE NOT (model_credit_rates ? $1)`, key).
			Scan(&missing); err != nil {
			t.Fatalf("count missing policy %s: %v", key, err)
		}
		if missing != 0 {
			t.Fatalf("%d policy/policies missing %s after migration", missing, key)
		}
	}
}
