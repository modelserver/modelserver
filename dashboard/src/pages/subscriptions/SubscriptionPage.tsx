import { useState, useEffect } from "react";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useAvailablePlans } from "@/api/plans";
import { useSubscriptions, useOrders, useCreateOrder, useCancelOrder, useSubscriptionUsage, useOrderStatus } from "@/api/subscriptions";
import type { Plan, Subscription, Order } from "@/api/types";
import type { CreditWindowStatus } from "@/api/subscriptions";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogDescription,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useQueryClient } from "@tanstack/react-query";
import { Pagination } from "@/components/shared/Pagination";
import { Loader2, Zap, ExternalLink, XCircle, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { QRCodeSVG } from "qrcode.react";

function statusColor(status: string) {
  switch (status) {
    case "active":
      return "default";
    case "expired":
      return "secondary";
    case "revoked":
      return "destructive";
    case "delivered":
    case "paid":
      return "default";
    case "pending":
    case "paying":
      return "secondary";
    case "failed":
    case "cancelled":
      return "destructive";
    default:
      return "outline";
  }
}

function formatDate(d: string) {
  return new Date(d).toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

function formatDateTime(d: string) {
  return new Date(d).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function formatExpiry(d: string) {
  const date = new Date(d);
  const now = new Date();
  const msIn50Years = 50 * 365.25 * 24 * 60 * 60 * 1000;
  if (date.getTime() - now.getTime() > msIn50Years) return "Never";
  return formatDate(d);
}

function formatPriceCNY(fen: number) {
  if (fen === 0) return "Free";
  return `\u00A5${(fen / 100).toFixed(2)}`;
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

const getPriceForChannel = (plan: Plan, ch: PaymentChannel): number =>
  ch === "stripe" ? plan.price_usd_cents : plan.price_cny_fen;

const formatPriceForChannel = (plan: Plan, ch: PaymentChannel): string =>
  ch === "stripe"
    ? formatPriceUSD(plan.price_usd_cents)
    : formatPriceCNY(plan.price_cny_fen);

type PaymentChannel = "wechat" | "alipay" | "stripe";

interface PaymentResult {
  order: Order;
  channel: PaymentChannel;
}

const ORDER_PAGE_SIZE = 10;

// Module-scope so the array reference is stable across renders — `as const`
// keeps the literal types narrow for the dialog's `channel` discriminated render.
const CHANNEL_OPTIONS = [
  { value: "wechat" as const, label: "WeChat Pay", currency: "CNY" as const },
  { value: "alipay" as const, label: "Alipay",     currency: "CNY" as const },
  { value: "stripe" as const, label: "Stripe",     currency: "USD" as const },
];

export function SubscriptionPage() {
  const projectId = useCurrentProject();
  const { data: plansData, isLoading: plansLoading } = useAvailablePlans(projectId);
  const { data: subsData, isLoading: subsLoading } = useSubscriptions(projectId);
  const [orderPage, setOrderPage] = useState(1);
  const { data: ordersData, isLoading: ordersLoading } = useOrders(projectId, orderPage, ORDER_PAGE_SIZE);
  const { data: usageData } = useSubscriptionUsage(projectId);
  const createOrder = useCreateOrder(projectId);
  const cancelOrder = useCancelOrder(projectId);

  const qc = useQueryClient();
  const [upgradeDialog, setUpgradeDialog] = useState<Plan | null>(null);
  const [periods, setPeriods] = useState(1);
  const [channel, setChannel] = useState<PaymentChannel>("wechat");
  const [paymentResult, setPaymentResult] = useState<PaymentResult | null>(null);
  const [dialogStep, setDialogStep] = useState<"form" | "paying">("form");
  const [isRenewal, setIsRenewal] = useState(false);

  const pollingOrderId = dialogStep === "paying" ? paymentResult?.order.id ?? null : null;
  const { data: orderStatusData } = useOrderStatus(projectId, pollingOrderId);

  useEffect(() => {
    if (!orderStatusData?.data) return;
    const status = orderStatusData.data.status;
    if (status === "delivered") {
      toast.success("Payment successful! Your subscription has been updated.");
      closeDialog();
      qc.invalidateQueries({ queryKey: ["subscriptions", projectId] });
      qc.invalidateQueries({ queryKey: ["orders", projectId] });
    } else if (status === "failed" || status === "cancelled") {
      toast.error(`Payment ${status}`);
      closeDialog();
    }
  }, [orderStatusData]);

  const plans = plansData?.data ?? [];
  const subscriptions = subsData?.data ?? [];
  const orders = ordersData?.data ?? [];
  const orderMeta = ordersData?.meta;

  const activeSub = subscriptions.find((s: Subscription) => s.status === "active");
  const isFreePlan = activeSub?.plan_name === "free";
  const activeSubPlan = activeSub
    ? plans.find((p: Plan) => p.slug === activeSub.plan_name)
    : null;

  type DisplayCurrency = "CNY" | "USD";

  const [displayCurrency, setDisplayCurrency] = useState<DisplayCurrency>(() => {
    const c = activeSub?.currency;
    return c === "USD" ? "USD" : "CNY";
  });

  useEffect(() => {
    if (activeSub?.currency === "USD") setDisplayCurrency("USD");
    else if (activeSub?.currency === "CNY") setDisplayCurrency("CNY");
  }, [activeSub?.currency]);

  // "" / undefined means unlocked (free or never-paid)
  const lockedCurrency = (activeSub?.currency || "") as "CNY" | "USD" | "";

  function pickInitialChannel(): PaymentChannel {
    if (lockedCurrency === "USD") return "stripe";
    return "wechat"; // CNY-locked or unlocked — sensible CN default
  }

  useEffect(() => {
    // Skip while a dialog is open — otherwise a background subscription
    // refresh could clobber the user's mid-flight channel pick. The lock
    // helper text + disabled buttons still enforce the rule on submit.
    if (upgradeDialog) return;
    setChannel(pickInitialChannel());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeSub?.currency]);

  const formatPrice = (plan: Plan) =>
    formatPriceForCurrency(plan, displayCurrency);

  function getButtonState(plan: Plan) {
    if (activeSub?.plan_name === plan.slug) {
      if (isFreePlan) {
        return { label: "Current Plan", disabled: true };
      }
      return { label: "Renew", disabled: false };
    }
    const activePlan = plans.find((p: Plan) => p.slug === activeSub?.plan_name);
    if (activePlan && plan.tier_level > activePlan.tier_level) {
      return { label: "Upgrade", disabled: false };
    }
    return { label: "Available after expiry", disabled: true };
  }

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

  async function handleCancel(order: Order) {
    try {
      await cancelOrder.mutateAsync(order.id);
      toast.success("Order cancelled");
    } catch {
      toast.error("Failed to cancel order");
    }
  }

  function closeDialog() {
    setUpgradeDialog(null);
    setPaymentResult(null);
    setDialogStep("form");
    setPeriods(1);
    setChannel(pickInitialChannel());
    setIsRenewal(false);
  }

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

  const orderColumns: Column<Order>[] = [
    {
      header: "Order ID",
      accessor: (o) => (
        <span className="font-mono text-xs">{o.id.slice(0, 8)}</span>
      ),
    },
    {
      header: "Date",
      accessor: (o) => formatDate(o.created_at),
    },
    {
      header: "Plan",
      accessor: (o) => {
        const plan = plans.find((p: Plan) => p.id === o.plan_id);
        return plan?.display_name || plan?.name || o.plan_id;
      },
    },
    {
      header: "Amount",
      accessor: (o) =>
        o.currency === "USD"
          ? formatPriceUSD(o.amount)
          : formatPriceCNY(o.amount),
    },
    {
      header: "Channel",
      accessor: (o) => o.channel ? (
        <span className="capitalize">{o.channel}</span>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    },
    {
      header: "Status",
      accessor: (o) => (
        <Badge variant={statusColor(o.status)}>{o.status}</Badge>
      ),
    },
    {
      header: "",
      accessor: (o) => {
        const canCancel = o.status === "pending" || o.status === "paying";
        return (
          <div className="flex items-center gap-1 justify-end">
            {o.status === "paying" && o.payment_url && (
              <Button variant="ghost" size="icon" className="h-8 w-8" onClick={() => openPaymentDialog(o)} title="Open payment link">
                <ExternalLink className="h-4 w-4" />
              </Button>
            )}
            {canCancel && (
              <Button
                variant="ghost"
                size="icon"
                className="h-8 w-8 text-destructive hover:text-destructive"
                onClick={() => handleCancel(o)}
                disabled={cancelOrder.isPending}
                title="Cancel order"
              >
                <XCircle className="h-4 w-4" />
              </Button>
            )}
          </div>
        );
      },
      className: "w-24",
    },
  ];

  const isLoading = plansLoading || subsLoading;

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 p-6 text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Loading...
      </div>
    );
  }

  const dialogPlan = upgradeDialog;
  const totalPages = orderMeta ? Math.ceil(orderMeta.total / orderMeta.per_page) : 1;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Subscription"
        description="Manage your plan and subscription"
      />

      {/* Current Subscription */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Current Subscription</CardTitle>
        </CardHeader>
        <CardContent>
          {activeSub ? (
            <div className="space-y-4">
              <div className="space-y-2">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-lg">
                    {activeSubPlan?.display_name || activeSubPlan?.name || activeSub.plan_name}
                  </span>
                  <Badge variant={statusColor(activeSub.status)}>{activeSub.status}</Badge>
                  {isFreePlan && <Badge variant="secondary">Free Tier</Badge>}
                  {!isFreePlan && activeSubPlan && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => {
                        setUpgradeDialog(activeSubPlan);
                        setIsRenewal(true);
                        setPeriods(1);
                        setChannel(pickInitialChannel());
                        setPaymentResult(null);
                        setDialogStep("form");
                      }}
                    >
                      <RefreshCw className="mr-1 h-3 w-3" />
                      Renew
                    </Button>
                  )}
                </div>
                {!isFreePlan && (
                  <p className="text-sm text-muted-foreground">
                    {formatDate(activeSub.starts_at)} — {formatExpiry(activeSub.expires_at)}
                  </p>
                )}
                {isFreePlan && (
                  <p className="text-sm text-muted-foreground">
                    Using free tier rate limits. Upgrade to a paid plan for higher limits.
                  </p>
                )}
              </div>
              {usageData?.data && usageData.data.length > 0 && (
                <div className="space-y-3 border-t pt-3">
                  {usageData.data.map((status: CreditWindowStatus) => {
                    const pct = status.percentage;
                    const clampedPct = Math.min(pct, 100);
                    const barColor = pct > 95
                      ? "bg-red-500"
                      : pct > 80
                        ? "bg-yellow-500"
                        : "bg-primary";

                    return (
                      <div key={status.window} className="space-y-1.5">
                        <div className="flex items-center justify-between text-sm">
                          <span className="font-medium">Usage ({status.window})</span>
                          {status.resets_at && (
                            <span className="text-xs text-muted-foreground">
                              Resets {formatDateTime(status.resets_at)}
                            </span>
                          )}
                        </div>
                        <div className="h-2 w-full rounded-full bg-muted overflow-hidden">
                          <div
                            className={`h-full rounded-full transition-all ${barColor}`}
                            style={{ width: `${clampedPct}%` }}
                          />
                        </div>
                        <p className="text-xs text-muted-foreground">
                          {pct.toFixed(2)}% used
                        </p>
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          ) : (
            <div className="flex items-center gap-2">
              <Badge variant="secondary">No Subscription</Badge>
              <span className="text-sm text-muted-foreground">No active plan assigned</span>
            </div>
          )}
        </CardContent>
      </Card>

      {/* Available Plans */}
      <div className="space-y-3">
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
        {plans.length === 0 ? (
          <Card>
            <CardContent className="py-8 text-center text-muted-foreground">
              No plans available for this project.
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {plans.map((plan: Plan) => {
              const btn = getButtonState(plan);
              const isCurrent = activeSub?.plan_name === plan.slug;
              return (
                <Card key={plan.id} className={isCurrent ? "border-primary" : ""}>
                  <CardHeader className="pb-2">
                    <div className="flex items-center justify-between">
                      <CardTitle className="text-base">{plan.display_name || plan.name}</CardTitle>
                      {isCurrent && <Badge>Current</Badge>}
                    </div>
                  </CardHeader>
                  <CardContent className="space-y-3">
                    {plan.description && (
                      <p className="text-sm text-muted-foreground">{plan.description}</p>
                    )}
                    <div>
                      <span className="text-2xl font-bold">{formatPrice(plan)}</span>
                      {(displayCurrency === "USD" ? plan.price_usd_cents : plan.price_cny_fen) > 0 && (
                        <span className="text-sm text-muted-foreground">
                          /{plan.period_months === 1 ? "mo" : `${plan.period_months}mo`}
                        </span>
                      )}
                    </div>
                    <Button
                      className="w-full"
                      variant={!btn.disabled ? "default" : "outline"}
                      disabled={btn.disabled}
                      onClick={() => {
                        setUpgradeDialog(plan);
                        setIsRenewal(btn.label === "Renew");
                        setPeriods(1);
                        setChannel(pickInitialChannel());
                        setPaymentResult(null);
                        setDialogStep("form");
                      }}
                    >
                      {btn.label === "Renew" ? (
                        <RefreshCw className="mr-2 h-4 w-4" />
                      ) : (
                        <Zap className="mr-2 h-4 w-4" />
                      )}
                      {btn.label}
                    </Button>
                  </CardContent>
                </Card>
              );
            })}
          </div>
        )}
      </div>

      {/* Order History */}
      <div className="space-y-3">
        <h3 className="text-sm font-medium text-muted-foreground uppercase tracking-wider">
          Order History
        </h3>
        <Card>
          <CardContent className="p-0">
            {ordersLoading ? (
              <div className="flex items-center gap-2 p-6 text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                Loading...
              </div>
            ) : (
              <>
                <DataTable
                  columns={orderColumns}
                  data={orders}
                  keyFn={(o: Order) => o.id}
                  emptyMessage="No orders yet"
                />
                {orderMeta && orderMeta.total > ORDER_PAGE_SIZE && (
                  <div className="border-t px-4 py-3">
                    <Pagination
                      page={orderPage}
                      totalPages={totalPages}
                      total={orderMeta.total}
                      perPage={ORDER_PAGE_SIZE}
                      onPageChange={setOrderPage}
                    />
                  </div>
                )}
              </>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Upgrade Dialog */}
      <Dialog open={!!upgradeDialog} onOpenChange={(open) => !open && closeDialog()}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{isRenewal ? "Renew Subscription" : "Upgrade Plan"}</DialogTitle>
            <DialogDescription>
              {dialogStep === "paying"
                ? "Complete your payment to activate the new plan."
                : isRenewal
                  ? `Renew your ${dialogPlan?.display_name || dialogPlan?.name} subscription.`
                  : `Upgrade to ${dialogPlan?.display_name || dialogPlan?.name}.`}
            </DialogDescription>
          </DialogHeader>

          {dialogPlan && dialogStep === "form" && (
            <>
              <div className="space-y-4 py-4">
                <div className="flex items-center justify-between">
                  <span className="text-sm font-medium">Plan</span>
                  <span className="text-sm">
                    {dialogPlan.display_name || dialogPlan.name}
                  </span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-sm font-medium">Price</span>
                  <span className="text-sm">
                    {formatPriceForChannel(dialogPlan, channel)}/
                    {dialogPlan.period_months === 1 ? "mo" : `${dialogPlan.period_months}mo`}
                  </span>
                </div>
                {!isFreePlan && !isRenewal && activeSubPlan && (
                  <div className="flex items-center justify-between">
                    <span className="text-sm font-medium">Current Plan</span>
                    <span className="text-sm">
                      {activeSubPlan.display_name || activeSubPlan.name}{" "}
                      ({formatPriceForCurrency(activeSubPlan, (activeSub?.currency || "CNY") as "CNY" | "USD")}/
                      {activeSubPlan.period_months === 1 ? "mo" : `${activeSubPlan.period_months}mo`})
                    </span>
                  </div>
                )}

                {/* Periods selector — for free->paid upgrades and renewals */}
                {(isFreePlan || isRenewal) && (
                  <div className="space-y-2">
                    <Label>Periods</Label>
                    <Input
                      type="number"
                      min={1}
                      max={24}
                      value={periods}
                      onChange={(e) => setPeriods(Math.max(1, Number(e.target.value) || 1))}
                    />
                  </div>
                )}

                {/* Payment channel selector */}
                <div className="space-y-2">
                  <Label>Payment Method</Label>
                  <div className="grid grid-cols-3 gap-2">
                    {CHANNEL_OPTIONS.map((opt) => {
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

                <div className="flex items-center justify-between border-t pt-3">
                  <span className="font-medium">Estimated Total</span>
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
                </div>
                {!isFreePlan && !isRenewal && activeSubPlan && (
                  <p className="text-xs text-muted-foreground">
                    New plan price minus remaining value of current subscription
                  </p>
                )}
              </div>
              <DialogFooter>
                <Button variant="outline" onClick={closeDialog}>
                  Cancel
                </Button>
                <Button onClick={handlePay} disabled={createOrder.isPending}>
                  {createOrder.isPending ? "Processing..." : "Pay"}
                </Button>
              </DialogFooter>
            </>
          )}

          {dialogStep === "paying" && paymentResult && (
            <>
              <div className="space-y-4 py-4">
                {paymentResult.order.payment_url && paymentResult.channel === "wechat" && (
                  <div className="flex flex-col items-center gap-3">
                    <p className="text-sm text-muted-foreground">
                      Scan the QR code with WeChat to pay
                    </p>
                    <div className="rounded-lg border p-4 bg-white">
                      <QRCodeSVG value={paymentResult.order.payment_url} size={200} />
                    </div>
                    <p className="text-xs text-muted-foreground">
                      Amount: {paymentResult.order.currency === "USD"
                        ? formatPriceUSD(paymentResult.order.amount)
                        : formatPriceCNY(paymentResult.order.amount)}
                    </p>
                  </div>
                )}
                {paymentResult.order.payment_url && paymentResult.channel === "alipay" && (
                  <div className="flex flex-col items-center gap-3">
                    <p className="text-sm text-muted-foreground">
                      Scan the QR code with Alipay to pay
                    </p>
                    <div className="rounded-lg border overflow-hidden bg-white">
                      <iframe
                        src={paymentResult.order.payment_url}
                        className="border-0"
                        style={{ width: 200, height: 200 }}
                        scrolling="no"
                      />
                    </div>
                    <p className="text-xs text-muted-foreground">
                      Amount: {paymentResult.order.currency === "USD"
                        ? formatPriceUSD(paymentResult.order.amount)
                        : formatPriceCNY(paymentResult.order.amount)}
                    </p>
                  </div>
                )}
              </div>
              <DialogFooter>
                <Button variant="outline" onClick={closeDialog}>
                  Close
                </Button>
                <Button
                  onClick={() => {
                    createOrder.reset();
                    closeDialog();
                  }}
                >
                  Payment Complete
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
