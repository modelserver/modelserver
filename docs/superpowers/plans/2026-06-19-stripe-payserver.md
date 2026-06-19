# Stripe Payserver Channel — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a third payment channel `stripe` to payserver — Stripe Checkout
Session for one-shot USD payments — while extending modelserver plans to
multi-currency and locking each project to its purchase currency.

**Architecture:** payserver gains a `StripeGateway` (creates Checkout
Sessions) and a `StripeNotifyHandler` (verifies `Stripe-Signature` + processes
`checkout.session.completed`), reusing the existing two-phase callback +
`compensate.Worker` pipeline. modelserver's `plans` table renames
`price_per_period → price_cny_fen` and adds `price_usd_cents`; a
`ChannelPricing` helper derives `(currency, unitPrice)` from `(channel, plan)`;
`GetActivePaidCurrency` reads the currency of a project's active paid
subscription's order to enforce a strict per-project currency lock.

**Tech Stack:** Go 1.26 (payserver module), `github.com/stripe/stripe-go/v86`,
PostgreSQL (pgx/v5), React/TypeScript dashboard.

**Spec:** `docs/superpowers/specs/2026-06-19-stripe-payserver-design.md`

## Global Constraints

- All payment amounts are `int64` in minor currency units (CNY fen / USD
  cents). Never use float for money.
- Cross-currency renew/upgrade is **strictly forbidden** — `409
  currency_mismatch`. A project is locked to the currency of its active paid
  subscription's order until that subscription expires (back to Free).
- payserver's `gateway.Gateway` interface stays: `CreatePayment(ctx,
  *PaymentRequest) (*PaymentResult, error)` + `Channel() string`.
- payserver's two-phase callback discipline: persist `paid` first (CAS on
  `status='pending'`), then ack the gateway, then call modelserver in a
  detached context; on failure increment `callback_retries` so
  `compensate.Worker` picks it up.
- Stripe webhook handler must verify `Stripe-Signature` using **raw request
  bytes** (no decode before verification).
- Stripe-go SDK version: **v86** (currently latest; see `go.mod`).
- payserver migrations are owned by payserver's own migration system
  (`services/payserver/internal/store/migrations/`); modelserver migrations
  live under `internal/store/migrations/` and are numbered sequentially
  (next free: `049`).
- All new SQL columns added to existing tables use `IF NOT EXISTS` and
  default values so migrations replay cleanly.
- Logs use slog with structured fields; new log lines must carry
  `channel="stripe"`, `order_id`, `trade_no=cs_xxx`, `event_id=evt_xxx` where
  applicable.

---

## Task 1: modelserver migration `049_plans_multi_currency.sql` + tests

**Files:**
- Create: `internal/store/migrations/049_plans_multi_currency.sql`
- Create: `internal/store/migrations_049_test.go`

**Interfaces:**
- Consumes: nothing (DDL only).
- Produces: column `plans.price_cny_fen` (renamed from `price_per_period`),
  new column `plans.price_usd_cents BIGINT NOT NULL DEFAULT 0`, backfilled
  USD prices per the slug map below.

- [ ] **Step 1: Write the failing test**

```go
// internal/store/migrations_049_test.go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store -run TestMigration049 -v`
Expected: FAIL — `column "price_usd_cents" does not exist` or similar.

- [ ] **Step 3: Write the migration**

```sql
-- internal/store/migrations/049_plans_multi_currency.sql
--
-- Rename price_per_period → price_cny_fen so the unit is explicit, and add
-- price_usd_cents for the Stripe channel. Backfill USD prices from business
-- anchors (no FX conversion):
--   pro=$20, max_5x=$100, max_20x=$200
--   other max_Nx: N/20 * $200 (= N * $10)
--   max_2x = 2 * pro = $40
-- free stays at 0. Unknown slugs stay at 0; admins must populate USD prices
-- for them before they can be sold via Stripe (ChannelPricing returns
-- ok=false on a zero price).
--
-- After this migration ships, any future *_add_max_*x_plan.sql or seed-data
-- insert must populate BOTH price_cny_fen and price_usd_cents directly.

ALTER TABLE plans RENAME COLUMN price_per_period TO price_cny_fen;

ALTER TABLE plans
    ADD COLUMN IF NOT EXISTS price_usd_cents BIGINT NOT NULL DEFAULT 0;

UPDATE plans SET price_usd_cents = CASE slug
    WHEN 'pro'      THEN   2000
    WHEN 'max_2x'   THEN   4000
    WHEN 'max_5x'   THEN  10000
    WHEN 'max_20x'  THEN  20000
    WHEN 'max_40x'  THEN  40000
    WHEN 'max_60x'  THEN  60000
    WHEN 'max_80x'  THEN  80000
    WHEN 'max_100x' THEN 100000
    WHEN 'max_120x' THEN 120000
    WHEN 'max_200x' THEN 200000
    WHEN 'max_240x' THEN 240000
    ELSE price_usd_cents
END,
updated_at = NOW();

COMMENT ON COLUMN plans.price_cny_fen   IS 'CNY price in fen for wechat/alipay channels';
COMMENT ON COLUMN plans.price_usd_cents IS 'USD price in cents for stripe channel';
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store -run TestMigration049 -v`
Expected: PASS (both `TestMigration049_USDPricesBackfilled` and
`TestMigration049_OldColumnGone`).

If the test environment caches DB state across runs, drop and recreate the
test database first (see `openTestStore` in `internal/store/`).

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/049_plans_multi_currency.sql \
        internal/store/migrations_049_test.go
git commit -m "feat(store): add price_usd_cents, rename price_per_period→price_cny_fen"
```

---

## Task 2: rename `Plan.PricePerPeriod → Plan.PriceCNYFen` + add `PriceUSDCents` + `IsFree()`

**Files:**
- Modify: `internal/types/plan.go`
- Modify: `internal/store/plans.go` (all 7 SELECT/INSERT/UPDATE callsites)
- Modify: `internal/billing/savings.go`
- Modify: `internal/billing/savings_test.go`
- Modify: `internal/admin/handle_plans.go` (`PricePerPeriod` field + UPDATE
  allow-list at line 161)
- Modify: `internal/admin/handle_orders.go` (all `plan.PricePerPeriod` /
  `activePlan.PricePerPeriod` references — replaced by Task 4's
  `ChannelPricing`, but we keep them compiling here)
- Modify: `internal/admin/usage_period_test.go` (fixture field name)

**Interfaces:**
- Consumes: Task 1 (DB columns now `price_cny_fen` and `price_usd_cents`).
- Produces:
  - `types.Plan.PriceCNYFen int64` (JSON tag `price_cny_fen`)
  - `types.Plan.PriceUSDCents int64` (JSON tag `price_usd_cents`)
  - `func (p *Plan) IsFree() bool { return p.PriceCNYFen == 0 && p.PriceUSDCents == 0 }`
  - `store.CreatePlan` / `GetPlanByID` / `GetPlanBySlug` / `ListPlans` /
    `ListPlansPaginated` / `ListPlansForProject` / `scanPlans` all read/write
    both new columns.
  - `handle_plans.go` body struct accepts both `price_cny_fen` and
    `price_usd_cents`; UPDATE allow-list at line 161 includes both names
    (and NOT the old name).

- [ ] **Step 1: Update the Plan type**

Replace `internal/types/plan.go` field block and add `IsFree`:

```go
// internal/types/plan.go
type Plan struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Slug             string                `json:"slug"`
	DisplayName      string                `json:"display_name"`
	Description      string                `json:"description,omitempty"`
	TierLevel        int                   `json:"tier_level"`
	GroupTag         string                `json:"group_tag,omitempty"`
	PriceCNYFen      int64                 `json:"price_cny_fen"`
	PriceUSDCents    int64                 `json:"price_usd_cents"`
	PeriodMonths     int                   `json:"period_months"`
	CreditRules      []CreditRule          `json:"credit_rules,omitempty"`
	ModelCreditRates map[string]CreditRate `json:"model_credit_rates,omitempty"`
	ClassicRules     []ClassicRule         `json:"classic_rules,omitempty"`
	IsActive         bool                  `json:"is_active"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

// IsFree reports whether the plan carries no price in any currency. Used by
// handle_orders.go to detect the "free → first paid purchase" branch; checking
// price_cny_fen alone would misclassify USD-only plans as free.
func (p *Plan) IsFree() bool {
	return p.PriceCNYFen == 0 && p.PriceUSDCents == 0
}
```

- [ ] **Step 2: Run `go build ./...` to enumerate compile errors**

Run: `go build ./...`
Expected: FAIL. Every reference to `PricePerPeriod` produces an error. This
is your worklist for Steps 3-7.

- [ ] **Step 3: Update `internal/store/plans.go`**

For each of the 7 SQL statements in `plans.go`, replace `price_per_period`
in the column list with `price_cny_fen, price_usd_cents`, and update the
matching scan/exec argument list to include `&p.PriceCNYFen, &p.PriceUSDCents`
(or `p.PriceCNYFen, p.PriceUSDCents` in INSERT). Specifically:

- `CreatePlan` (line ~14): column list and `$N` placeholder count both grow
  by one; bind args list grows by one. The placeholder list goes from
  `$1..$12` to `$1..$13`.
- `GetPlanByID` (line ~30), `GetPlanBySlug` (line ~54), `ListPlans` (line
  ~78), `ListPlansPaginated` (line ~98), `ListPlansForProject` (line ~126):
  SELECT column lists each replace `price_per_period` with
  `price_cny_fen, price_usd_cents`.
- `scanPlans` (line ~157): the scan arg list gains `&p.PriceCNYFen,
  &p.PriceUSDCents` in place of `&p.PricePerPeriod`.

Example diff for `CreatePlan`:

