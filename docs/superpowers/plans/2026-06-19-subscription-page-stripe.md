# SubscriptionPage Stripe Channel + Multi-Currency Display — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire the existing Stripe payment channel into the dashboard subscription page so users can actually pick it, with a CNY/USD price-display toggle and a currency-lock-enforced disabled-button UX.

**Architecture:** Two-file frontend-only change. `dashboard/src/api/types.ts` declares the new `Subscription.currency` field (backend already emits it via PR #44). `dashboard/src/pages/subscriptions/SubscriptionPage.tsx` gains: a `displayCurrency` state + top-of-grid toggle (display lens), a third `"stripe"` `PaymentChannel`, three-button dialog with currency-lock disable + native `title` tooltip, and a `window.location.href` redirect branch in `handlePay`.

**Tech Stack:** React 18, TypeScript, TanStack Query, shadcn/ui Button + Label, lucide-react icons (existing), `qrcode.react` (existing, only used by WeChat path). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-06-19-subscription-page-stripe-design.md`

## Global Constraints

- Money values are `int64` minor units everywhere — render as `(value / 100).toFixed(2)`. Never use float arithmetic for price math.
- Stripe Checkout = hosted page → full-page `window.location.href` redirect. No iframe (Stripe forbids), no popup.
- Currency toggle is **display lens only**. Real currency enforcement lives on the dialog's channel buttons. The two must be decoupled so users can't see one currency and pay in another.
- All four `setChannel("wechat")` reset sites must use `pickInitialChannel()` instead, so a USD-locked project doesn't default to a disabled channel.
- Native HTML `title=""` is the tooltip — no new dependency, no shadcn `<Tooltip>` import.
- UI strings stay English (matches existing labels: "WeChat Pay", "Pay", "Renew").
- Test gate: `cd dashboard && pnpm build` must succeed with zero TypeScript errors. There is no React unit-test harness in this project today — this plan does not add one.
- Working directory: `/root/coding/modelserver`. Branch: `feat/subscription-page-stripe` (already created).

---

## Task 1: Declare `Subscription.currency` in the TS API types

**Files:**
- Modify: `dashboard/src/api/types.ts:256-266`

**Interfaces:**
- Consumes: nothing (pure type declaration).
- Produces: `Subscription.currency?: "CNY" | "USD" | ""` available to all consumers. The field is already emitted by the backend (PR #44, `json:"currency,omitempty"`); this just teaches TypeScript about it.

- [ ] **Step 1: Verify current shape**

Run: `grep -n "currency" dashboard/src/api/types.ts`
Expected: no hits inside the `Subscription` interface. Confirms the field is missing.

- [ ] **Step 2: Add the field**

Edit `dashboard/src/api/types.ts`. Locate the `Subscription` interface (around line 256) and add `currency` between `expires_at` and `created_at`:

```ts
export interface Subscription {
  id: string;
  project_id: string;
  plan_id?: string;
  plan_name: string;
  status: "active" | "expired" | "revoked";
  starts_at: string;
  expires_at: string;
  currency?: "CNY" | "USD" | "";
  created_at: string;
  updated_at: string;
}
```

- [ ] **Step 3: Type-check**

Run: `cd dashboard && pnpm build`
Expected: PASS. (No consumer reads `subscription.currency` yet, so adding an optional field is safe.)

- [ ] **Step 4: Commit**

```bash
git add dashboard/src/api/types.ts
git commit -m "feat(dashboard): declare Subscription.currency from PR #44 backend"
```

---

## Task 2: Add `displayCurrency` toggle + dual-currency price helpers

**Files:**
- Modify: `dashboard/src/pages/subscriptions/SubscriptionPage.tsx`

**Interfaces:**
- Consumes: Task 1 (`Subscription.currency`).
- Produces:
  - `type DisplayCurrency = "CNY" | "USD"`
  - `displayCurrency` state + `setDisplayCurrency` setter
  - `formatPriceCNY(fen: number): string`
  - `formatPriceUSD(cents: number): string`
  - `formatPrice(plan: Plan): string` (toggle-aware, plan-grid use)
  - `formatPriceForCurrency(plan: Plan, cur: "CNY" | "USD"): string` (currency-explicit, current-plan-row use)

  The dialog/channel-aware helpers (`getPriceForChannel`, `formatPriceForChannel`) land in Task 4 when `PaymentChannel` exists.

  Plan-grid render is wired in Task 3; the toggle UI element itself is added in Task 3 too. This task only defines the state and helpers.

- [ ] **Step 1: Replace the single `formatPrice` with three new helpers**

In `SubscriptionPage.tsx`, locate the existing `formatPrice` (around line 75) and replace it with:

```tsx
function formatPriceCNY(fen: number) {
  if (fen === 0) return "Free";
  return `¥${(fen / 100).toFixed(2)}`;
}

function formatPriceUSD(cents: number) {
  if (cents === 0) return "Free";
  return `$${(cents / 100).toFixed(2)}`;
}

function formatPriceForCurrency(plan: Plan, cur: "CNY" | "USD") {
  return cur === "USD"
    ? formatPriceUSD(plan.price_usd_cents)
    : formatPriceCNY(plan.price_cny_fen);
}
```

Note: the existing single `formatPrice(cents: number)` used `¥` and consumed CNY fen. The replacements are byte-equivalent for the CNY path (`formatPriceCNY`). All existing call sites become broken on the next build — that's expected; Task 3 rewires them.

- [ ] **Step 2: Add `DisplayCurrency` type + state**

Inside the `SubscriptionPage` component body, after the existing `useState`/hooks block (around the `useCreateOrder` line), add the imports if not already present (`useEffect` is already imported), and add the new state:

```tsx
type DisplayCurrency = "CNY" | "USD";

const [displayCurrency, setDisplayCurrency] = useState<DisplayCurrency>(() => {
  const c = activeSub?.currency;
  return c === "USD" ? "USD" : "CNY";
});

useEffect(() => {
  if (activeSub?.currency === "USD") setDisplayCurrency("USD");
  else if (activeSub?.currency === "CNY") setDisplayCurrency("CNY");
}, [activeSub?.currency]);
```

**Placement matters**: `activeSub` is declared at line ~129 (`const activeSub = subscriptions.find(...)`). The new `useState`+`useEffect` block must come AFTER that line, because the initializer reads `activeSub`. Place it immediately after `activeSubPlan` is declared.

- [ ] **Step 3: Add the toggle-aware plan-grid helper**

Below the new state, add the toggle-driven helper that the plan grid will use:

```tsx
const formatPrice = (plan: Plan) =>
  formatPriceForCurrency(plan, displayCurrency);
```

Use a `const` arrow function (not `function`) so it closes over `displayCurrency` from component scope.

- [ ] **Step 4: Type-check**

Run: `cd dashboard && pnpm build`
Expected: FAIL with multiple `Argument of type 'number' is not assignable to parameter of type 'Plan'` (the old `formatPrice(o.amount)` / `formatPrice(plan.price_cny_fen)` call sites are now broken because the signature changed). This is your worklist for Task 3.

Do NOT fix the build here. Task 3 rewires every site explicitly. Leaving the build red lets the next implementer see the exact list.

- [ ] **Step 5: Commit**

```bash
git add dashboard/src/pages/subscriptions/SubscriptionPage.tsx
git commit -m "feat(dashboard): add displayCurrency state + dual-currency price helpers (WIP, build red)"
```

Note the "WIP" tag — the build is intentionally broken between this commit and Task 3. If your reviewer dislikes red commits, squash Tasks 2 + 3 at PR-merge time.

---

## Task 3: Wire toggle to plan-grid render + fix all `formatPrice` call sites

**Files:**
- Modify: `dashboard/src/pages/subscriptions/SubscriptionPage.tsx`

**Interfaces:**
- Consumes: Task 2 (`displayCurrency`, `setDisplayCurrency`, `formatPrice(plan)`, `formatPriceForCurrency`, `formatPriceCNY`).
- Produces: All non-channel-dependent price rendering switched to the new helpers. Plan grid follows the toggle; current-plan reference always uses subscription currency; order-history table always renders CNY (orders carry their own `currency` field — see Step 6 note).

The channel-dependent sites (dialog "Price", dialog "Estimated Total", payment-result "Amount") are touched in Task 5 after `PaymentChannel` gains `"stripe"`. This task fixes the toggle-or-subscription-driven sites.

- [ ] **Step 1: Render the toggle alongside "Available Plans" heading**

Locate the "Available Plans" heading. In current code it appears as a section above the plan grid. Find the line that renders this heading (search for `Available Plans` literal). Wrap heading + toggle in a flex row:

```tsx
<div className="flex items-center justify-between">
  <h3 className="text-sm font-medium text-muted-foreground uppercase tracking-wider">
    Available Plans
  </h3>
  <div className="flex gap-1">
    <Button
      size="sm"
      variant={displayCurrency === "CNY" ? "default" : "outline"}
      onClick={() => setDisplayCurrency("CNY")}
    >
      ¥ CNY
    </Button>
    <Button
      size="sm"
      variant={displayCurrency === "USD" ? "default" : "outline"}
      onClick={() => setDisplayCurrency("USD")}
    >
      $ USD
    </Button>
  </div>
</div>
```

Match the existing heading's exact `className` (other section headings on this page use `text-sm font-medium text-muted-foreground uppercase tracking-wider`). If the literal text is wrapped differently, preserve its existing wrapper and just put the toggle on the same row.

- [ ] **Step 2: Fix plan-card big-price render (~line 401)**

Current:
```tsx
<span className="text-2xl font-bold">{formatPrice(plan.price_cny_fen)}</span>
{plan.price_cny_fen > 0 && (
  <span className="text-sm text-muted-foreground">
    /{plan.period_months === 1 ? "mo" : `${plan.period_months}mo`}
  </span>
)}
```

Replace with:
```tsx
<span className="text-2xl font-bold">{formatPrice(plan)}</span>
{(displayCurrency === "USD" ? plan.price_usd_cents : plan.price_cny_fen) > 0 && (
  <span className="text-sm text-muted-foreground">
    /{plan.period_months === 1 ? "mo" : `${plan.period_months}mo`}
  </span>
)}
```

The conditional on "/mo" must use the SAME currency as the price label, otherwise a USD-only plan would show "Free" in the toggle-CNY view but still render "/mo".

- [ ] **Step 3: Fix dialog "Current Plan" row (~line 508)**

Current:
```tsx
({formatPrice(activeSubPlan.price_cny_fen)}/
{activeSubPlan.period_months === 1 ? "mo" : `${activeSubPlan.period_months}mo`})
```

Replace with:
```tsx
({formatPriceForCurrency(activeSubPlan, (activeSub?.currency || "CNY") as "CNY" | "USD")}/
{activeSubPlan.period_months === 1 ? "mo" : `${activeSubPlan.period_months}mo`})
```

The "current plan" reference always honors the subscription's own currency at purchase time, never the toggle.

- [ ] **Step 4: Fix order-history table "Amount" column (~line 216)**

Current:
```tsx
{
  header: "Amount",
  accessor: (o) => formatPrice(o.amount),
},
```

Orders carry their own `currency` field (`Order.currency: "CNY" | "USD"` — already declared in the existing `Order` type because the backend has always sent it). Use it directly:

```tsx
{
  header: "Amount",
  accessor: (o) =>
    o.currency === "USD"
      ? formatPriceUSD(o.amount)
      : formatPriceCNY(o.amount),
},
```

If the existing `Order` interface doesn't have `currency` typed, declare it in `dashboard/src/api/types.ts`'s `Order` interface as `currency: "CNY" | "USD"` (the field is required at order creation, never nullable). Quick check:

```bash
grep -n "currency" dashboard/src/api/types.ts
```

If `Order.currency` isn't listed, add it now in the same file as Task 1's `Subscription.currency` edit. (It's a one-line addition; group it under this step's commit.)

- [ ] **Step 5: Verify Task 5's sites remain broken intentionally**

Run: `cd dashboard && pnpm build`
Expected: still FAIL, but the only remaining errors should be in the dialog "Price" line (~499), "Estimated Total" block (~555-571), and the payment-result "Amount" lines (~603, ~621). These are channel-dependent and handled in Task 5.

If the build now passes, you missed one of Step 2–4's sites. Re-grep:
```bash
grep -n "formatPrice" dashboard/src/pages/subscriptions/SubscriptionPage.tsx
```
Every remaining hit should be either inside the dialog "Price"/"Estimated Total"/payment-result blocks (left for Task 5) or the function definitions in Task 2.

- [ ] **Step 6: Commit**

```bash
git add dashboard/src/pages/subscriptions/SubscriptionPage.tsx \
        dashboard/src/api/types.ts  # only if Step 4 added Order.currency
git commit -m "feat(dashboard): wire currency toggle to plan grid + fix non-channel price sites"
```

---

## Task 4: Add `stripe` to `PaymentChannel` + dialog-channel-aware price helpers

**Files:**
- Modify: `dashboard/src/pages/subscriptions/SubscriptionPage.tsx`

**Interfaces:**
- Consumes: Task 2 (`formatPriceCNY`, `formatPriceUSD`).
- Produces:
  - `type PaymentChannel = "wechat" | "alipay" | "stripe"`
  - `getPriceForChannel(plan: Plan, ch: PaymentChannel): number` → returns the raw integer in the channel's currency
  - `formatPriceForChannel(plan: Plan, ch: PaymentChannel): string` → formatted display
  - `channelOptions` array driving the three dialog buttons
  - `lockedCurrency` derived value
  - `pickInitialChannel()` function returning a default that honors the lock

  The three new buttons and the lock-enforcement render land in Task 5; this task only sets up the types and pure helpers.

- [ ] **Step 1: Extend `PaymentChannel`**

Locate the existing `PaymentChannel` type (around line 80) and add `"stripe"`:

```tsx
type PaymentChannel = "wechat" | "alipay" | "stripe";
```

- [ ] **Step 2: Add the channel-aware helpers**

Below the `formatPriceForCurrency` helper from Task 2, add:

```tsx
const getPriceForChannel = (plan: Plan, ch: PaymentChannel): number =>
  ch === "stripe" ? plan.price_usd_cents : plan.price_cny_fen;

const formatPriceForChannel = (plan: Plan, ch: PaymentChannel): string =>
  ch === "stripe"
    ? formatPriceUSD(plan.price_usd_cents)
    : formatPriceCNY(plan.price_cny_fen);
```

These are pure functions — no closures over component state.

- [ ] **Step 3: Add `channelOptions`, `lockedCurrency`, `pickInitialChannel` inside the component**

Inside `SubscriptionPage` body, after `activeSubPlan` and after the `displayCurrency` state from Task 2, add:

```tsx
const channelOptions = [
  { value: "wechat" as const, label: "WeChat Pay", currency: "CNY" as const },
  { value: "alipay" as const, label: "Alipay",     currency: "CNY" as const },
  { value: "stripe" as const, label: "Stripe",     currency: "USD" as const },
];

// "" / undefined means unlocked (free or never-paid)
const lockedCurrency = (activeSub?.currency || "") as "CNY" | "USD" | "";

function pickInitialChannel(): PaymentChannel {
  if (lockedCurrency === "USD") return "stripe";
  return "wechat"; // CNY-locked or unlocked — sensible CN default
}
```

Place this immediately after the `displayCurrency` `useEffect` to keep all the currency-related declarations together.

- [ ] **Step 4: Type-check**

Run: `cd dashboard && pnpm build`
Expected: still FAIL (Task 5 dialog/handler updates still pending), but no NEW errors from the type widening (`"wechat" | "alipay"` → `"wechat" | "alipay" | "stripe"` is a superset, all existing `setChannel("wechat")` etc. stay valid).

- [ ] **Step 5: Commit**

```bash
git add dashboard/src/pages/subscriptions/SubscriptionPage.tsx
git commit -m "feat(dashboard): extend PaymentChannel with stripe + add channel pricing helpers"
```

---

## Task 5: Render three-button dialog + lock enforcement + Stripe handlePay redirect

**Files:**
- Modify: `dashboard/src/pages/subscriptions/SubscriptionPage.tsx`

**Interfaces:**
- Consumes: Tasks 2 + 4 (`displayCurrency`, `channelOptions`, `lockedCurrency`, `pickInitialChannel`, `getPriceForChannel`, `formatPriceForChannel`).
- Produces: Build green. User-facing change complete.

- [ ] **Step 1: Replace the two-button channel selector with the three-button render**

Locate the existing payment-method block (around line 528–549, the `{/* Payment channel selector */}` comment marks it). Replace the inner `<div className="grid grid-cols-2 gap-2">` block (the two hardcoded buttons) with:

```tsx
<div className="space-y-2">
  <Label>Payment Method</Label>
  <div className="grid grid-cols-3 gap-2">
    {channelOptions.map((opt) => {
      const locked = lockedCurrency !== "" && lockedCurrency !== opt.currency;
      return (
        <Button
          key={opt.value}
          type="button"
          variant={channel === opt.value ? "default" : "outline"}
          className="w-full"
          disabled={locked}
          title={
            locked
              ? `Current subscription is in ${lockedCurrency}; switching currency requires the subscription to expire first.`
              : undefined
          }
          onClick={() => setChannel(opt.value)}
        >
          {opt.label}
        </Button>
      );
    })}
  </div>
  {lockedCurrency !== "" && (
    <p className="text-xs text-muted-foreground">
      Locked to {lockedCurrency} — current paid subscription pins the currency.
    </p>
  )}
</div>
```

Keep the outer `<Label>Payment Method</Label>` line that already exists; you're only replacing the inner two-button grid.

- [ ] **Step 2: Replace all `setChannel("wechat")` reset sites with `pickInitialChannel()`**

There are four such sites in this file. Grep them:

```bash
grep -n 'setChannel("wechat")' dashboard/src/pages/subscriptions/SubscriptionPage.tsx
```

Expected: 4 hits. Locations and replacements:

1. Initial `useState` (around line 102):
   ```tsx
   const [channel, setChannel] = useState<PaymentChannel>(pickInitialChannel());
   ```
   **Note**: `pickInitialChannel` reads `activeSub` which is computed after the `useState` calls. Use a lazy initializer instead:
   ```tsx
   const [channel, setChannel] = useState<PaymentChannel>("wechat");
   ```
   and add a `useEffect` after `pickInitialChannel` is defined to sync once `activeSub` arrives:
   ```tsx
   useEffect(() => {
     setChannel(pickInitialChannel());
     // eslint-disable-next-line react-hooks/exhaustive-deps
   }, [activeSub?.currency]);
   ```
   The dependency is `activeSub?.currency` (not `activeSub`) so it doesn't re-fire on every subscription poll. Suppress the exhaustive-deps lint with a one-line comment because `pickInitialChannel` is a stable function defined in component scope but ESLint can't always tell.

2. `closeDialog` (around line 185):
   ```tsx
   setChannel(pickInitialChannel());
   ```

3. Inline open from "Current Subscription" Renew button (around line 306):
   ```tsx
   setChannel(pickInitialChannel());
   ```

4. Plan-card open (around line 416):
   ```tsx
   setChannel(pickInitialChannel());
   ```

- [ ] **Step 3: Update `openPaymentDialog` reopen channel inference (around line 190)**

Current code coerces unknown channels to `"alipay"`:

```tsx
const ch: PaymentChannel = order.channel === "wechat" ? "wechat" : "alipay";
```

Replace with explicit three-way:

```tsx
const ch: PaymentChannel =
  order.channel === "wechat" || order.channel === "alipay" || order.channel === "stripe"
    ? order.channel
    : "wechat";
```

Note that re-opening a stripe order from history is a UX edge case — the user would have been redirected to `checkout.stripe.com` originally. If they click the "Open payment link" icon for a `paying` Stripe order, the dialog opens, but the existing `paymentResult` render block (Task 5 Step 5 below) has no `stripe` case — it shows an empty body. The "Open payment link" icon on the order history table for Stripe orders would be better handled as a direct redirect; see Step 6.

- [ ] **Step 4: Fix dialog "Price" row (~line 499) — channel-driven**

Current:
```tsx
{formatPrice(dialogPlan.price_cny_fen)}/
{dialogPlan.period_months === 1 ? "mo" : `${dialogPlan.period_months}mo`}
```

Replace with:
```tsx
{formatPriceForChannel(dialogPlan, channel)}/
{dialogPlan.period_months === 1 ? "mo" : `${dialogPlan.period_months}mo`}
```

- [ ] **Step 5: Fix dialog "Estimated Total" block (~line 553–571) — channel-driven**

Current:
```tsx
<span className="font-medium">
  {isFreePlan || isRenewal
    ? formatPrice(dialogPlan.price_cny_fen * periods)
    : (() => {
        const currentPrice = activeSubPlan?.price_cny_fen ?? 0;
        if (!activeSub?.starts_at || !activeSub?.expires_at) {
          return formatPrice(Math.max(dialogPlan.price_cny_fen - currentPrice, 0));
        }
        const now = Date.now();
        const startsAt = new Date(activeSub.starts_at).getTime();
        const expiresAt = new Date(activeSub.expires_at).getTime();
        const totalDuration = expiresAt - startsAt;
        const usedDuration = now - startsAt;
        let remainingValue = 0;
        if (totalDuration > 0 && usedDuration < totalDuration) {
          remainingValue = Math.round(((totalDuration - usedDuration) / totalDuration) * currentPrice);
        }
        return formatPrice(Math.max(dialogPlan.price_cny_fen - remainingValue, 0));
      })()}
</span>
```

Replace with a channel-aware version that computes both `dialogBase` and `activeBase` from `getPriceForChannel`:

```tsx
<span className="font-medium">
  {(() => {
    const dialogBase = getPriceForChannel(dialogPlan, channel);
    const activeBase = activeSubPlan ? getPriceForChannel(activeSubPlan, channel) : 0;
    const renderPrice = (v: number) =>
      channel === "stripe" ? formatPriceUSD(v) : formatPriceCNY(v);

    if (isFreePlan || isRenewal) {
      return renderPrice(dialogBase * periods);
    }
    if (!activeSub?.starts_at || !activeSub?.expires_at) {
      return renderPrice(Math.max(dialogBase - activeBase, 0));
    }
    const now = Date.now();
    const startsAt = new Date(activeSub.starts_at).getTime();
    const expiresAt = new Date(activeSub.expires_at).getTime();
    const totalDuration = expiresAt - startsAt;
    const usedDuration = now - startsAt;
    let remainingValue = 0;
    if (totalDuration > 0 && usedDuration < totalDuration) {
      remainingValue = Math.round(((totalDuration - usedDuration) / totalDuration) * activeBase);
    }
    return renderPrice(Math.max(dialogBase - remainingValue, 0));
  })()}
</span>
```

The numeric math is unchanged — only the prices fed in change. Because the currency lock guarantees same-currency upgrades (backend enforces 409 `currency_mismatch` otherwise), `dialogBase` and `activeBase` are always in the same units, and the proration math is sound.

- [ ] **Step 6: Add Stripe branch to `handlePay`**

Locate `handlePay` (around line 149). Add a Stripe branch BEFORE setting `paymentResult`:

```tsx
async function handlePay() {
  if (!upgradeDialog) return;
  try {
    const result = await createOrder.mutateAsync({
      plan_slug: upgradeDialog.slug,
      periods,
      channel,
    });
    const order = result.data;
    if (channel === "stripe") {
      if (!order.payment_url) {
        toast.error("Stripe checkout URL missing");
        return;
      }
      window.location.href = order.payment_url;
      return; // unreachable on success
    }
    if (order.payment_url) {
      setPaymentResult({ order, channel });
      setDialogStep("paying");
    } else {
      toast.success("Order created successfully");
      closeDialog();
    }
  } catch (e: unknown) {
    const msg = e instanceof Error ? e.message : "Failed to create order";
    toast.error(msg);
  }
}
```

The Stripe branch leaves the page; nothing after that runs. The existing wechat/alipay flow is unchanged.

- [ ] **Step 7: Also short-circuit `openPaymentDialog` for stripe**

Reopening a stripe order from history (Task 5 Step 3) lands in a dialog with no `paymentResult` render branch. Better UX: redirect to the stored `payment_url` immediately. Replace `openPaymentDialog` (around line 189):

```tsx
function openPaymentDialog(order: Order) {
  if (order.channel === "stripe") {
    if (order.payment_url) {
      window.location.href = order.payment_url;
    } else {
      toast.error("Stripe checkout URL missing");
    }
    return;
  }
  const ch: PaymentChannel =
    order.channel === "wechat" || order.channel === "alipay"
      ? order.channel
      : "wechat";
  setPaymentResult({ order, channel: ch });
  setDialogStep("paying");
  setUpgradeDialog(plans.find((p: Plan) => p.id === order.plan_id) ?? null);
}
```

This supersedes Task 5 Step 3's edit (it's the same function — apply this version as the final form).

- [ ] **Step 8: Fix payment-result "Amount" rendering (~lines 603 & 621)**

These two lines render the amount on the WeChat QR card and the Alipay iframe card. Both still use the old `formatPrice(paymentResult.order.amount)`. Replace each with:

```tsx
Amount: {paymentResult.channel === "stripe"
  ? formatPriceUSD(paymentResult.order.amount)
  : formatPriceCNY(paymentResult.order.amount)}
```

(The `paymentResult.channel === "stripe"` branch is unreachable in practice — Stripe redirects away before this block ever renders — but the conditional makes the code self-documenting and avoids a future bug if someone repurposes the block.)

- [ ] **Step 9: Type-check + build**

Run: `cd dashboard && pnpm build`
Expected: PASS with zero TypeScript errors.

If anything still complains about `formatPrice`, grep:
```bash
grep -n 'formatPrice(' dashboard/src/pages/subscriptions/SubscriptionPage.tsx
```
Every remaining hit must call one of:
- `formatPrice(plan)` (toggle-driven)
- `formatPriceCNY(...)` / `formatPriceUSD(...)` (currency-explicit)
- `formatPriceForCurrency(plan, cur)` / `formatPriceForChannel(plan, ch)`

No remaining call like `formatPrice(someNumber)` (the old signature).

- [ ] **Step 10: Commit**

```bash
git add dashboard/src/pages/subscriptions/SubscriptionPage.tsx
git commit -m "feat(dashboard): three-button dialog with currency lock + Stripe redirect"
```

---

## Task 6: Smoke test + open PR

**Files:** none (verification + PR).

**Interfaces:** none.

This task is a gate, not code. Run through the spec's manual smoke checklist before opening the PR.

- [ ] **Step 1: Final build sanity**

```bash
cd dashboard && pnpm build
```
Expected: PASS, no TS errors. `dist/` populated.

Also grep for stale references:
```bash
grep -rn "price_per_period" dashboard/src/
grep -n 'formatPrice(' dashboard/src/pages/subscriptions/SubscriptionPage.tsx | grep -v 'formatPrice(plan)' | grep -v 'formatPriceCNY\|formatPriceUSD\|formatPriceForCurrency\|formatPriceForChannel'
```
Expected: both empty.

- [ ] **Step 2: Manual smoke (semi-automated)**

Bring up the dashboard locally (whatever the dev convention is — typically `cd dashboard && pnpm dev`) and walk through each scenario from the spec's §7 testing block:

1. **Free project, default view**:
   - Toggle defaults to CNY.
   - Plan cards show `¥`.
   - Switch toggle to USD → cards show `$`.
   - Open the upgrade dialog → all three channel buttons enabled, default highlighted is "WeChat Pay".
2. **Stripe pay path** (requires Stripe `test` mode keys on payserver):
   - Select Stripe → click "Pay" → browser navigates to `checkout.stripe.com/c/cs_test_…`.
   - Pay with `4242 4242 4242 4242`, any future expiry, any CVC.
   - Stripe redirects back to the configured `success_url`.
   - Subscription reflects active with USD currency.
3. **CNY-locked project**:
   - Top toggle defaults to CNY.
   - Open dialog → "Stripe" button is grey/disabled. Hover → native tooltip appears with the lock message.
   - Helper text "Locked to CNY — current paid subscription pins the currency." renders below the buttons.
4. **USD-locked project** (synthesized via the smoke step 2 above):
   - Top toggle defaults to USD.
   - Open dialog → "WeChat Pay" and "Alipay" buttons are disabled with tooltips; "Stripe" is enabled and selected by default.
5. **No regression on WeChat / Alipay**:
   - Free project → select WeChat → click Pay → QR code renders as before.
   - Free project → select Alipay → click Pay → iframe renders as before.

If any scenario fails, fix the regression before continuing.

- [ ] **Step 3: Push the branch and open the PR**

```bash
git push -u origin feat/subscription-page-stripe
```

PR body (paste into `gh pr create`):

```
## Summary

Wires PR #44's backend Stripe channel into the dashboard subscription page so users can actually pick it.

- Spec: docs/superpowers/specs/2026-06-19-subscription-page-stripe-design.md

## What's in the box

- Currency display toggle (CNY / USD) beside the "Available Plans" heading. Default follows `activeSub.currency`.
- Third payment-method button "Stripe" in the upgrade dialog.
- Currency lock: the two channels whose currency conflicts with the project's active paid currency are `disabled` with a native HTML tooltip explaining the lock.
- Stripe path: full-page `window.location.href` redirect to `checkout.stripe.com`. Reopening a paying Stripe order from the order history also redirects rather than opening the empty dialog.
- All price rendering split into channel-aware / subscription-aware / toggle-aware helpers so the dialog can never display one currency while charging another.

## Touched files

- `dashboard/src/api/types.ts` — declare `Subscription.currency` (backend already emits it via PR #44)
- `dashboard/src/pages/subscriptions/SubscriptionPage.tsx` — all UI changes

## Out of scope

- No `/payment/success` landing page — reuse the existing `/subscription` route.
- No `extra_usage_topup` Stripe wiring (tracked as PR #44 Follow-up F2).
- No localization of tooltips / labels (current UI is English).

## Test verification

- `cd dashboard && pnpm build` clean.
- Manual smoke checklist passed (CNY default, USD default, lock UX both directions, WeChat QR / Alipay iframe regression-free).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
```

Open the PR:

```bash
gh pr create --base main --head feat/subscription-page-stripe \
  --title "feat(dashboard): subscription page Stripe channel + multi-currency display" \
  --body-file /tmp/sub-page-stripe-pr-body.md
```

(Adjust the `--body-file` path — paste the body into a tempfile first.)

- [ ] **Step 4: Final commit (if any cleanup landed)**

If the smoke test surfaced a fixup, commit it with a descriptive message. Otherwise this step is a no-op.

---

## Self-Review

**1. Spec coverage:**

| Spec §                                  | Task(s)            |
|------------------------------------------|--------------------|
| §1 File changes                          | All tasks          |
| §2 Currency toggle (state + helpers)     | 2                  |
| §3 Plan-grid price rendering (5 sites)   | 3 (sites 1, 3, 4) + 5 (sites 2, 4-Estimated, 5-Amount) |
| §4 Dialog channel buttons + lock         | 4 (types/helpers) + 5 (render) |
| §5 Pay handler + Stripe redirect         | 5                  |
| §6 Subscription TS interface             | 1                  |
| §7 Testing                               | 6                  |
| Out-of-scope (Future Work)               | non-implementing   |

The spec's §3 lists 5 rendering sites; this plan additionally fixes a 6th site (order-history "Amount" in Task 3 Step 4) that the spec missed. The spec's intent matches — orders carry their own currency and should render accordingly — so this is plan-level correction within the design's spirit.

Also fixes `openPaymentDialog`'s reopen behavior for stripe (Task 5 Step 7), which the spec didn't enumerate but is the obvious consequence of having stripe orders in the order history.

**2. Placeholder scan:** No "TBD", "TODO", "implement later", or vague handwaves. Every step has explicit code or commands. The few "see step N" cross-references (Task 5 Step 7 supersedes Step 3, Task 3 Step 4's potential `Order.currency` add) are explicit not deferred.

**3. Type consistency:**

- `DisplayCurrency = "CNY" | "USD"` — used in Tasks 2 (state) and 3 (heading toggle render).
- `PaymentChannel = "wechat" | "alipay" | "stripe"` — defined in Task 4, consumed in Tasks 5 + 6.
- `formatPriceCNY` / `formatPriceUSD` — defined in Task 2 Step 1, used everywhere else.
- `formatPrice(plan: Plan)` — defined Task 2 Step 3, consumed Task 3 Step 2.
- `formatPriceForCurrency(plan, cur)` — defined Task 2 Step 1, consumed Task 3 Step 3.
- `getPriceForChannel(plan, ch)` / `formatPriceForChannel(plan, ch)` — defined Task 4 Step 2, consumed Task 5 Steps 4, 5.
- `channelOptions`, `lockedCurrency`, `pickInitialChannel` — defined Task 4 Step 3, consumed Task 5 Steps 1, 2.
- `Subscription.currency?: "CNY" | "USD" | ""` — Task 1, consumed by `activeSub?.currency` reads in Tasks 2 (initializer + effect) + 4 (`lockedCurrency` derivation).

All signatures consistent across the tasks that use them.
