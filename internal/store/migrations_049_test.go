package store

import (
	"context"
	"testing"
)

// migration049USDPrices is the USD-cents value each slug must carry after
// migration 049 runs. Anchors: pro=$20, max_5x=$100, max_20x=$200; other
// max_Nx scale linearly off max_20x (N/20 * $200 = N * $10).
var migration049USDPrices = map[string]int64{
	"free":     0,
	"pro":      2000,
	"max_2x":   4000,
	"max_5x":   10000,
	"max_20x":  20000,
	"max_40x":  40000,
	"max_60x":  60000,
	"max_80x":  80000,
	"max_100x": 100000,
	"max_120x": 120000,
	"max_200x": 200000,
	"max_240x": 240000,
}

// TestMigration049_USDPricesBackfilled asserts every known slug has the
// expected price_usd_cents value after migration 049.
func TestMigration049_USDPricesBackfilled(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	for slug, want := range migration049USDPrices {
		var got int64
		err := st.pool.QueryRow(ctx,
			`SELECT price_usd_cents FROM plans WHERE slug = $1`, slug).Scan(&got)
		if err != nil {
			t.Fatalf("query slug %s: %v", slug, err)
		}
		if got != want {
			t.Fatalf("slug %s: price_usd_cents = %d, want %d", slug, got, want)
		}
	}
}

// TestMigration049_OldColumnGone asserts the rename succeeded.
func TestMigration049_OldColumnGone(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Old name must be gone.
	var oldExists bool
	if err := st.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'plans' AND column_name = 'price_per_period'
		)`).Scan(&oldExists); err != nil {
		t.Fatalf("check old column: %v", err)
	}
	if oldExists {
		t.Fatal("plans.price_per_period still exists after migration 049")
	}

	// New name must be present.
	var newExists bool
	if err := st.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'plans' AND column_name = 'price_cny_fen'
		)`).Scan(&newExists); err != nil {
		t.Fatalf("check new column: %v", err)
	}
	if !newExists {
		t.Fatal("plans.price_cny_fen missing after migration 049")
	}
}
