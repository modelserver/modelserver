# SubscriptionPage Stripe Channel + Multi-Currency Display Design

## Overview

The backend Stripe channel (PR #44, commit 3ecba39) is live, but
`dashboard/src/pages/subscriptions/SubscriptionPage.tsx` still hardcodes
WeChat / Alipay as the only two payment options. Users on the dashboard
cannot pick Stripe, so Stripe is effectively dead-on-arrival despite the
payserver being configured. This spec wires Stripe into the subscription
page, adds a CNY / USD currency display toggle, and enforces the
per-project currency lock with a tooltip-explained disabled button.

Scoped to two files: `dashboard/src/pages/subscriptions/SubscriptionPage.tsx`
and `dashboard/src/api/types.ts`. No backend changes — `subscriptions.currency`
(JSON tag `currency,omitempty`) was already added in PR #44.

## Confirmed Decisions

| Topic | Decision |
|---|---|
| Stripe payment UX | `window.location.href = payment_url` full-page redirect (Stripe's official recommendation; no iframe, no popup) |
| Currency lock display | Stripe / WeChat / Alipay buttons all visible in the dialog; the ones whose currency conflicts with `activeSub.currency` are `disabled` with a native HTML `title=""` tooltip |
| Currency toggle scope | A `[¥ CNY | $ USD]` toggle sits beside the "Available Plans" heading. It controls plan-card price display ONLY |
| Toggle default | Follows `activeSub.currency`; falls back to `"CNY"` when free / never-paid |
| Toggle ↔ channel buttons | Independent. The toggle is a display lens; channel buttons enforce the real currency lock. Avoids "looking at $20 but paying ¥120" confusion |
| Tooltip implementation | Native HTML `title` attribute — zero new dependency |
| Locale of tooltip / labels | English, matches existing button copy ("WeChat Pay", "Renew", "Pay") |

## §1 — File Changes

```
dashboard/src/
├── api/types.ts                          # Subscription interface gains currency?: "CNY" | "USD" | ""
├── pages/subscriptions/
│   └── SubscriptionPage.tsx              # All UI changes (see below)
```

Two files, no backend touch, no migration, no test infrastructure changes.

## §2 — Currency Toggle (display lens)

Placed beside the "Available Plans" heading so it visually owns the plan
grid. Default follows `activeSub.currency`; resets when the sub loads
asynchronously.

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

Render (two adjacent Buttons in a flex row, no new component imported):

```tsx
<div className="flex items-center justify-between">
  <h3>Available Plans</h3>
  <div className="flex gap-1">
    <Button
      size="sm"
      variant={displayCurrency === "CNY" ? "default" : "outline"}
      onClick={() => setDisplayCurrency("CNY")}
    >¥ CNY</Button>
    <Button
      size="sm"
      variant={displayCurrency === "USD" ? "default" : "outline"}
      onClick={() => setDisplayCurrency("USD")}
    >$ USD</Button>
  </div>
</div>
```

The toggle is **never** disabled by the lock — it is only a display lens.
Real enforcement happens in the dialog.

Helpers:

```tsx
const formatPriceCNY = (fen: number) =>
  fen === 0 ? "Free" : `¥${(fen / 100).toFixed(2)}`;
const formatPriceUSD = (cents: number) =>
  cents === 0 ? "Free" : `$${(cents / 100).toFixed(2)}`;

// Plan-grid display, follows the toggle
const formatPrice = (plan: Plan) =>
  displayCurrency === "USD"
    ? formatPriceUSD(plan.price_usd_cents)
    : formatPriceCNY(plan.price_cny_fen);

// Dialog uses the *selected channel*, NOT the toggle
const getPriceForChannel = (plan: Plan, ch: PaymentChannel): number =>
  ch === "stripe" ? plan.price_usd_cents : plan.price_cny_fen;
const formatPriceForChannel = (plan: Plan, ch: PaymentChannel) =>
  ch === "stripe"
    ? formatPriceUSD(plan.price_usd_cents)
    : formatPriceCNY(plan.price_cny_fen);

// Currency lookup used for the "Current Plan" line — always honors the
// subscription's own currency, never the toggle
const formatPriceForCurrency = (plan: Plan, cur: "CNY" | "USD") =>
  cur === "USD"
    ? formatPriceUSD(plan.price_usd_cents)
    : formatPriceCNY(plan.price_cny_fen);
```

## §3 — Plan Grid Price Rendering

Five sites currently call `formatPrice(plan.price_cny_fen)`; rewrite them
with the per-context helper from §2.

1. **Plan card big price (line ~401)** — uses the toggle:
   ```tsx
   <span className="text-2xl font-bold">{formatPrice(plan)}</span>
   {(displayCurrency === "USD" ? plan.price_usd_cents : plan.price_cny_fen) > 0 && (
     <span className="text-sm text-muted-foreground">
       /{plan.period_months === 1 ? "mo" : `${plan.period_months}mo`}
     </span>
   )}
   ```

2. **Dialog "Price" row (line ~499)** — uses the dialog's selected channel:
   ```tsx
   {formatPriceForChannel(dialogPlan, channel)}/
   {dialogPlan.period_months === 1 ? "mo" : `${dialogPlan.period_months}mo`}
   ```

3. **Dialog "Current Plan" row (line ~508)** — uses `activeSub.currency`
   (the subscription's own currency at the time it was paid for):
   ```tsx
   ({formatPriceForCurrency(activeSubPlan, (activeSub?.currency || "CNY") as "CNY" | "USD")}/
   {activeSubPlan.period_months === 1 ? "mo" : `${activeSubPlan.period_months}mo`})
   ```

4. **Dialog "Estimated Total" (lines ~555 / 559 / 570)** — proration uses
   the selected-channel price for both `dialogPlan` and `activeSubPlan`.
   The backend's currency lock guarantees same-currency upgrades only:
   ```tsx
   const dialogBase = getPriceForChannel(dialogPlan, channel);
   const activeBase = activeSubPlan
     ? getPriceForChannel(activeSubPlan, channel)
     : 0;
   // existing proration math with dialogBase / activeBase
   {isFreePlan || isRenewal
     ? formatPriceForChannel(dialogPlan, channel) // displays plan price * periods inline
     : ...}
   ```

5. **`paymentResult` "Amount" rendering (line ~603 / 621)** — uses
   `paymentResult.channel`:
   ```tsx
   Amount: {paymentResult.channel === "stripe"
     ? formatPriceUSD(paymentResult.order.amount)
     : formatPriceCNY(paymentResult.order.amount)}
   ```

**Invariant**: the toggle never affects dialog math. The dialog always
trusts (a) the user's channel pick for new orders, and (b) the
subscription's own currency for current-plan reference.

## §4 — Dialog Channel Buttons + Currency Lock

```tsx
type PaymentChannel = "wechat" | "alipay" | "stripe";

const channelOptions = [
  { value: "wechat" as const, label: "WeChat Pay", currency: "CNY" as const },
  { value: "alipay" as const, label: "Alipay",     currency: "CNY" as const },
  { value: "stripe" as const, label: "Stripe",     currency: "USD" as const },
];

// "" / undefined means unlocked (free or never-paid)
const lockedCurrency = (activeSub?.currency || "") as "CNY" | "USD" | "";

// Initial channel pick when opening the dialog
function pickInitialChannel(): PaymentChannel {
  if (lockedCurrency === "USD") return "stripe";
  return "wechat"; // CNY-locked or unlocked — sensible CN default
}
```

Render:

```tsx
<div className="space-y-2">
  <Label>Payment Method</Label>
  <div className="grid grid-cols-3 gap-2">
    {channelOptions.map(opt => {
      const locked = lockedCurrency !== "" && lockedCurrency !== opt.currency;
      return (
        <Button
          key={opt.value}
          type="button"
          variant={channel === opt.value ? "default" : "outline"}
          className="w-full"
          disabled={locked}
          title={locked
            ? `Current subscription is in ${lockedCurrency}; switch currency requires the subscription to expire first.`
            : undefined}
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

Four existing `setChannel("wechat")` sites (dialog open / cancel reset /
post-pay reset) all change to `setChannel(pickInitialChannel())`.

## §5 — "Pay" Handler + Stripe Redirect

Stripe Checkout is hosted — the order's `payment_url` is
`https://checkout.stripe.com/c/cs_…`. Stripe explicitly recommends a
full-page redirect (not iframe, not popup). So `channel === "stripe"`
takes a separate branch that bypasses the existing "paying" dialog step.

```tsx
async function handlePay() {
  if (!dialogPlan) return;
  try {
    const order = await createOrder.mutateAsync({
      plan_slug: dialogPlan.slug,
      periods,
      channel,
    });
    if (channel === "stripe") {
      if (!order.payment_url) {
        toast.error("Stripe checkout URL missing");
        return;
      }
      window.location.href = order.payment_url;
      return; // unreachable on success
    }
    setPaymentResult({ order, channel });
    setDialogStep("paying");
  } catch (err) {
    // existing error toast surfacing
  }
}
```

If the current code uses `createOrder.mutate(..., { onSuccess })` rather
than `mutateAsync`, port the same shape — the call signature is a local
adaptation. The behavioral contract is what matters: on Stripe success,
`window.location.href = order.payment_url` before any further state
manipulation.

The `paymentResult` rendering block (existing WeChat QR + Alipay iframe
branches) needs no new `stripe` case — the user has navigated away.

When Stripe redirects back to `success_url` after payment, the user lands
on whatever URL the operator configured for `billing.return_url` (often
`/subscription` itself). The subscription will be activated via the
webhook → modelserver → DB path that PR #44 already wires.

## §6 — Subscription TS Interface

`dashboard/src/api/types.ts`:

```ts
export interface Subscription {
  id: string;
  project_id: string;
  plan_id?: string;
  plan_name: string;
  status: "active" | "expired" | "revoked";
  starts_at: string;
  expires_at: string;
  currency?: "CNY" | "USD" | "";  // NEW — from PR #44, populated by DeliverOrder. Empty = free / never-paid.
  created_at: string;
  updated_at: string;
}
```

Backend already emits `currency,omitempty`; this is pure TS declaration.

## §7 — Testing & Out of Scope

### Testing

There is no React unit-test harness in the dashboard project today. Gate
on:

- `cd dashboard && pnpm build` — must succeed with zero TypeScript errors.
- Semi-manual smoke checklist:
  1. **Free project, default view**: toggle defaults to CNY; plan cards
     show `¥`. Switch toggle to USD; cards show `$`. Open dialog → all
     three channel buttons enabled, default `wechat`.
  2. **Stripe pay path**: select Stripe, click Pay. Page redirects to
     `checkout.stripe.com`. Pay with test card `4242 4242 4242 4242`.
     Stripe redirects back to `success_url`. Subscription activates.
  3. **CNY-locked project**: toggle defaults to CNY; dialog opens with
     Stripe button disabled and tooltip explaining the lock. Hover the
     button — tooltip text appears.
  4. **USD-locked project** (synthesized via Stripe test-mode purchase):
     toggle defaults to USD; dialog opens with `wechat` / `alipay`
     disabled and tooltip explaining the lock.
  5. **No regression**: WeChat QR scan + Alipay iframe still render
     identically for CNY purchases.

### Out of Scope (Future Work, consistent with PR #44)

- A dedicated `/payment/success` landing page — current spec relies on the
  existing `/subscription` route.
- Stripe Subscription / auto-renewal.
- USD-aware savings analytics in the Usage page.
- Admin "switch currency" escape hatch.
- Plugging Stripe into the extra-usage top-up flow (`extra_usage_topup`)
  — that's PR #44 Follow-up F2 and lives in a separate ticket.
- Localized tooltips / labels (current UI is English).

### Risks

- **`window.location.href` loses in-flight React state**. The user's
  toggle pick, dialog selections, etc. are discarded when Stripe
  redirects. Acceptable for v1 — the order is already created server-side
  before redirect, and the return trip lands on the subscription page
  which re-renders from the server. If we ever need to preserve dialog
  context across redirect, that's a separate routing concern.
- **Toggle / channel-button decoupling could surprise users**. Someone
  toggles to USD to see USD prices, then opens the dialog and finds
  Stripe disabled because they're CNY-locked. The "Locked to CNY"
  helper text under the channel buttons addresses this — they're told
  why.