```go
return s.pool.QueryRow(context.Background(), `
    INSERT INTO plans (name, slug, display_name, description, tier_level, group_tag,
        price_cny_fen, price_usd_cents, period_months, credit_rules, model_credit_rates, classic_rules, is_active)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
    RETURNING id, created_at, updated_at`,
    p.Name, p.Slug, p.DisplayName, p.Description, p.TierLevel, p.GroupTag,
    p.PriceCNYFen, p.PriceUSDCents, p.PeriodMonths, creditRulesJSON, ratesJSON, classicJSON, p.IsActive,
).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
```

Apply the analogous transformation to all 7 statements.

- [ ] **Step 4: Update `handle_plans.go` create-body + UPDATE allow-list**

In `handleCreatePlan` body struct (line ~83):

```go
var body struct {
    Name             string                      `json:"name"`
    Slug             string                      `json:"slug"`
    DisplayName      string                      `json:"display_name"`
    Description      string                      `json:"description"`
    TierLevel        int                         `json:"tier_level"`
    GroupTag         string                      `json:"group_tag"`
    PriceCNYFen      int64                       `json:"price_cny_fen"`
    PriceUSDCents    int64                       `json:"price_usd_cents"`
    PeriodMonths     int                         `json:"period_months"`
    CreditRules      []types.CreditRule          `json:"credit_rules"`
    ModelCreditRates map[string]types.CreditRate `json:"model_credit_rates"`
    ClassicRules     []types.ClassicRule         `json:"classic_rules"`
}
```

In the `plan := &types.Plan{...}` literal:

```go
plan := &types.Plan{
    Name:             body.Name,
    Slug:             body.Slug,
    DisplayName:      body.DisplayName,
    Description:      body.Description,
    TierLevel:        body.TierLevel,
    GroupTag:         body.GroupTag,
    PriceCNYFen:      body.PriceCNYFen,
    PriceUSDCents:    body.PriceUSDCents,
    PeriodMonths:     body.PeriodMonths,
    CreditRules:      body.CreditRules,
    ModelCreditRates: rates,
    ClassicRules:     body.ClassicRules,
    IsActive:         true,
}
```

In `handleUpdatePlan` (line 161), replace the allow-list:

```go
for _, field := range []string{"name", "slug", "display_name", "description", "tier_level",
    "group_tag", "price_cny_fen", "price_usd_cents", "period_months", "is_active"} {
    if v, ok := body[field]; ok {
        updates[field] = v
    }
}
```

- [ ] **Step 5: Update `handle_orders.go` field references**

Replace every `plan.PricePerPeriod` and `activePlan.PricePerPeriod` with
`plan.PriceCNYFen` / `activePlan.PriceCNYFen`. **This is a mechanical
rename to keep the build green; the real channel-aware logic lands in
Task 4 and is gated by the currency lock added there. This task leaves
behavior unchanged (still always CNY).**

Specifically (line numbers approximate):
- Line 152: `unitPrice = plan.PricePerPeriod` → `unitPrice = plan.PriceCNYFen`
- Line 154: `activePlan.PricePerPeriod == 0` → `activePlan.PriceCNYFen == 0`
  (will become `activePlan.IsFree()` in Task 4)
- Line 156: `unitPrice = plan.PricePerPeriod` → `unitPrice = plan.PriceCNYFen`
- Line 166: `activePlan.PricePerPeriod` → `activePlan.PriceCNYFen`
- Line 168: `plan.PricePerPeriod` → `plan.PriceCNYFen`

- [ ] **Step 6: Update billing/savings.go and tests**

`internal/billing/savings.go` line 78: `plan.PricePerPeriod` → `plan.PriceCNYFen`.
(Stripe gating comes in Task 5.)

`internal/billing/savings_test.go` lines 34/100/163/200:
`PricePerPeriod: 19900` → `PriceCNYFen: 19900` (and `16314` / `0` likewise).

`internal/admin/usage_period_test.go` line 17:
`PricePerPeriod: 19900` → `PriceCNYFen: 19900`.

- [ ] **Step 7: Run build + all touched tests**

Run: `go build ./...`
Expected: PASS.

Run: `go test ./internal/types/... ./internal/store/... ./internal/admin/... ./internal/billing/... -count=1`
Expected: PASS. Migration 049 test passes; plans round-trip works; savings
tests pass.

- [ ] **Step 8: Commit**

```bash
git add internal/types/plan.go internal/store/plans.go \
        internal/admin/handle_plans.go internal/admin/handle_orders.go \
        internal/admin/usage_period_test.go \
        internal/billing/savings.go internal/billing/savings_test.go
git commit -m "refactor(plans): rename PricePerPeriod→PriceCNYFen + add PriceUSDCents/IsFree"
```

---

## Task 3: `billing.ChannelPricing` helper + tests

**Files:**
- Create: `internal/billing/channel.go`
- Create: `internal/billing/channel_test.go`

**Interfaces:**
- Consumes: Task 2 (`Plan.PriceCNYFen`, `Plan.PriceUSDCents`).
- Produces:
  - `func ChannelPricing(channel string, plan *types.Plan) (currency string, unitPrice int64, ok bool)`
  - Channels supported: `"wechat"` and `"alipay"` → `("CNY",
    plan.PriceCNYFen, plan.PriceCNYFen > 0)`; `"stripe"` → `("USD",
    plan.PriceUSDCents, plan.PriceUSDCents > 0)`; unknown → `("", 0, false)`.

- [ ] **Step 1: Write the failing test**

```go
// internal/billing/channel_test.go
package billing

import (
	"testing"

	"github.com/modelserver/modelserver/internal/types"
)

func TestChannelPricing(t *testing.T) {
	plan := &types.Plan{PriceCNYFen: 11999, PriceUSDCents: 2000}
	freePlan := &types.Plan{}
	cnyOnly := &types.Plan{PriceCNYFen: 11999}
	usdOnly := &types.Plan{PriceUSDCents: 2000}

	cases := []struct {
		name     string
		channel  string
		plan     *types.Plan
		wantCur  string
		wantUnit int64
		wantOK   bool
	}{
		{"wechat ok", "wechat", plan, "CNY", 11999, true},
		{"alipay ok", "alipay", plan, "CNY", 11999, true},
		{"stripe ok", "stripe", plan, "USD", 2000, true},
		{"free plan via wechat", "wechat", freePlan, "CNY", 0, false},
		{"free plan via stripe", "stripe", freePlan, "USD", 0, false},
		{"cny-only via stripe", "stripe", cnyOnly, "USD", 0, false},
		{"usd-only via alipay", "alipay", usdOnly, "CNY", 0, false},
		{"unknown channel", "paypal", plan, "", 0, false},
		{"empty channel", "", plan, "", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cur, unit, ok := ChannelPricing(tc.channel, tc.plan)
			if cur != tc.wantCur || unit != tc.wantUnit || ok != tc.wantOK {
				t.Fatalf("ChannelPricing(%q, ...) = (%q, %d, %v); want (%q, %d, %v)",
					tc.channel, cur, unit, ok, tc.wantCur, tc.wantUnit, tc.wantOK)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/billing -run TestChannelPricing -v`
Expected: FAIL with `undefined: ChannelPricing`.

- [ ] **Step 3: Write the implementation**

```go
// internal/billing/channel.go
package billing

import "github.com/modelserver/modelserver/internal/types"

// ChannelPricing returns the currency and per-period unit price the given
// channel must charge for this plan. `ok=false` means the channel either is
// unsupported or has no price configured for this plan in its currency —
// callers should reject the order in either case.
//
// Adding a new channel: extend the switch with its currency + the plan
// column it reads from.
func ChannelPricing(channel string, plan *types.Plan) (currency string, unitPrice int64, ok bool) {
	switch channel {
	case "wechat", "alipay":
		return "CNY", plan.PriceCNYFen, plan.PriceCNYFen > 0
	case "stripe":
		return "USD", plan.PriceUSDCents, plan.PriceUSDCents > 0
	default:
		return "", 0, false
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/billing -run TestChannelPricing -v`
Expected: PASS (all 9 subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/billing/channel.go internal/billing/channel_test.go
git commit -m "feat(billing): add ChannelPricing helper for currency-per-channel"
```

---

## Task 4: `Store.GetActivePaidCurrency` + tests

**Files:**
- Modify: `internal/store/orders.go` (append new method)
- Create: `internal/store/orders_currency_test.go`

**Interfaces:**
- Consumes: existing `orders` table with `currency`, `existing_subscription_id`,
  `status`, `updated_at` columns.
- Produces:
  - `func (s *Store) GetActivePaidCurrency(projectID, subscriptionID string) (string, error)`
  - Returns `""` when no paid/delivered order is tied to that subscription
    (free tier case). Returns the `currency` of the latest such order
    otherwise. Only considers `status IN ('paid','delivered')`.

- [ ] **Step 1: Write the failing test**

```go
// internal/store/orders_currency_test.go
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
	if err != nil { t.Fatalf("no-orders: %v", err) }
	if got != "" {
		t.Fatalf("no-orders: got %q, want \"\"", got)
	}

	// 2) Insert a paying order — must be ignored.
	insertOrder(t, st, ctx, projectID, subFree.ID, "CNY", "paying")
	got, err = st.GetActivePaidCurrency(projectID, subFree.ID)
	if err != nil { t.Fatalf("paying-only: %v", err) }
	if got != "" {
		t.Fatalf("paying order leaked: got %q", got)
	}

	// 3) Insert a paid CNY order tied to the subscription → "CNY".
	insertOrder(t, st, ctx, projectID, subFree.ID, "CNY", "paid")
	got, err = st.GetActivePaidCurrency(projectID, subFree.ID)
	if err != nil { t.Fatalf("paid-cny: %v", err) }
	if got != "CNY" {
		t.Fatalf("paid CNY: got %q, want CNY", got)
	}

	// 4) Insert a newer delivered USD order tied to the SAME subscription →
	//    "USD" (latest wins). In practice this won't happen because of the
	//    lock; the test pins the ORDER BY contract regardless.
	insertOrderAt(t, st, ctx, projectID, subFree.ID, "USD", "delivered",
		time.Now().Add(time.Minute))
	got, err = st.GetActivePaidCurrency(projectID, subFree.ID)
	if err != nil { t.Fatalf("paid-usd-later: %v", err) }
	if got != "USD" {
		t.Fatalf("latest wins: got %q, want USD", got)
	}

	// 5) Different subscription id must NOT see these orders.
	otherSub := "00000000-0000-0000-0000-000000000099"
	got, err = st.GetActivePaidCurrency(projectID, otherSub)
	if err != nil { t.Fatalf("other-sub: %v", err) }
	if got != "" {
		t.Fatalf("subscription bleed: got %q, want \"\"", got)
	}
}

