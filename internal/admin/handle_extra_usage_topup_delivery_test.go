package admin

// TestDeliverExtraUsageTopupOrder_AppliesPreComputedCredits pins the
// delivery-side invariant of the credits-wallet design: the webhook
// branch reads `order.ExtraUsageAmountCredits` (pre-computed at order
// creation by handleCreateExtraUsageTopup) and credits the wallet by
// exactly that value. No derivation from amount_fen / amount_cents,
// no re-conversion via the current credit_price config — those values
// can drift between order creation and delivery and the user must
// receive what they paid for.

import (
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/modelserver/modelserver/internal/store"
	"github.com/modelserver/modelserver/internal/types"
)

func TestDeliverExtraUsageTopupOrder_AppliesPreComputedCredits(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL to run")
	}

	st, err := store.New(dbURL, slog.Default())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	u := &types.User{
		Email:  fmt.Sprintf("delivery-test-%d@test.local", time.Now().UnixNano()),
		Status: "active",
	}
	if err := st.CreateUser(u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	p := &types.Project{
		Name:      "delivery-test-" + u.ID[:8],
		CreatedBy: u.ID,
		Status:    types.ProjectStatusActive,
	}
	if err := st.CreateProject(p); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Seed a paid order with a deliberately-distinct credits value.
	// 12_345 is chosen to not match any plausible (amount × 1M / price)
	// arithmetic — if delivery derived from amount it would produce a
	// different number and this test would fail.
	const wantCredits int64 = 12_345
	order := &types.Order{
		ProjectID:               p.ID,
		Periods:                 1,
		UnitPrice:               1000, // payment-side, ignored by delivery
		Amount:                  1000, // payment-side, ignored by delivery
		Currency:                "CNY",
		Status:                  types.OrderStatusPaid,
		Channel:                 "wechat",
		Metadata:                "{}",
		OrderType:               types.OrderTypeExtraUsageTopup,
		ExtraUsageAmountCredits: wantCredits,
	}
	if err := st.CreateOrder(order); err != nil {
		t.Fatalf("create order: %v", err)
	}

	bal, err := deliverExtraUsageTopupOrder(st, order)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if bal != wantCredits {
		t.Errorf("balance after first delivery = %d, want %d "+
			"(bug if equal to order.Amount=%d — delivery re-derived from fen)",
			bal, wantCredits, order.Amount)
	}

	settings, err := st.GetExtraUsageSettings(p.ID)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	if settings == nil {
		t.Fatal("settings nil after delivery; expected row created by TopUp")
	}
	if settings.BalanceCredits != wantCredits {
		t.Errorf("settings.BalanceCredits = %d, want %d",
			settings.BalanceCredits, wantCredits)
	}

	// Idempotency: a second delivery of the same order must not double-credit.
	// TopUpExtraUsage guards on the partial unique index (order_id) for
	// topup-type ledger rows; deliverExtraUsageTopupOrder bubbles up the
	// "apply topup" error from that path.
	bal2, _ := deliverExtraUsageTopupOrder(st, order)
	// Either error or balance unchanged — both acceptable per current shape.
	if bal2 != 0 && bal2 != wantCredits {
		t.Errorf("second-delivery balance = %d, want 0 (err) or %d (unchanged)", bal2, wantCredits)
	}
}

// TestDeliverExtraUsageTopupOrder_RejectsZeroCredits guards the
// defensive check: an order with ExtraUsageAmountCredits=0 must NOT
// be applied. This indicates schema/data corruption (every order is
// created with a non-zero value by handleCreateExtraUsageTopup).
func TestDeliverExtraUsageTopupOrder_RejectsZeroCredits(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("set TEST_DATABASE_URL to run")
	}

	st, err := store.New(dbURL, slog.Default())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	order := &types.Order{
		OrderType:               types.OrderTypeExtraUsageTopup,
		ExtraUsageAmountCredits: 0, // the bug we're guarding against
	}
	_, err = deliverExtraUsageTopupOrder(st, order)
	if err == nil {
		t.Fatal("expected error on zero-credit topup order, got nil")
	}
}
