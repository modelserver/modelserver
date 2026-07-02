package store

import (
	"context"
	"testing"
)

// TestMigration058_CatalogRowsPresent asserts that claude-sonnet-5 and
// claude-fable-5 were inserted into the models table with the expected
// default_credit_rate JSONB payload (input_rate, output_rate,
// cache_creation_rate, cache_read_rate).
func TestMigration058_CatalogRowsPresent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name            string
		wantInput       float64
		wantOutput      float64
		wantCacheCreate float64
		wantCacheRead   float64
	}{
		// Sonnet 5 mirrors sonnet-4-6 rates (API_price / 7.5, $3/$15).
		{"claude-sonnet-5", 0.4, 2.0, 0.4, 0},
		// Fable 5 at API-price scale ($10/$50 / 7.5), cache pricing follows
		// Anthropic's published rates (cache_creation = 1.25 * input for
		// 5min TTL; cache_read = 0.1 * input).
		{"claude-fable-5", 1.333, 6.667, 1.667, 0.133},
	}

	for _, tc := range cases {
		var input, output, cacheCreate, cacheRead float64
		err := st.pool.QueryRow(ctx, `
			SELECT
			  (default_credit_rate->>'input_rate')::float8,
			  (default_credit_rate->>'output_rate')::float8,
			  (default_credit_rate->>'cache_creation_rate')::float8,
			  (default_credit_rate->>'cache_read_rate')::float8
			FROM models WHERE name = $1`, tc.name).
			Scan(&input, &output, &cacheCreate, &cacheRead)
		if err != nil {
			t.Fatalf("%s: query catalog: %v", tc.name, err)
		}
		if input != tc.wantInput || output != tc.wantOutput ||
			cacheCreate != tc.wantCacheCreate || cacheRead != tc.wantCacheRead {
			t.Fatalf("%s catalog rates: input=%v output=%v cache_create=%v cache_read=%v; want %v/%v/%v/%v",
				tc.name, input, output, cacheCreate, cacheRead,
				tc.wantInput, tc.wantOutput, tc.wantCacheCreate, tc.wantCacheRead)
		}
	}
}

// TestMigration058_FableExtraUsageOnly asserts fable-5's metadata carries
// the extra_usage_only flag. This is what SubscriptionEligibilityMiddleware
// reads to force ALL clients (including Claude Code and Claude Desktop) onto
// the extra-usage path — without it, subscribers would silently consume
// fable-5 from their plan even though the model is priced above any current
// bundle. sonnet-5 must NOT carry the flag so its normal in-plan pricing
// (added below) works.
func TestMigration058_FableExtraUsageOnly(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		wantEUO bool
	}{
		{"claude-fable-5", true},
		{"claude-sonnet-5", false},
	}
	for _, tc := range cases {
		var got bool
		// COALESCE handles the JSON-key-absent case: extra_usage_only
		// defaults to false, matching the Go struct's zero-value semantics.
		err := st.pool.QueryRow(ctx, `
			SELECT COALESCE((metadata->>'extra_usage_only')::bool, false)
			  FROM models WHERE name = $1`, tc.name).Scan(&got)
		if err != nil {
			t.Fatalf("%s: query metadata: %v", tc.name, err)
		}
		if got != tc.wantEUO {
			t.Fatalf("%s metadata.extra_usage_only = %v; want %v", tc.name, got, tc.wantEUO)
		}
	}
}

// TestMigration058_SonnetSeededInPlans asserts every pre-existing plan now
// has claude-sonnet-5 in model_credit_rates with the expected rate values.
// claude-fable-5 is intentionally NOT seeded into plans — it's routed
// through extra-usage via metadata.extra_usage_only=true and prices from
// catalog.default_credit_rate, so a plan-side rate would be a dead entry.
func TestMigration058_SonnetSeededInPlans(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	const key = "claude-sonnet-5"
	const wantInput, wantOutput, wantCacheCreate, wantCacheRead = 0.4, 2.0, 0.4, 0.0

	// Every plan should have the key (NOT (rates ? key) guard means any
	// plan touched by the migration set it).
	var missing int
	if err := st.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM plans WHERE NOT (model_credit_rates ? $1)`, key).
		Scan(&missing); err != nil {
		t.Fatalf("count missing %s: %v", key, err)
	}
	if missing != 0 {
		t.Fatalf("%d plan(s) missing %s after migration", missing, key)
	}

	// Spot-check the rate values on the well-known 'pro' plan.
	var input, output, cacheCreate, cacheRead float64
	err := st.pool.QueryRow(ctx, `
		SELECT
		  (model_credit_rates->$1->>'input_rate')::float8,
		  (model_credit_rates->$1->>'output_rate')::float8,
		  (model_credit_rates->$1->>'cache_creation_rate')::float8,
		  (model_credit_rates->$1->>'cache_read_rate')::float8
		FROM plans WHERE slug = 'pro'`, key).
		Scan(&input, &output, &cacheCreate, &cacheRead)
	if err != nil {
		t.Fatalf("query pro plan %s: %v", key, err)
	}
	if input != wantInput || output != wantOutput ||
		cacheCreate != wantCacheCreate || cacheRead != wantCacheRead {
		t.Fatalf("pro plan %s rates: input=%v output=%v cache_create=%v cache_read=%v; want %v/%v/%v/%v",
			key, input, output, cacheCreate, cacheRead,
			wantInput, wantOutput, wantCacheCreate, wantCacheRead)
	}
}

// TestMigration058_FableNotSeededInPlans is the negative counterpart to the
// sonnet check above: because fable-5 rides on default_credit_rate through
// the extra-usage path, no plan should carry a plan-side rate for it.
// Regressions here (someone adds a fable-5 seed later) create silent
// dead-code entries that mislead dashboard readers.
func TestMigration058_FableNotSeededInPlans(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var present int
	if err := st.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM plans WHERE model_credit_rates ? 'claude-fable-5'`).
		Scan(&present); err != nil {
		t.Fatalf("count plans with fable-5: %v", err)
	}
	if present != 0 {
		t.Fatalf("%d plan(s) unexpectedly carry a claude-fable-5 rate — fable-5 is extra-usage-only and prices from default_credit_rate", present)
	}
}

// TestMigration058_SonnetSeededInPolicies mirrors the plan-seeded test above
// for rate_limit_policies. Fable-5 is intentionally omitted for the same
// reason (extra_usage_only routes it around plan/policy rates entirely).
// Skips silently if no policies exist yet (fresh installs have none).
func TestMigration058_SonnetSeededInPolicies(t *testing.T) {
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

	const key = "claude-sonnet-5"
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