// Helpers — implement these inline in the test file using the existing
// st.pool to INSERT into orders / projects. Use minimal columns: id,
// project_id, plan_id, periods, unit_price, amount, currency, status,
// channel, existing_subscription_id, updated_at, created_at.
func mustCreateProject(t *testing.T, st *Store, slug string) string {
	t.Helper()
	// Find or create using whatever helper exists in the test fixture; if
	// none, INSERT directly. Return UUID.
	// ... (see existing tests for the pattern; reuse mustCreateUser/Project
	// helpers if present, otherwise minimal raw INSERT.)
	panic("implement using existing test fixtures")
}
func mustGetPlanID(t *testing.T, st *Store, slug string) string {
	t.Helper()
	p, err := st.GetPlanBySlug(slug)
	if err != nil || p == nil { t.Fatalf("get plan %s: %v", slug, err) }
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
	if err != nil { t.Fatalf("insert order: %v", err) }
}
```

**Implementation note for the engineer:** before writing the helpers above,
read 2-3 existing tests in `internal/store/` to see the project-creation
pattern (look for `TestGetActiveSubscription` and similar). Reuse what's
there. Don't invent a new fixture system.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store -run TestGetActivePaidCurrency -v`
Expected: FAIL with `st.GetActivePaidCurrency undefined`.

- [ ] **Step 3: Write the implementation**

Append to `internal/store/orders.go`:

```go
// GetActivePaidCurrency returns the currency code recorded on the latest
// paid or delivered order tied to the given subscription. Empty string
// means the subscription has no paid order (e.g. it's the Free tier
// granted without a purchase) — callers treat that as "unlocked".
//
// Used to enforce the cross-currency lock: a project that paid in CNY can
// only renew/upgrade with CNY; switching currencies requires waiting for
// the subscription to expire.
func (s *Store) GetActivePaidCurrency(projectID, subscriptionID string) (string, error) {
	var currency string
	err := s.pool.QueryRow(context.Background(), `
		SELECT currency FROM orders
		WHERE project_id = $1
		  AND existing_subscription_id = $2
		  AND status IN ('paid', 'delivered')
		ORDER BY updated_at DESC
		LIMIT 1`, projectID, subscriptionID).Scan(&currency)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get active paid currency: %w", err)
	}
	return currency, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store -run TestGetActivePaidCurrency -v`
Expected: PASS (all 5 sub-assertions).

- [ ] **Step 5: Commit**

```bash
git add internal/store/orders.go internal/store/orders_currency_test.go
git commit -m "feat(store): add GetActivePaidCurrency for per-project currency lock"
```

---

## Task 5: rewrite `handleCreateOrder` to use `ChannelPricing` + currency lock; gate `savings`

**Files:**
- Modify: `internal/admin/handle_orders.go` (pricing branches + currency
  lock + email/metadata pass-through)
- Modify: `internal/billing/savings.go` (early-return on non-CNY orders)
- Create: `internal/admin/handle_orders_currency_test.go`

**Interfaces:**
- Consumes: Tasks 2–4 (`Plan.IsFree`, `ChannelPricing`,
  `GetActivePaidCurrency`).
