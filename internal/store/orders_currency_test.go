package store

import (
	"context"
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/types"
)

func TestGetActivePaidCurrency(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Bootstrap: project + free subscription + USD plan we'll attach later.
	projectID := mustCreateProject(t, st, "p-currency-test")
	subFree, _ := st.CreateSubscriptionFromPlan(projectID,
		&types.Plan{ID: mustGetPlanID(t, st, "free"), Slug: "free"},
		time.Now(), time.Now().Add(365*24*time.Hour))

	// 1) No paid orders → "".
	got, err := st.GetActivePaidCurrency(projectID, subFree.ID)
	if err != nil {
		t.Fatalf("no-orders: %v", err)
	}
	if got != "" {
		t.Fatalf("no-orders: got %q, want \"\"", got)
	}

	// 2) Insert a paying order — must be ignored.
	insertOrder(t, st, ctx, projectID, subFree.ID, "CNY", "paying")
	got, err = st.GetActivePaidCurrency(projectID, subFree.ID)
	if err != nil {
		t.Fatalf("paying-only: %v", err)
	}
	if got != "" {
		t.Fatalf("paying order leaked: got %q", got)
	}

	// 3) Insert a paid CNY order tied to the subscription → "CNY".
	insertOrder(t, st, ctx, projectID, subFree.ID, "CNY", "paid")
	got, err = st.GetActivePaidCurrency(projectID, subFree.ID)
	if err != nil {
		t.Fatalf("paid-cny: %v", err)
	}
	if got != "CNY" {
		t.Fatalf("paid CNY: got %q, want CNY", got)
	}

	// 4) Insert a newer delivered USD order tied to the SAME subscription →
	//    "USD" (latest wins). In practice this won't happen because of the
	//    lock; the test pins the ORDER BY contract regardless.
	insertOrderAt(t, st, ctx, projectID, subFree.ID, "USD", "delivered",
		time.Now().Add(time.Minute))
	got, err = st.GetActivePaidCurrency(projectID, subFree.ID)
	if err != nil {
		t.Fatalf("paid-usd-later: %v", err)
	}
	if got != "USD" {
		t.Fatalf("latest wins: got %q, want USD", got)
	}

	// 5) Different subscription id must NOT see these orders.
	otherSub := "00000000-0000-0000-0000-000000000099"
	got, err = st.GetActivePaidCurrency(projectID, otherSub)
	if err != nil {
		t.Fatalf("other-sub: %v", err)
	}
	if got != "" {
		t.Fatalf("subscription bleed: got %q, want \"\"", got)
	}
}

// mustCreateProject creates a user+project pair and returns the project UUID.
// Reuses the seedUserAndProject helper defined in extra_usage_db_test.go.
func mustCreateProject(t *testing.T, st *Store, _ string) string {
	t.Helper()
	_, projectID := seedUserAndProject(t, st)
	return projectID
}

func mustGetPlanID(t *testing.T, st *Store, slug string) string {
	t.Helper()
	p, err := st.GetPlanBySlug(slug)
	if err != nil || p == nil {
		t.Fatalf("get plan %s: %v", slug, err)
	}
	return p.ID
}

func insertOrder(t *testing.T, st *Store, ctx context.Context, projectID, subID, currency, status string) {
	t.Helper()
	insertOrderAt(t, st, ctx, projectID, subID, currency, status, time.Now())
}

func insertOrderAt(t *testing.T, st *Store, ctx context.Context, projectID, subID, currency, status string, ts time.Time) {
	t.Helper()
	_, err := st.pool.Exec(ctx, `
		INSERT INTO orders (project_id, plan_id, periods, unit_price, amount,
		                    currency, status, channel, existing_subscription_id,
		                    updated_at, created_at, metadata)
		VALUES ($1, (SELECT id FROM plans WHERE slug='free'), 1, 0, 0,
		        $2, $3, 'wechat', $4, $5, $5, '{}')`,
		projectID, currency, status, subID, ts)
	if err != nil {
		t.Fatalf("insert order: %v", err)
	}
}