- Produces: `POST /api/v1/projects/{id}/orders` now:
  - Rejects with **409 `currency_mismatch`** when `lockedCurrency != ""` and
    `lockedCurrency != orderCurrency`.
  - Rejects with **400 `bad_request`** when `ChannelPricing` returns
    `ok=false` (unsupported channel OR plan missing the channel's price).
  - Otherwise sets `order.Currency` from the channel-derived currency and
    `unitPrice` from the channel-derived base price.
  - Sends `CustomerEmail` (from project owner) and `Metadata` (`plan_slug`,
    `periods`) on the outbound `billing.PaymentRequest`. (`PaymentRequest`
    struct gains those fields in Task 6; this task assumes the struct
    update lands first or with this one — sequence Task 6 before Step 4
    here.)

- [ ] **Step 1: Write the failing test**

```go
// internal/admin/handle_orders_currency_test.go
package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests exercise the wired-up handler against an in-memory store and
// a stub payment client. Reuse the harness pattern from existing admin
// tests (look for handle_orders_test.go if present, otherwise from
// handle_plans_test.go). Each test sets up:
//   - a project with an active subscription whose latest paid order pins
//     the currency (or none, for free-tier baseline);
//   - calls POST /projects/{id}/orders;
//   - asserts status code + error code on rejections, or
//   - asserts order.Currency + unit_price on success.

func TestCreateOrder_FreeFirstBuyCNY(t *testing.T) {
	h := newOrdersHarness(t)
	h.seedFreeSubscription("proj-1")
	resp := h.post("proj-1", map[string]any{
		"plan_slug": "pro", "periods": 1, "channel": "wechat",
	})
	mustStatus(t, resp, http.StatusCreated)
	mustField(t, resp, "currency", "CNY")
}

func TestCreateOrder_FreeFirstBuyUSD(t *testing.T) {
	h := newOrdersHarness(t)
	h.seedFreeSubscription("proj-2")
	resp := h.post("proj-2", map[string]any{
		"plan_slug": "pro", "periods": 1, "channel": "stripe",
	})
	mustStatus(t, resp, http.StatusCreated)
	mustField(t, resp, "currency", "USD")
}

func TestCreateOrder_CNYLocked_USDRejected(t *testing.T) {
	h := newOrdersHarness(t)
	h.seedPaidSubscription("proj-3", "pro", "CNY")
	resp := h.post("proj-3", map[string]any{
		"plan_slug": "max_5x", "periods": 1, "channel": "stripe",
	})
	mustStatus(t, resp, http.StatusConflict)
	mustErrorCode(t, resp, "currency_mismatch")
}

func TestCreateOrder_USDLocked_CNYRejected(t *testing.T) {
	h := newOrdersHarness(t)
	h.seedPaidSubscription("proj-4", "pro", "USD")
	resp := h.post("proj-4", map[string]any{
		"plan_slug": "max_5x", "periods": 1, "channel": "alipay",
	})
	mustStatus(t, resp, http.StatusConflict)
	mustErrorCode(t, resp, "currency_mismatch")
}

func TestCreateOrder_CNYLocked_CNYRenewalAllowed(t *testing.T) {
	h := newOrdersHarness(t)
	h.seedPaidSubscription("proj-5", "pro", "CNY")
	resp := h.post("proj-5", map[string]any{
		"plan_slug": "pro", "periods": 1, "channel": "wechat",
	})
	mustStatus(t, resp, http.StatusCreated)
	mustField(t, resp, "currency", "CNY")
}

func TestCreateOrder_PlanMissingUSDPrice_BadRequest(t *testing.T) {
	h := newOrdersHarness(t)
	h.seedFreeSubscription("proj-6")
	h.zeroPlanUSDPrice("max_5x") // simulate ops forgot to set price_usd_cents
	resp := h.post("proj-6", map[string]any{
		"plan_slug": "max_5x", "periods": 1, "channel": "stripe",
	})
	mustStatus(t, resp, http.StatusBadRequest)
}

// --- harness helpers — implement using existing admin-test patterns; do NOT
// invent a new test framework. Read `handle_plans_test.go` (or whichever
// admin test file exists) to learn the established conventions for chi
// routing, store wiring, and stub payment clients. ---

type ordersHarness struct {
	// ...
}
func newOrdersHarness(t *testing.T) *ordersHarness { /* ... */ return nil }
func (h *ordersHarness) seedFreeSubscription(projectID string) { /* ... */ }
func (h *ordersHarness) seedPaidSubscription(projectID, planSlug, currency string) { /* ... */ }
func (h *ordersHarness) zeroPlanUSDPrice(planSlug string) { /* ... */ }
func (h *ordersHarness) post(projectID string, body map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/projects/"+projectID+"/orders", bytes.NewReader(b))
	w := httptest.NewRecorder()
	// route via h.router
	return w
}
func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) { /* ... */ }
func mustField(t *testing.T, w *httptest.ResponseRecorder, key, want string) { /* ... */ }
func mustErrorCode(t *testing.T, w *httptest.ResponseRecorder, want string) { /* ... */ }
```

**Engineer note:** if no `handle_orders_test.go` exists, look at
`handle_plans.go`'s test (if any) or the closest existing admin-handler
test to copy the harness shape. The point is: 6 concrete scenarios pass
through the real handler against a real test DB, using a stub
`billing.PaymentClient` that records the request and returns a canned
`PaymentResponse`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/admin -run TestCreateOrder_ -v`
Expected: FAIL — most tests fail because the lock and channel pricing are
not yet implemented (USD orders still get rejected as wrong currency, or
the harness helpers are unimplemented).

- [ ] **Step 3: Add the currency-lock + ChannelPricing block to `handleCreateOrder`**

In `internal/admin/handle_orders.go`, between the "active subscription"
lookup and the existing `isRenewal := ...` line, add the lock + pricing
derivation block:

```go
// (after `activeSub` and `activePlan` are loaded)

// Determine the currency the project is locked to via its paid history.
// "" means no paid commitment yet (Free) — any channel is allowed.
lockedCurrency, err := st.GetActivePaidCurrency(projectID, activeSub.ID)
if err != nil {
    writeError(w, http.StatusInternalServerError, "internal", "currency lookup failed")
    return
}

orderCurrency, basePrice, ok := billing.ChannelPricing(body.Channel, plan)
if !ok {
    writeError(w, http.StatusBadRequest, "bad_request",
        "channel '"+body.Channel+"' not supported or plan has no price for this channel")
    return
}

if lockedCurrency != "" && lockedCurrency != orderCurrency {
    writeError(w, http.StatusConflict, "currency_mismatch",
        "current subscription is in "+lockedCurrency+
        "; please use the same currency to renew/upgrade, or wait for it to expire")
    return
}
```

Place this block AFTER `activePlan` is fetched and BEFORE the downgrade
check (so a CNY-locked user trying to "upgrade" via USD sees
`currency_mismatch`, not `downgrade_not_allowed`).

- [ ] **Step 4: Rewire the three pricing branches to use channel-derived values**

Replace the existing `if isRenewal { ... } else if activePlan.PricePerPeriod == 0 { ... } else { ... }` block with:

```go
var unitPrice int64
var amount int64
periods := body.Periods
existingSubID := activeSub.ID

switch {
case isRenewal:
    unitPrice = basePrice
    amount = unitPrice * int64(periods)
case activePlan.IsFree():
    unitPrice = basePrice
    amount = unitPrice * int64(periods)
default:
    // Paid → paid upgrade in the SAME currency (currency lock above
    // guarantees this). Credit the residual time-based value of the
    // active subscription, denominated in the same currency.
    _, activeBase, ok := billing.ChannelPricing(body.Channel, activePlan)
    if !ok {
        // Defense in depth: the lock should have made this unreachable.
        writeError(w, http.StatusConflict, "currency_mismatch",
            "active plan has no price in "+orderCurrency)
        return
    }
    now := time.Now()
    totalDuration := activeSub.ExpiresAt.Sub(activeSub.StartsAt)
    usedDuration := now.Sub(activeSub.StartsAt)
    var remainingValue int64
    if totalDuration > 0 && usedDuration < totalDuration {
        fraction := float64(totalDuration-usedDuration) / float64(totalDuration)
        remainingValue = int64(math.Round(fraction * float64(activeBase)))
    }
    unitPrice = basePrice - remainingValue
    if unitPrice < 0 {
        unitPrice = 0
    }
    amount = unitPrice
    periods = 1
}
```

- [ ] **Step 5: Replace hardcoded `Currency: "CNY"` and add email/metadata to PaymentRequest**

```go
order := &types.Order{
    ProjectID:              projectID,
    PlanID:                 plan.ID,
    Periods:                periods,
    UnitPrice:              unitPrice,
    Amount:                 amount,
    Currency:               orderCurrency,  // was "CNY"
    Status:                 types.OrderStatusPending,
    Channel:                body.Channel,
    ExistingSubscriptionID: existingSubID,
    Metadata:               "{}",
}
```

And, fetching owner email + sending in PaymentRequest:

```go
// Project owner email — used by Stripe Checkout to prefill the email
// field. Best-effort: if lookup fails or owner has no email, send empty
// and let Stripe collect it.
var ownerEmail string
if owner, _ := st.GetUserByID(project.OwnerID); owner != nil {
    ownerEmail = owner.Email
}

payResp, err := payClient.CreatePayment(r.Context(), billing.PaymentRequest{
    OrderID:       order.ID,
    ProductName:   plan.DisplayName,
    Channel:       body.Channel,
    Currency:      order.Currency,
    Amount:        order.Amount,
    NotifyURL:     billingCfg.NotifyURL,
    ReturnURL:     billingCfg.ReturnURL,
    CustomerEmail: ownerEmail,
    Metadata: map[string]string{
        "plan_slug": plan.Slug,
        "periods":   strconv.Itoa(periods),
    },
})
```

Add the `strconv` import.

- [ ] **Step 6: Gate `billing/savings.go` to CNY-only**

`savings.go` is a CNY-only analytics module (it computes fen-denominated
"how much you saved vs OAuth top-ups"). Stripe orders must NOT flow in,
or the aggregate becomes a meaningless USD+CNY sum.

Find the entry point (`func Compute...` or similar — check the file).
Add at the top of the public function:

```go
// USD orders are excluded from savings analytics in v1; mixing USD cents
// into a fen-denominated aggregate would produce a meaningless number.
// See docs/superpowers/specs/2026-06-19-stripe-payserver-design.md §5.7.
if order.Currency != "" && order.Currency != "CNY" {
    return &SavingsBreakdown{} // or whatever zero-value the function returns
}
```

If `savings.go`'s public API doesn't receive an `order.Currency` directly,
read whichever caller has it and pass it through. Inspect the file before
writing this guard — the exact signature varies.

- [ ] **Step 7: Run the new tests + full admin/billing/store sweep**

Run: `go test ./internal/admin -run TestCreateOrder_ -v -count=1`
Expected: PASS (all 6).

Run: `go test ./internal/admin/... ./internal/billing/... ./internal/store/... -count=1`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/admin/handle_orders.go \
        internal/admin/handle_orders_currency_test.go \
        internal/billing/savings.go
git commit -m "feat(orders): per-channel pricing + strict currency lock + savings gating"
```

---

## Task 6: extend `billing.PaymentRequest` + payserver `gateway.PaymentRequest` + handler/alipay wiring

**Files:**
- Modify: `internal/billing/client.go` (add `CustomerEmail`, `Metadata`)
- Modify: `internal/billing/http_client.go` (serializer carries them already
  via struct tags, but verify)
- Modify: `services/payserver/internal/gateway/gateway.go`
- Modify: `services/payserver/internal/server/handler.go` (forward new
  fields)
- Modify: `services/payserver/internal/gateway/alipay.go` (prefer
  `req.ReturnURL` with config fallback)
- Modify: `services/payserver/internal/server/handler_test.go` (extend
  `TestParsePaymentRequest` to cover new fields)
- Create: `services/payserver/internal/gateway/alipay_returnurl_test.go`

**Interfaces:**
- Consumes: caller from Task 5.
- Produces:
  - `billing.PaymentRequest` gains `CustomerEmail string`, `Metadata
    map[string]string` (already has `Metadata` per spec; verify and add
    `CustomerEmail`).
  - `gateway.PaymentRequest` becomes:
    ```go
    type PaymentRequest struct {
        OutTradeNo    string            `json:"-"`
        Description   string            `json:"-"`
        Amount        int64             `json:"-"`
        Currency      string            `json:"-"`
        ReturnURL     string            `json:"-"`
        CustomerEmail string            `json:"-"`
        Metadata      map[string]string `json:"-"`
    }
    ```
  - `paymentAPIRequest` JSON shape gains `customer_email`, `metadata`.
  - `AlipayGateway.CreatePayment` uses `req.ReturnURL` when non-empty,
    `g.cfg.ReturnURL` otherwise.

- [ ] **Step 1: Write the failing test for alipay return URL precedence**

```go
// services/payserver/internal/gateway/alipay_returnurl_test.go
package gateway

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

func TestAlipayCreatePayment_ReturnURLPrecedence(t *testing.T) {
	g := newTestAlipayGateway(t, "https://config-default.example/return")

	// (a) request supplies its own return_url → use it.
	res, err := g.CreatePayment(context.Background(), &PaymentRequest{
		OutTradeNo:  "order-abc",
		Description: "Pro",
		Amount:      11999,
		ReturnURL:   "https://from-request.example/done",
	})
	if err != nil { t.Fatalf("create: %v", err) }
	must, _ := url.Parse(res.PaymentURL)
	if got := must.Query().Get("return_url"); got != "https://from-request.example/done" {
		t.Fatalf("request URL not honored: got %q", got)
	}

	// (b) request omits return_url → fall back to config.
	res, err = g.CreatePayment(context.Background(), &PaymentRequest{
		OutTradeNo:  "order-xyz",
		Description: "Pro",
		Amount:      11999,
	})
	if err != nil { t.Fatalf("create: %v", err) }
	must, _ = url.Parse(res.PaymentURL)
	if got := must.Query().Get("return_url"); got != "https://config-default.example/return" {
		t.Fatalf("config fallback not used: got %q", got)
	}

	if !strings.Contains(res.PaymentURL, "alipay") {
		t.Fatalf("expected alipay URL, got %s", res.PaymentURL)
	}
}

// newTestAlipayGateway builds an AlipayGateway with a throwaway in-memory
// keypair. Reuse the helper from alipay_test.go if it exists; otherwise
// adapt the keygen pattern from there.
func newTestAlipayGateway(t *testing.T, configReturnURL string) *AlipayGateway {
	t.Helper()
	// ... (see services/payserver/internal/gateway/alipay_test.go for keygen)
	panic("implement using existing alipay_test.go helpers")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/payserver && go test ./internal/gateway -run TestAlipayCreatePayment_ReturnURLPrecedence -v`
Expected: FAIL (helper unimplemented + request URL ignored).

- [ ] **Step 3: Extend `gateway.PaymentRequest`**

```go
// services/payserver/internal/gateway/gateway.go
package gateway

import "context"

type Gateway interface {
	CreatePayment(ctx context.Context, req *PaymentRequest) (*PaymentResult, error)
	Channel() string
}

type PaymentRequest struct {
	OutTradeNo    string
	Description   string
	Amount        int64
	Currency      string
	ReturnURL     string
	CustomerEmail string
	Metadata      map[string]string
}

type PaymentResult struct {
	TradeNo    string
	PaymentURL string
}
```

- [ ] **Step 4: Extend `paymentAPIRequest` and forward to gateway**

`services/payserver/internal/server/handler.go`:

```go
type paymentAPIRequest struct {
	OrderID       string            `json:"order_id"`
	ProductName   string            `json:"product_name"`
	Channel       string            `json:"channel"`
	Currency      string            `json:"currency"`
	Amount        int64             `json:"amount"`
	NotifyURL     string            `json:"notify_url"`
	ReturnURL     string            `json:"return_url"`
	CustomerEmail string            `json:"customer_email,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}
```

In the gateway call inside `handleCreatePayment`:

```go
result, err := gw.CreatePayment(r.Context(), &gateway.PaymentRequest{
    OutTradeNo:    strings.ReplaceAll(req.OrderID, "-", ""),
    Description:   req.ProductName,
    Amount:        req.Amount,
    Currency:      req.Currency,
    ReturnURL:     req.ReturnURL,
    CustomerEmail: req.CustomerEmail,
    Metadata:      req.Metadata,
})
```

- [ ] **Step 5: Update alipay gateway to honor `req.ReturnURL`**

`services/payserver/internal/gateway/alipay.go`, inside `CreatePayment`:

```go
returnURL := req.ReturnURL
if returnURL == "" {
    returnURL = g.cfg.ReturnURL
}
// ...
params.Set("return_url", returnURL)
```

- [ ] **Step 6: Extend `billing.PaymentRequest`**

`internal/billing/client.go`:

```go
type PaymentRequest struct {
	OrderID       string            `json:"order_id"`
	ProductName   string            `json:"product_name"`
	Channel       string            `json:"channel"`
	Currency      string            `json:"currency"`
	Amount        int64             `json:"amount"`
	NotifyURL     string            `json:"notify_url"`
	ReturnURL     string            `json:"return_url"`
	CustomerEmail string            `json:"customer_email,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}
```

`http_client.go` already serializes via these struct tags — verify no
explicit marshaling code drops the new fields.

- [ ] **Step 7: Run the alipay test + payserver handler tests + modelserver build**

Run: `cd services/payserver && go test ./internal/gateway -run TestAlipayCreatePayment_ -v`
Expected: PASS.

Run: `cd services/payserver && go test ./internal/server -v`
Expected: PASS (existing tests + extended `TestParsePaymentRequest` if you
updated it to cover the new fields).

Run from repo root: `go build ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/billing/client.go internal/billing/http_client.go \
        services/payserver/internal/gateway/gateway.go \
        services/payserver/internal/gateway/alipay.go \
        services/payserver/internal/gateway/alipay_returnurl_test.go \
        services/payserver/internal/server/handler.go \
        services/payserver/internal/server/handler_test.go
git commit -m "feat(gateway): carry Currency/ReturnURL/CustomerEmail/Metadata through; alipay honors request return_url"
```

---

## Task 7: Stripe gateway (Checkout Session creation) + tests

**Files:**
- Create: `services/payserver/internal/gateway/stripe.go`
- Create: `services/payserver/internal/gateway/stripe_test.go`
- Modify: `services/payserver/go.mod` (add stripe-go v86)

**Interfaces:**
- Consumes: extended `gateway.PaymentRequest` from Task 6.
- Produces:
  - `type StripeGatewayConfig struct { SecretKey, SuccessURL, CancelURL,
    DefaultLocale string }`
  - `func NewStripeGateway(cfg StripeGatewayConfig) (*StripeGateway, error)`
  - `func (*StripeGateway) Channel() string` → `"stripe"`
  - `func (*StripeGateway) CreatePayment(ctx, *PaymentRequest)
    (*PaymentResult, error)` returning `PaymentResult{TradeNo: session.ID,
    PaymentURL: session.URL}`.

- [ ] **Step 1: Add the stripe-go dependency**

Run: `cd services/payserver && go get github.com/stripe/stripe-go/v86@latest`
Expected: `go.mod` gains the require line; `go.sum` updated.

- [ ] **Step 2: Write the failing test**

```go
// services/payserver/internal/gateway/stripe_test.go
package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/client"
)

// fakeStripeBackend captures the outgoing form-encoded body so the test
// can assert which parameters are sent to Stripe without doing any real
// network calls.
type fakeStripeBackend struct {
	captured url.Values
	respond  func(method, path string) (string, error)
}

func (b *fakeStripeBackend) Call(method, path, key string, params stripe.ParamsContainer, v stripe.LastResponseSetter) error {
	return b.do(method, path, params, v)
}
func (b *fakeStripeBackend) CallStreaming(method, path, key string, params stripe.ParamsContainer, v stripe.StreamingLastResponseSetter) error {
	return errors.New("unused")
}
func (b *fakeStripeBackend) CallRaw(method, path, key string, body *form.Values, params *stripe.Params, v stripe.LastResponseSetter) error {
	// Capture body; respond with canned JSON.
	if body != nil { b.captured = body.ToValues() }
	return b.respond2(method, path, v)
}
func (b *fakeStripeBackend) CallMultipart(...) error { return errors.New("unused") }
func (b *fakeStripeBackend) SetMaxNetworkRetries(int) {}

// Engineer: the stripe-go Backend interface evolves; check the v86
// `stripe.Backend` interface in your go.mod cache and implement every
// method. The point of the fake is: capture the POSTed form, return a
// canned Checkout Session JSON, never hit the network.

func TestStripeCreatePayment_ParamsAssembly(t *testing.T) {
	be := &fakeStripeBackend{
		respond: func(method, path string) (string, error) {
			if !strings.HasSuffix(path, "/v1/checkout/sessions") {
				return "", nil
			}
			return `{"id":"cs_test_abc123","url":"https://checkout.stripe.com/c/cs_test_abc123"}`, nil
		},
	}
	sc := &client.API{}
	sc.Init("sk_test_dummy", &stripe.Backends{
		API: be, Connect: be, Uploads: be, FilesEndpoint: be, MeterEvents: be,
	})

	g := &StripeGateway{
		sc: sc,
		cfg: StripeGatewayConfig{
			SecretKey:  "sk_test_dummy",
			SuccessURL: "https://config.example/success",
			CancelURL:  "https://config.example/cancel",
			DefaultLocale: "en",
		},
	}

	res, err := g.CreatePayment(context.Background(), &PaymentRequest{
		OutTradeNo:    "order123",
		Description:   "Pro Plan",
		Amount:        2000,
		Currency:      "USD",
		ReturnURL:     "https://from-request.example/back",
		CustomerEmail: "user@example.com",
		Metadata: map[string]string{
			"plan_slug": "pro",
			"periods":   "1",
		},
	})
	if err != nil { t.Fatalf("CreatePayment: %v", err) }

	// PaymentResult shape.
	if res.TradeNo != "cs_test_abc123" {
		t.Errorf("TradeNo = %q, want cs_test_abc123", res.TradeNo)
	}
	if !strings.HasPrefix(res.PaymentURL, "https://checkout.stripe.com/") {
		t.Errorf("PaymentURL = %q", res.PaymentURL)
	}

	// Outgoing params: success_url prefers request, currency lower-cased,
	// amount in cents, ClientReferenceID, metadata, email, locale.
	got := be.captured
	if got.Get("success_url") != "https://from-request.example/back" {
		t.Errorf("success_url = %q, want request-provided value", got.Get("success_url"))
	}
	if got.Get("cancel_url") != "https://config.example/cancel" {
		t.Errorf("cancel_url = %q, want config fallback", got.Get("cancel_url"))
	}
	if got.Get("client_reference_id") != "order123" {
		t.Errorf("client_reference_id = %q", got.Get("client_reference_id"))
	}
	if got.Get("customer_email") != "user@example.com" {
		t.Errorf("customer_email = %q", got.Get("customer_email"))
	}
	if got.Get("locale") != "en" {
		t.Errorf("locale = %q", got.Get("locale"))
	}
	if got.Get("line_items[0][price_data][currency]") != "usd" {
		t.Errorf("currency = %q (expect lowercase usd)", got.Get("line_items[0][price_data][currency]"))
	}
	if got.Get("line_items[0][price_data][unit_amount]") != "2000" {
		t.Errorf("unit_amount = %q", got.Get("line_items[0][price_data][unit_amount]"))
	}
	if got.Get("metadata[order_id]") != "order123" {
		t.Errorf("metadata.order_id missing")
	}
	if got.Get("metadata[plan_slug]") != "pro" {
		t.Errorf("metadata.plan_slug = %q", got.Get("metadata[plan_slug]"))
	}
}

func TestStripeCreatePayment_DefaultsWhenRequestEmpty(t *testing.T) {
	// Same fake backend setup as above.
	// Send a request with no Currency / ReturnURL / CustomerEmail / Metadata
	// and assert: currency defaults to "usd", success_url falls back to
	// config, customer_email param is NOT set (empty optional), locale only
	// set if config has DefaultLocale.
	_ = io.EOF // placeholder for full test body — implement analogously
	_ = http.StatusOK
}
```

**Engineer note:** stripe-go's `Backend` interface in v86 has additional
methods; check the package for the full set and stub the unused ones with
`errors.New("unused")` or empty bodies. The principle: build a `client.API`
backed by a fake that captures the encoded form, drive `g.CreatePayment`,
inspect what was sent.

- [ ] **Step 3: Run test to verify it fails**

Run: `cd services/payserver && go test ./internal/gateway -run TestStripeCreatePayment_ -v`
Expected: FAIL — `StripeGateway` undefined.

- [ ] **Step 4: Implement `stripe.go`**

```go
// services/payserver/internal/gateway/stripe.go
package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/client"
)

type StripeGatewayConfig struct {
	SecretKey     string
	SuccessURL    string
	CancelURL     string
	DefaultLocale string
}

type StripeGateway struct {
	sc  *client.API
	cfg StripeGatewayConfig
}

func NewStripeGateway(cfg StripeGatewayConfig) (*StripeGateway, error) {
	if cfg.SecretKey == "" {
		return nil, errors.New("stripe: secret_key is required")
	}
	sc := &client.API{}
	sc.Init(cfg.SecretKey, nil)
	return &StripeGateway{sc: sc, cfg: cfg}, nil
}

func (g *StripeGateway) Channel() string { return "stripe" }

func (g *StripeGateway) CreatePayment(ctx context.Context, req *PaymentRequest) (*PaymentResult, error) {
	currency := strings.ToLower(req.Currency)
	if currency == "" {
		currency = "usd"
	}

	successURL := req.ReturnURL
	if successURL == "" {
		successURL = g.cfg.SuccessURL
	}
	cancelURL := g.cfg.CancelURL
	if cancelURL == "" {
		cancelURL = successURL
	}

	params := &stripe.CheckoutSessionParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL:        stripe.String(successURL),
		CancelURL:         stripe.String(cancelURL),
		ClientReferenceID: stripe.String(req.OutTradeNo),
		LineItems: []*stripe.CheckoutSessionLineItemParams{{
			Quantity: stripe.Int64(1),
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:   stripe.String(currency),
				UnitAmount: stripe.Int64(req.Amount),
				ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
					Name: stripe.String(req.Description),
				},
			},
		}},
		PaymentIntentData: &stripe.CheckoutSessionPaymentIntentDataParams{},
	}
	// PaymentIntent metadata is separate from Session metadata.
	params.PaymentIntentData.AddMetadata("order_id", req.OutTradeNo)
	// Session metadata — what we use in the webhook for tagging.
	params.AddMetadata("order_id", req.OutTradeNo)
	for k, v := range req.Metadata {
		params.AddMetadata(k, v)
	}

	if req.CustomerEmail != "" {
		params.CustomerEmail = stripe.String(req.CustomerEmail)
	}
	if g.cfg.DefaultLocale != "" {
		params.Locale = stripe.String(g.cfg.DefaultLocale)
	}

	sess, err := g.sc.CheckoutSessions.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe checkout session: %w", err)
	}
	return &PaymentResult{
		TradeNo:    sess.ID,
		PaymentURL: sess.URL,
	}, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd services/payserver && go test ./internal/gateway -run TestStripeCreatePayment_ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add services/payserver/go.mod services/payserver/go.sum \
        services/payserver/internal/gateway/stripe.go \
        services/payserver/internal/gateway/stripe_test.go
git commit -m "feat(gateway): add Stripe Checkout Session gateway"
```

---

## Task 8: Stripe webhook notify handler + tests

**Files:**
- Create: `services/payserver/internal/notify/stripe.go`
- Create: `services/payserver/internal/notify/stripe_test.go`

**Interfaces:**
- Consumes: `store.Store` (existing methods), `notify.CallbackClient`
  (existing), `uuidFromCompact` (existing in `notify/callback.go`).
- Produces:
  - `type StripeNotifyHandler struct { ... }`
  - `func NewStripeNotifyHandler(secret string, st *store.Store, cb
    *CallbackClient, logger *slog.Logger) *StripeNotifyHandler`
  - `func (h *StripeNotifyHandler) ServeHTTP(w http.ResponseWriter, r
    *http.Request)` — verifies `Stripe-Signature`, handles
    `checkout.session.completed`, persists `paid`, callbacks modelserver.

- [ ] **Step 1: Write the failing test**

```go
// services/payserver/internal/notify/stripe_test.go
package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

const testStripeSecret = "whsec_test_dummy_secret_value_long_enough"

// signStripePayload computes the same v1 HMAC Stripe sends so tests can
// drive ServeHTTP without monkey-patching the SDK.
func signStripePayload(t *testing.T, body []byte, secret string) (header string, ts int64) {
	t.Helper()
	ts = time.Now().Unix()
	signedPayload := fmt.Sprintf("%d.%s", ts, body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	sig := hex.EncodeToString(mac.Sum(nil))
	header = fmt.Sprintf("t=%d,v1=%s", ts, sig)
	return
}

func buildCheckoutSessionEvent(orderID string, amount int64, paymentStatus string) []byte {
	ev := map[string]any{
		"id":      "evt_test_1",
		"object":  "event",
		"type":    "checkout.session.completed",
		"created": time.Now().Unix(),
		"data": map[string]any{
			"object": map[string]any{
				"id":                  "cs_test_xyz",
				"object":              "checkout.session",
				"client_reference_id": orderID,
				"amount_total":        amount,
				"currency":            "usd",
				"payment_status":      paymentStatus,
			},
		},
	}
	b, _ := json.Marshal(ev)
	return b
}

func TestStripeNotify_BadSignature(t *testing.T) {
	h, _, _ := newStripeHarness(t)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader([]byte("{}")))
	req.Header.Set("Stripe-Signature", "t=0,v1=deadbeef")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad sig: got %d, want 400", w.Code)
	}
}

func TestStripeNotify_HappyPath(t *testing.T) {
	h, st, cb := newStripeHarness(t)
	orderID := seedPendingPayment(t, st, "stripe", 2000)

	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 2000, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("happy path: got %d, want 200", w.Code)
	}
	// Wait briefly for the detached callback goroutine — the handler
	// runs callback synchronously in ServeHTTP after WriteHeader, so by
	// the time ServeHTTP returns the callback has either completed or
	// failed. Verify state:
	p, _ := st.GetPaymentByOrderID(orderID)
	if p.Status != "paid" {
		t.Errorf("status = %q, want paid", p.Status)
	}
	if cb.calls != 1 {
		t.Errorf("callback calls = %d, want 1", cb.calls)
	}
}

func TestStripeNotify_NonCheckoutCompletedAcked(t *testing.T) {
	h, _, _ := newStripeHarness(t)
	body := []byte(`{"id":"evt_test_2","type":"payment_intent.created","data":{"object":{}},"created":` +
		strconv.FormatInt(time.Now().Unix(), 10) + `}`)
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ignored event: got %d, want 200", w.Code)
	}
}

func TestStripeNotify_PaymentStatusNotPaidAcked(t *testing.T) {
	h, st, cb := newStripeHarness(t)
	orderID := seedPendingPayment(t, st, "stripe", 2000)
	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 2000, "unpaid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK { t.Fatalf("got %d, want 200", w.Code) }
	if cb.calls != 0 { t.Fatalf("callback fired on unpaid event: %d calls", cb.calls) }
}

func TestStripeNotify_PaymentNotFound(t *testing.T) {
	h, _, _ := newStripeHarness(t)
	body := buildCheckoutSessionEvent("00000000000000000000000000000000", 2000, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing payment: got %d, want 404", w.Code)
	}
}

func TestStripeNotify_ChannelMismatch(t *testing.T) {
	h, st, _ := newStripeHarness(t)
	orderID := seedPendingPayment(t, st, "wechat", 2000) // wrong channel
	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 2000, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("channel mismatch: got %d, want 400", w.Code)
	}
}

func TestStripeNotify_AmountMismatch(t *testing.T) {
	h, st, _ := newStripeHarness(t)
	orderID := seedPendingPayment(t, st, "stripe", 2000)
	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 999, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("amount mismatch: got %d, want 400", w.Code)
	}
}

func TestStripeNotify_DuplicateAlreadyPaidAndCallbackSuccess(t *testing.T) {
	h, st, cb := newStripeHarness(t)
	orderID := seedPaidPayment(t, st, "stripe", 2000) // status=paid, callback_status=success
	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 2000, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK { t.Fatalf("got %d, want 200", w.Code) }
	if cb.calls != 0 { t.Fatalf("duplicate triggered callback: %d", cb.calls) }
}

func TestStripeNotify_CallbackFailureIncrementsRetries(t *testing.T) {
	h, st, cb := newStripeHarness(t)
	cb.fail = true
	orderID := seedPendingPayment(t, st, "stripe", 2000)
	body := buildCheckoutSessionEvent(strings.ReplaceAll(orderID, "-", ""), 2000, "paid")
	sig, _ := signStripePayload(t, body, testStripeSecret)
	req := httptest.NewRequest("POST", "/notify/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", sig)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK { t.Fatalf("got %d, want 200", w.Code) }
	p, _ := st.GetPaymentByOrderID(orderID)
	if p.CallbackRetries != 1 {
		t.Errorf("retries = %d, want 1", p.CallbackRetries)
	}
	if p.CallbackStatus == "success" {
		t.Errorf("callback marked success despite failure")
	}
}

// --- harness ---
// Reuse the test fixtures from alipay_test.go / callback_test.go for store
// setup and a stub callback target. Implement seedPendingPayment /
// seedPaidPayment using st.InsertOrGetPayment + st.MarkPaymentPaid.

type stubCallback struct { calls int; fail bool }
func newStripeHarness(t *testing.T) (*StripeNotifyHandler, *store.Store, *stubCallback) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := openTestPayserverStore(t) // existing helper or write it; see callback_test.go
	cb := &stubCallback{}
	cbClient := newStubCallbackClient(cb) // returns a *CallbackClient pointed at an httptest.Server
	return NewStripeNotifyHandler(testStripeSecret, st, cbClient, logger), st, cb
}

func seedPendingPayment(t *testing.T, st *store.Store, channel string, amount int64) string {
	t.Helper()
	// Use store.InsertOrGetPayment.
	p := &store.Payment{OrderID: uuid.NewString(), Channel: channel, Amount: amount, Status: "pending"}
	_, err := st.InsertOrGetPayment(p)
	if err != nil { t.Fatalf("seed pending: %v", err) }
	return p.OrderID
}
func seedPaidPayment(t *testing.T, st *store.Store, channel string, amount int64) string {
	t.Helper()
	orderID := seedPendingPayment(t, st, channel, amount)
	_, _ = st.MarkPaymentPaid(orderID, "cs_seed", `{}`, time.Now())
	_ = st.MarkCallbackSuccess(orderID)
	return orderID
}
```

**Engineer note:** the stripe-go v86 `webhook.ConstructEvent` performs a
tolerance check (±5 minutes) on the timestamp. Using `time.Now()` in the
helper is safe within that window. Sniff the `webhook` package source if
the exact signature header format (`t=<ts>,v1=<hex>`) differs in v86.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/payserver && go test ./internal/notify -run TestStripeNotify_ -v`
Expected: FAIL — `StripeNotifyHandler` undefined and harness helpers
unimplemented.

- [ ] **Step 3: Implement `stripe.go`**

```go
// services/payserver/internal/notify/stripe.go
package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/webhook"

	"github.com/modelserver/modelserver/services/payserver/internal/store"
)

type StripeNotifyHandler struct {
	webhookSecret string
	store         *store.Store
	callback      *CallbackClient
	logger        *slog.Logger
}

func NewStripeNotifyHandler(secret string, st *store.Store, cb *CallbackClient, logger *slog.Logger) *StripeNotifyHandler {
	return &StripeNotifyHandler{
		webhookSecret: secret,
		store:         st,
		callback:      cb,
		logger:        logger,
	}
}

func (h *StripeNotifyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	sig := r.Header.Get("Stripe-Signature")
	event, err := webhook.ConstructEvent(body, sig, h.webhookSecret)
	if err != nil {
		h.logger.Error("stripe notify: signature verification failed", "error", err)
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}

	if event.Type != "checkout.session.completed" {
		w.WriteHeader(http.StatusOK)
		return
	}

	var sess stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		http.Error(w, "decode", http.StatusBadRequest)
		return
	}

	if sess.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
		w.WriteHeader(http.StatusOK)
		return
	}

	orderID := uuidFromCompact(sess.ClientReferenceID)
	tradeNo := sess.ID
	paidAmount := sess.AmountTotal
	paidAt := time.Unix(event.Created, 0).UTC()

	payment, err := h.store.GetPaymentByOrderID(orderID)
	if err != nil || payment == nil {
		h.logger.Error("stripe notify: payment not found",
			"order_id", orderID, "event_id", event.ID, "error", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if payment.Channel != "stripe" {
		h.logger.Error("stripe notify: channel mismatch",
			"order_id", orderID, "channel", payment.Channel, "event_id", event.ID)
		http.Error(w, "channel mismatch", http.StatusBadRequest)
		return
	}
	if paidAmount != payment.Amount {
		h.logger.Error("stripe notify: amount mismatch",
			"order_id", orderID, "expected", payment.Amount, "got", paidAmount,
			"event_id", event.ID)
		http.Error(w, "amount mismatch", http.StatusBadRequest)
		return
	}

	if payment.Status == "paid" && payment.CallbackStatus == "success" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if payment.Status == "pending" {
		rawNotify, _ := json.Marshal(sess)
		if _, err := h.store.MarkPaymentPaid(orderID, tradeNo, string(rawNotify), paidAt); err != nil {
			h.logger.Error("stripe notify: mark paid failed",
				"order_id", orderID, "event_id", event.ID, "error", err)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
	}

	// Ack Stripe before doing the modelserver callback.
	w.WriteHeader(http.StatusOK)

	payload := DeliveryPayload{
		OrderID:    orderID,
		PaymentRef: payment.ID,
		Status:     "paid",
		PaidAmount: payment.Amount,
		PaidAt:     paidAt.Format(time.RFC3339),
	}
	cbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.callback.Send(cbCtx, payload); err != nil {
		h.logger.Warn("stripe notify: callback to modelserver failed, will retry",
			"order_id", orderID, "event_id", event.ID, "error", err)
		h.store.IncrCallbackRetries(orderID)
		return
	}
	h.store.MarkCallbackSuccess(orderID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/payserver && go test ./internal/notify -run TestStripeNotify_ -v`
Expected: PASS (all 8 sub-tests).

- [ ] **Step 5: Commit**

```bash
git add services/payserver/internal/notify/stripe.go \
        services/payserver/internal/notify/stripe_test.go
git commit -m "feat(notify): Stripe webhook handler with two-phase callback"
```

---

## Task 9: payserver config + main.go wiring

**Files:**
- Modify: `services/payserver/internal/config/config.go` (StripeConfig + env
  overrides)
- Modify: `services/payserver/config.example.yml`
- Modify: `services/payserver/cmd/payserver/main.go` (assemble gateway +
  notify)
- Modify: `services/payserver/internal/server/routes.go` (register
  `/notify/stripe`)

**Interfaces:**
- Consumes: Tasks 7–8 (`gateway.NewStripeGateway`,
  `notify.NewStripeNotifyHandler`).
- Produces:
  - `config.StripeConfig{SecretKey, WebhookSecret, SuccessURL, CancelURL, DefaultLocale string}`
  - env vars `PAYSERVER_STRIPE_{SECRET_KEY,WEBHOOK_SECRET,SUCCESS_URL,CANCEL_URL,DEFAULT_LOCALE}`
  - `server.Config.StripeNotify *notify.StripeNotifyHandler`
  - `POST /notify/stripe` route (only when `cfg.Stripe.SecretKey != ""`).

- [ ] **Step 1: Extend `config.Config`**

`services/payserver/internal/config/config.go`:

```go
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	DB       DBConfig       `yaml:"db"`
	Callback CallbackConfig `yaml:"callback"`
	APIKey   string         `yaml:"api_key"`
	Log      LogConfig      `yaml:"log"`
	WeChat   WeChatConfig   `yaml:"wechat"`
	Alipay   AlipayConfig   `yaml:"alipay"`
	Stripe   StripeConfig   `yaml:"stripe"`
}

type StripeConfig struct {
	SecretKey     string `yaml:"secret_key"`
	WebhookSecret string `yaml:"webhook_secret"`
	SuccessURL    string `yaml:"success_url"`
	CancelURL     string `yaml:"cancel_url"`
	DefaultLocale string `yaml:"default_locale"`
}
```

In `ApplyEnvOverrides()`, append:

```go
if v := os.Getenv("PAYSERVER_STRIPE_SECRET_KEY"); v != "" {
    c.Stripe.SecretKey = v
}
if v := os.Getenv("PAYSERVER_STRIPE_WEBHOOK_SECRET"); v != "" {
    c.Stripe.WebhookSecret = v
}
if v := os.Getenv("PAYSERVER_STRIPE_SUCCESS_URL"); v != "" {
    c.Stripe.SuccessURL = v
}
if v := os.Getenv("PAYSERVER_STRIPE_CANCEL_URL"); v != "" {
    c.Stripe.CancelURL = v
}
if v := os.Getenv("PAYSERVER_STRIPE_DEFAULT_LOCALE"); v != "" {
    c.Stripe.DefaultLocale = v
}
```

- [ ] **Step 2: Extend `config.example.yml`**

Append:

```yaml
stripe:
  secret_key: ""        # sk_live_... or sk_test_... (env: PAYSERVER_STRIPE_SECRET_KEY)
  webhook_secret: ""    # whsec_... from Stripe Dashboard webhook endpoint
  success_url: ""       # fallback; PaymentRequest.ReturnURL preferred when set
  cancel_url: ""        # fallback; defaults to success_url if empty
  default_locale: ""    # "" = auto (Stripe detects); "en" / "zh" / etc. to pin
```

- [ ] **Step 3: Wire gateway + notify in `cmd/payserver/main.go`**

Add `import notifyPkg ".../internal/notify"` if not already imported (it
is). After the alipay block:

```go
var stripeNotify *notifyPkg.StripeNotifyHandler
if cfg.Stripe.SecretKey != "" {
    if cfg.Stripe.WebhookSecret == "" {
        log.Fatal("stripe.webhook_secret is required when stripe is enabled")
    }
    sg, err := gateway.NewStripeGateway(gateway.StripeGatewayConfig{
        SecretKey:     cfg.Stripe.SecretKey,
        SuccessURL:    cfg.Stripe.SuccessURL,
        CancelURL:     cfg.Stripe.CancelURL,
        DefaultLocale: cfg.Stripe.DefaultLocale,
    })
    if err != nil {
        log.Fatalf("failed to init stripe gateway: %v", err)
    }
    gateways["stripe"] = sg
    stripeNotify = notifyPkg.NewStripeNotifyHandler(cfg.Stripe.WebhookSecret, st, callbackClient, logger)
    logger.Info("stripe gateway initialized")
}
```

Add `StripeNotify: stripeNotify,` to the `server.Config{...}` literal.

- [ ] **Step 4: Add `StripeNotify` field to `server.Config` + register route**

`services/payserver/internal/server/routes.go`:

```go
type Config struct {
	APIKey       string
	Store        *store.Store
	Gateways     map[string]gateway.Gateway
	WeChatNotify *notify.WeChatNotifyHandler
	AlipayNotify *notify.AlipayNotifyHandler
	StripeNotify *notify.StripeNotifyHandler
	Logger       *slog.Logger
}
```

In `NewRouter`:

```go
r.Route("/notify", func(r chi.Router) {
    if cfg.WeChatNotify != nil { r.Post("/wechat", cfg.WeChatNotify.ServeHTTP) }
    if cfg.AlipayNotify != nil { r.Post("/alipay", cfg.AlipayNotify.ServeHTTP) }
    if cfg.StripeNotify != nil { r.Post("/stripe", cfg.StripeNotify.ServeHTTP) }
})
```

- [ ] **Step 5: Build + run all payserver tests**

Run: `cd services/payserver && go build ./...`
Expected: PASS.

Run: `cd services/payserver && go test ./... -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add services/payserver/internal/config/config.go \
        services/payserver/config.example.yml \
        services/payserver/cmd/payserver/main.go \
        services/payserver/internal/server/routes.go
git commit -m "feat(payserver): wire Stripe gateway + webhook into main + routes"
```

---

## Task 10: Dashboard — Plan type + plans API + admin form + subscription page

**Files:**
- Modify: `dashboard/src/api/types.ts` (Plan interface)
- Modify: `dashboard/src/api/plans.ts` (request payload field name)
- Modify: `dashboard/src/pages/admin/PlansPage.tsx` (two price inputs)
- Modify: `dashboard/src/pages/subscriptions/SubscriptionPage.tsx` (display
  by currency)

**Interfaces:**
- Consumes: backend admin API now returns/accepts `price_cny_fen` and
  `price_usd_cents`.
- Produces: dashboard renders both prices in admin; subscription page picks
  the right price based on its `currency` field.

- [ ] **Step 1: Update `Plan` interface in `dashboard/src/api/types.ts`**

```ts
// line ~244 currently has `price_per_period: number;`
// Replace with:
price_cny_fen: number;
price_usd_cents: number;
```

- [ ] **Step 2: Update `dashboard/src/api/plans.ts`**

Lines ~29 and ~53 currently reference `price_per_period`. Replace both
with `price_cny_fen: number` (line 29) and `price_cny_fen?: number;
price_usd_cents?: number;` (line 53, the optional payload).

- [ ] **Step 3: Update `dashboard/src/pages/admin/PlansPage.tsx`**

Multiple touchpoints (line numbers from current source):
- Line 179: form type interface — add both fields.
- Line 194: form initial state — add both with `0` defaults.
- Line 210: load-from-plan — copy both.
- Line 232: submit payload — send both.
- Line 368: table column — show CNY with a subtext for USD, e.g.:

  ```tsx
  { header: "Price (CNY / USD)", accessor: (p) =>
      `${formatPriceCNY(p.price_cny_fen)} / $${(p.price_usd_cents/100).toFixed(2)}`
  },
  ```

  (Implement `formatPriceCNY` if not already present, or rename the
  existing `formatPrice` to `formatPriceCNY` and add a new
  `formatPriceUSD`.)
- Lines 527/529 form input — split into two inputs:

  ```tsx
  <label>Price (CNY fen)</label>
  <input type="number" value={form.price_cny_fen}
    onChange={(e) => setForm((p) => ({ ...p, price_cny_fen: Number(e.target.value) || 0 }))}/>
  <label>Price (USD cents)</label>
  <input type="number" value={form.price_usd_cents}
    onChange={(e) => setForm((p) => ({ ...p, price_usd_cents: Number(e.target.value) || 0 }))}/>
  ```

- [ ] **Step 4: Update `dashboard/src/pages/subscriptions/SubscriptionPage.tsx`**

Pick the right price based on the currently selected channel / project
currency. Since v1 chooses currency from channel, the simplest approach:
two `formatPrice` helpers (`formatPriceCNY`, `formatPriceUSD`), and where
each plan-price is rendered, render based on the page's active currency
context.

Lines to change (touch each — line numbers approximate):
- Line 401: `formatPrice(plan.price_per_period)` →
  `formatPriceCNY(plan.price_cny_fen)` (or whatever the page's currency
  context dictates — read the surrounding code).
- Lines 402/499/508/555/557/559/570: same mechanical rename, using the
  CNY helper.

If the subscription page is CNY-only in v1 (most likely — it's the
existing CN dashboard), keeping all displays as CNY is fine; just rename
the field. A future PR adds the USD display when the channel picker
gains a Stripe option.

- [ ] **Step 5: Smoke-test the dashboard build**

Run: `cd dashboard && npm run build` (or whatever this repo uses —
`yarn build` / `pnpm build`).
Expected: TypeScript compiles cleanly; no references to `price_per_period`
remain.

Sanity grep: `grep -rn "price_per_period" dashboard/src/` → should return
no results (except possibly inside `.claude/worktrees/` which we ignore).

- [ ] **Step 6: Commit**

```bash
git add dashboard/src/api/types.ts dashboard/src/api/plans.ts \
        dashboard/src/pages/admin/PlansPage.tsx \
        dashboard/src/pages/subscriptions/SubscriptionPage.tsx
git commit -m "feat(dashboard): rename plan price field + two-currency admin form"
```

---

## Task 11: end-to-end smoke (semi-manual) + final sweep

**Files:** none (verification only).

**Interfaces:** none.

This task is a checklist gate. It does not produce code; it verifies the
working bundle before declaring done.

- [ ] **Step 1: Full modelserver test sweep**

Run: `go test ./... -count=1`
Expected: PASS, including migration 049, ChannelPricing, GetActivePaidCurrency,
order-creation currency-lock tests.

- [ ] **Step 2: Full payserver test sweep**

Run: `cd services/payserver && go test ./... -count=1`
Expected: PASS, including StripeGateway, StripeNotifyHandler, alipay
return-URL precedence.

- [ ] **Step 3: Stripe CLI integration test (manual)**

Provision a Stripe test mode account, fill `secret_key=sk_test_...` and
`webhook_secret=whsec_test_...` in payserver config.

Terminal A:

```bash
cd services/payserver
go run ./cmd/payserver --config config.yml
```

Terminal B:

```bash
stripe listen --forward-to localhost:8090/notify/stripe
# copy the whsec_xxx the CLI prints; if it differs from your config,
# stop payserver, update PAYSERVER_STRIPE_WEBHOOK_SECRET, restart.

# Trigger a synthetic completed checkout (this will NOT find a matching
# payment row, so expect a 404 — that's the integrity check working):
stripe trigger checkout.session.completed
```

Expected: payserver logs `stripe notify: payment not found` with 404
status. Confirms signature verification + event routing work.

For a true happy path: drive an actual order via the dashboard (Task 10's
Stripe channel), pay with test card `4242 4242 4242 4242`, observe the
order transition to `delivered` and the subscription activate.

- [ ] **Step 4: Spec / code drift grep**

Run:

```bash
grep -rn "price_per_period\|PricePerPeriod" . \
  --include="*.go" --include="*.ts" --include="*.tsx" --include="*.sql" \
  --exclude-dir=".claude" --exclude-dir="node_modules"
```

Expected: only matches in `internal/store/migrations/001_init.sql` and the
seven `*_add_max_*x_plan.sql` (historical migrations — must NOT be changed)
plus the 044/048 bump migrations. No matches in current Go or TS source.

- [ ] **Step 5: Commit any cleanup / final tweaks**

```bash
# only if anything outstanding
git add -p
git commit -m "chore: address smoke-test findings"
```

- [ ] **Step 6: Open the PR**

Default to opening against `main` from a branch named
`feat/stripe-payserver` (or whatever your branch convention is). Include
in the PR description:

- Link to spec: `docs/superpowers/specs/2026-06-19-stripe-payserver-design.md`
- Migration order callout: **049 must run before binary upgrade**; old
  binaries fail on `price_per_period`.
- Public admin API JSON contract change: `price_per_period` →
  `price_cny_fen` + new `price_usd_cents`.
- Alipay behavior tweak: `return_url` now follows the request when sent.

---

## Self-Review

**Spec coverage:**

| Spec §                                  | Task(s)                          |
|------------------------------------------|----------------------------------|
| §1 File changes                          | All tasks                        |
| §2 `gateway.PaymentRequest` extension    | 6                                |
| §3 Stripe gateway                        | 7                                |
| §4 Stripe webhook                        | 8                                |
| §5.1 migration 049                       | 1                                |
| §5.2 Go field rename                     | 2                                |
| §5.3 `ChannelPricing`                    | 3                                |
| §5.4 `handleCreateOrder` + lock          | 4 (helper), 5 (handler)          |
| §5.5 Dashboard                           | 10                               |
| §5.6 unchanged surfaces                  | verified by Task 11 grep         |
| §5.7 risks                               | Task 11 PR description           |
| §6 Config + deployment                   | 9                                |
| §7 Tests + error handling                | 1, 3, 4, 5, 6, 7, 8 inline tests |
| §8 Out-of-scope                          | non-implementing                 |

Every spec section maps to at least one task.

**Placeholder scan:** Test harness helpers in Tasks 4, 5, 8 are deliberately
named-only (`mustCreateProject`, `newOrdersHarness`, `openTestPayserverStore`)
because the existing test-fixture pattern varies per package and the
engineer must read 1-2 sibling tests to pick up the convention. Each
appearance is annotated with the file to read for guidance — not a
placeholder, an explicit handoff.

**Type consistency:**
- `ChannelPricing(channel, *Plan) (currency string, unitPrice int64, ok bool)`
  — same signature in Tasks 3, 5, 7's USD plan-pricing check (the latter
  via spec; not invoked by stripe.go directly).
- `GetActivePaidCurrency(projectID, subscriptionID string) (string, error)`
  — same in Tasks 4 (definition) and 5 (consumption).
- `PriceCNYFen`/`PriceUSDCents` field names — used identically in Tasks 2,
  3, 5, 10.
- `StripeGatewayConfig{SecretKey, SuccessURL, CancelURL, DefaultLocale}` —
  Tasks 7 and 9 match.
- `StripeNotifyHandler` constructor `NewStripeNotifyHandler(secret, st,
  cb, logger)` — Tasks 8 and 9 match.

Consistent throughout.
