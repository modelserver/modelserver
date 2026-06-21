import { useState, useEffect, useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { QRCodeSVG } from "qrcode.react";
import { Loader2, Zap } from "lucide-react";

import { useCurrentProject } from "@/hooks/useCurrentProject";
import {
  useExtraUsage,
  useUpdateExtraUsage,
  useExtraUsageTransactions,
  useCreateExtraUsageTopup,
  useExtraUsageTopupStatus,
  type ExtraUsageTransaction,
} from "@/api/extra-usage";

import { PageHeader } from "@/components/layout/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Pagination } from "@/components/shared/Pagination";
import type { Order } from "@/api/types";

type PaymentChannel = "wechat" | "alipay" | "stripe";

interface PaymentResult {
  order: Order;
  channel: PaymentChannel;
}

const TX_PAGE_SIZE = 20;

// Module-scope so the array reference is stable across renders — matches the
// pattern in SubscriptionPage.tsx so the two payment dialogs stay in sync.
const CHANNEL_OPTIONS = [
  { value: "wechat" as const, label: "WeChat Pay", currency: "CNY" as const },
  { value: "alipay" as const, label: "Alipay",     currency: "CNY" as const },
  { value: "stripe" as const, label: "Stripe",     currency: "USD" as const },
];

/** Format a credits integer with thousands separators. */
function formatCredits(credits: number): string {
  return credits.toLocaleString("en-US");
}

/** Parse a localized payment-channel label from a description string like "channel=wechat currency=CNY". */
function channelFromDescription(desc: string | null | undefined): string | null {
  if (!desc) return null;
  const match = desc.match(/channel=(\w+)/);
  if (!match) return null;
  const key = match[1];
  if (key === "wechat") return "微信支付";
  if (key === "alipay") return "支付宝";
  if (key === "stripe") return "Stripe";
  return key ?? null;
}

/** Format a CNY fen amount as ¥X.XX yuan. */
function formatYuan(fen: number): string {
  return `¥${(fen / 100).toFixed(2)}`;
}

/** Format a USD cents amount as $X.XX. */
function formatUSD(cents: number): string {
  return `$${(cents / 100).toFixed(2)}`;
}

function formatDateTime(d: string) {
  return new Date(d).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function txTypeBadge(t: ExtraUsageTransaction["type"]) {
  switch (t) {
    case "topup":
      return <Badge variant="default">Top-up</Badge>;
    case "deduction":
      return <Badge variant="secondary">Deduction</Badge>;
    case "refund":
      return <Badge variant="outline">Refund</Badge>;
    case "adjust":
      return <Badge variant="outline">Adjust</Badge>;
    default:
      return <Badge variant="outline">{t}</Badge>;
  }
}

export function ExtraUsagePage() {
  const projectId = useCurrentProject();
  const qc = useQueryClient();

  const { data: euData, isLoading } = useExtraUsage(projectId);
  const settings = euData?.data;
  const updateSettings = useUpdateExtraUsage(projectId);
  const topup = useCreateExtraUsageTopup(projectId);

  // Local form state for the monthly limit (credits) — persisted on save.
  const [monthlyLimitInput, setMonthlyLimitInput] = useState<string>("");
  useEffect(() => {
    if (settings) {
      setMonthlyLimitInput(String(settings.monthly_limit_credits ?? 0));
    }
  }, [settings?.monthly_limit_credits]);

  // Derived ¥-equivalent preview for the monthly limit input.
  const monthlyLimitYuanPreview = useMemo(() => {
    const credits = parseInt(monthlyLimitInput, 10);
    if (!isFinite(credits) || credits <= 0 || !settings) return null;
    const fen = (credits * settings.credit_unit_prices.cny_fen_per_million) / 1_000_000;
    return formatYuan(Math.round(fen));
  }, [monthlyLimitInput, settings]);

  // Transactions pagination.
  const [txPage, setTxPage] = useState(1);
  const { data: txData, isLoading: txLoading } = useExtraUsageTransactions(
    projectId,
    txPage,
    TX_PAGE_SIZE,
  );

  // Top-up dialog state. dialogStep mirrors SubscriptionPage's form|paying
  // state machine so the dialog explicitly transitions between collecting
  // input and showing the payment artifact; without it the form fields and
  // QR code render simultaneously which is the inconsistency this page is
  // being aligned to fix.
  const [topupOpen, setTopupOpen] = useState(false);
  const [topupAmount, setTopupAmount] = useState("50");
  const [topupChannel, setTopupChannel] = useState<PaymentChannel>("wechat");
  const [paymentResult, setPaymentResult] = useState<PaymentResult | null>(null);
  const [dialogStep, setDialogStep] = useState<"form" | "paying">("form");

  // Poll status only while we're in the paying step — matches the subscription
  // flow's discipline of not polling stale order ids after the dialog closes.
  const pollingOrderId =
    dialogStep === "paying" ? paymentResult?.order.id ?? null : null;
  const { data: statusData } = useExtraUsageTopupStatus(projectId, pollingOrderId);

  function closeDialog() {
    setTopupOpen(false);
    setPaymentResult(null);
    setDialogStep("form");
    setTopupAmount("50");
    setTopupChannel("wechat");
  }

  useEffect(() => {
    if (!statusData?.data) return;
    const s = statusData.data.status;
    if (s === "delivered") {
      toast.success("Top-up successful");
      closeDialog();
      qc.invalidateQueries({ queryKey: ["extra-usage", projectId] });
      qc.invalidateQueries({ queryKey: ["extra-usage-transactions", projectId] });
    } else if (s === "failed" || s === "cancelled") {
      toast.error(`Payment ${s}`);
      closeDialog();
    }
  }, [statusData]);

  // Live credits preview for topup dialog.
  const topupCreditsPreview = useMemo(() => {
    if (!settings) return null;
    const amount = parseFloat(topupAmount);
    if (!isFinite(amount) || amount <= 0) return null;
    if (topupChannel === "stripe") {
      const cents = Math.round(amount * 100);
      return Math.floor((cents * 1_000_000) / settings.credit_unit_prices.usd_cents_per_million);
    } else {
      const fen = Math.round(amount * 100);
      return Math.floor((fen * 1_000_000) / settings.credit_unit_prices.cny_fen_per_million);
    }
  }, [topupAmount, topupChannel, settings]);

  const monthlyPct = useMemo(() => {
    if (!settings || !settings.monthly_limit_credits) return 0;
    const pct = (settings.monthly_spent_credits / settings.monthly_limit_credits) * 100;
    return Math.min(100, Math.max(0, pct));
  }, [settings]);

  async function handleToggleEnabled() {
    if (!settings) return;
    try {
      await updateSettings.mutateAsync({ enabled: !settings.enabled });
      toast.success(settings.enabled ? "Extra usage disabled" : "Extra usage enabled");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Update failed");
    }
  }

  async function handleSaveMonthlyLimit() {
    const credits = parseInt(monthlyLimitInput, 10);
    if (!isFinite(credits) || credits < 0) {
      toast.error("Invalid monthly limit");
      return;
    }
    try {
      await updateSettings.mutateAsync({ monthly_limit_credits: credits });
      toast.success("Monthly limit updated");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Update failed");
    }
  }

  async function handleCreateTopup() {
    if (!settings) return;
    const amount = parseFloat(topupAmount);
    if (!isFinite(amount) || amount <= 0) {
      toast.error("Invalid amount");
      return;
    }

    try {
      let resp;
      if (topupChannel === "stripe") {
        const cents = Math.round(amount * 100);
        if (cents < settings.min_topup.usd_cents) {
          toast.error(`Minimum top-up is ${formatUSD(settings.min_topup.usd_cents)}`);
          return;
        }
        if (cents > settings.max_topup.usd_cents) {
          toast.error(`Maximum top-up is ${formatUSD(settings.max_topup.usd_cents)}`);
          return;
        }
        resp = await topup.mutateAsync({ channel: "stripe", amount_cents: cents });
      } else {
        const fen = Math.round(amount * 100);
        if (fen < settings.min_topup.cny_fen) {
          toast.error(`Minimum top-up is ${formatYuan(settings.min_topup.cny_fen)}`);
          return;
        }
        if (fen > settings.max_topup.cny_fen) {
          toast.error(`Maximum top-up is ${formatYuan(settings.max_topup.cny_fen)}`);
          return;
        }
        resp = await topup.mutateAsync({ channel: topupChannel, amount_fen: fen });
      }

      const order = resp.data;
      if (topupChannel === "stripe") {
        // Stripe payment_url is a hosted Checkout page — must be a full-page
        // redirect, NOT a QR code (its long https URL is not a scannable
        // payment intent). Matches SubscriptionPage's Stripe branch.
        if (!order.payment_url) {
          toast.error("Stripe checkout URL missing");
          return;
        }
        window.location.href = order.payment_url;
        return; // unreachable on success
      }

      if (order.payment_url) {
        setPaymentResult({ order, channel: topupChannel });
        setDialogStep("paying");
      } else {
        toast.success("Order created");
        closeDialog();
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Top-up failed");
    }
  }

  // When channel switches, reset the amount input so the number stays reasonable.
  function handleChannelChange(ch: PaymentChannel) {
    setTopupChannel(ch);
    setTopupAmount(ch === "stripe" ? "10" : "50");
  }

  const txs = txData?.data ?? [];
  const txMeta = txData?.meta;

  const txColumns: Column<ExtraUsageTransaction>[] = [
    { header: "Time", accessor: (t) => formatDateTime(t.created_at) },
    { header: "Type", accessor: (t) => txTypeBadge(t.type) },
    {
      header: "Amount",
      accessor: (t) => {
        const creditsSpan = (
          <span className={t.amount_credits < 0 ? "text-destructive" : "text-emerald-600"}>
            {t.amount_credits < 0 ? "-" : "+"}
            {formatCredits(Math.abs(t.amount_credits))} credits
          </span>
        );
        if (t.type === "topup") {
          const channel = channelFromDescription(t.description);
          return (
            <div className="flex flex-col">
              {creditsSpan}
              {channel && (
                <span className="text-xs text-muted-foreground">{channel}</span>
              )}
            </div>
          );
        }
        return creditsSpan;
      },
    },
    {
      header: "Balance after",
      accessor: (t) => `${formatCredits(t.balance_after_credits)} credits`,
    },
    { header: "Reason", accessor: (t) => t.reason || "—" },
    {
      header: "Description",
      accessor: (t) => {
        // Topup rows use the Amount column for channel info; suppress here to avoid duplication.
        if (t.type === "topup") return null;
        if (!t.description) return "—";
        return <span className="text-xs text-muted-foreground">{t.description}</span>;
      },
    },
  ];

  // Min/max helper text for topup dialog.
  const topupMinMax = useMemo(() => {
    if (!settings) return null;
    if (topupChannel === "stripe") {
      return `Min ${formatUSD(settings.min_topup.usd_cents)} · Max ${formatUSD(settings.max_topup.usd_cents)}`;
    }
    return `Min ${formatYuan(settings.min_topup.cny_fen)} · Max ${formatYuan(settings.max_topup.cny_fen)}`;
  }, [settings, topupChannel]);

  if (isLoading) {
    return (
      <div className="p-8">
        <Loader2 className="h-5 w-5 animate-spin" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Extra Usage"
        description="Pay-per-use top-ups that activate when your subscription's credit window runs out or when a non-Claude-Code client requests an Anthropic model."
      />

      <div className="grid gap-4 md:grid-cols-3">
        {/* Balance card */}
        <Card>
          <CardHeader>
            <CardTitle className="text-sm font-medium">Extra-Usage Wallet</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-semibold">
              {formatCredits(settings?.balance_credits ?? 0)} credits
            </div>
            {settings && (
              <div className="mt-1 text-xs text-muted-foreground">
                ≈ {formatYuan(
                  Math.round(
                    (settings.balance_credits * settings.credit_unit_prices.cny_fen_per_million) /
                      1_000_000,
                  ),
                )}
              </div>
            )}
            <div className="mt-1 text-xs text-muted-foreground">
              {settings?.enabled ? "Enabled" : "Disabled"}
            </div>
          </CardContent>
        </Card>

        {/* Monthly spend card */}
        <Card>
          <CardHeader>
            <CardTitle className="text-sm font-medium">This month</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-2xl font-semibold">
              {formatCredits(settings?.monthly_spent_credits ?? 0)} credits
            </div>
            <div className="mt-2 h-2 w-full rounded bg-muted">
              <div
                className="h-2 rounded bg-primary"
                style={{ width: `${monthlyPct}%` }}
              />
            </div>
            <div className="mt-1 text-xs text-muted-foreground">
              {settings?.monthly_limit_credits
                ? `of ${formatCredits(settings.monthly_limit_credits)} limit`
                : "no monthly limit set"}
            </div>
          </CardContent>
        </Card>

        {/* Actions card */}
        <Card>
          <CardHeader>
            <CardTitle className="text-sm font-medium">Actions</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            <Button className="w-full" onClick={() => setTopupOpen(true)}>
              <Zap className="mr-2 h-4 w-4" />
              Top up
            </Button>
            <Button
              variant="outline"
              className="w-full"
              onClick={handleToggleEnabled}
              disabled={updateSettings.isPending}
            >
              {settings?.enabled ? "Disable" : "Enable"} extra usage
            </Button>
          </CardContent>
        </Card>
      </div>

      {/* Monthly spend cap */}
      <Card>
        <CardHeader>
          <CardTitle>Monthly spend cap</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Cap how much extra usage this project can consume each calendar
            month (Asia/Shanghai). Set to 0 to remove the cap.
          </p>
          <div className="flex gap-2">
            <div className="flex-1">
              <Label htmlFor="monthly-limit">Monthly limit (credits)</Label>
              <Input
                id="monthly-limit"
                type="number"
                min="0"
                step="1"
                value={monthlyLimitInput}
                onChange={(e) => setMonthlyLimitInput(e.target.value)}
              />
              {monthlyLimitYuanPreview && (
                <p className="mt-1 text-xs text-muted-foreground">
                  ≈ {monthlyLimitYuanPreview}
                </p>
              )}
            </div>
            <Button
              className="mt-6"
              onClick={handleSaveMonthlyLimit}
              disabled={updateSettings.isPending}
            >
              Save
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Transactions */}
      <Card>
        <CardHeader>
          <CardTitle>Transactions</CardTitle>
        </CardHeader>
        <CardContent>
          {txLoading ? (
            <div className="flex justify-center p-6">
              <Loader2 className="h-5 w-5 animate-spin" />
            </div>
          ) : (
            <DataTable
              data={txs}
              columns={txColumns}
              keyFn={(t) => t.id}
              emptyMessage="No transactions yet."
            />
          )}
          {txMeta && txMeta.total_pages > 1 && (
            <Pagination
              page={txPage}
              totalPages={txMeta.total_pages}
              total={txMeta.total}
              perPage={txMeta.per_page}
              onPageChange={setTxPage}
            />
          )}
        </CardContent>
      </Card>

      {/* Topup dialog */}
      <Dialog open={topupOpen} onOpenChange={(open) => !open && closeDialog()}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Top up extra usage balance</DialogTitle>
            <DialogDescription>
              {dialogStep === "paying"
                ? "Complete your payment to credit the wallet."
                : `Payment is billed at official API prices.${topupMinMax ? ` ${topupMinMax} per top-up.` : ""}`}
            </DialogDescription>
          </DialogHeader>

          {dialogStep === "form" && (
            <>
              <div className="space-y-4 py-4">
                {/* Payment channel selector — matches SubscriptionPage layout
                    (3-col grid, w-full buttons, "Payment Method" label) so
                    the two payment dialogs look identical. No currency lock
                    here — credits is a single-wallet design, any channel
                    tops up the same balance. */}
                <div className="space-y-2">
                  <Label>Payment Method</Label>
                  <div className="grid grid-cols-3 gap-2">
                    {CHANNEL_OPTIONS.map((opt) => (
                      <Button
                        key={opt.value}
                        type="button"
                        variant={topupChannel === opt.value ? "default" : "outline"}
                        className="w-full"
                        onClick={() => handleChannelChange(opt.value)}
                      >
                        {opt.label}
                      </Button>
                    ))}
                  </div>
                </div>

                {/* Amount input — label and currency symbol swap based on channel */}
                <div className="space-y-2">
                  <Label htmlFor="topup-amount">
                    Amount ({topupChannel === "stripe" ? "$" : "¥"})
                  </Label>
                  <div className="relative">
                    <span className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-sm text-muted-foreground">
                      {topupChannel === "stripe" ? "$" : "¥"}
                    </span>
                    <Input
                      id="topup-amount"
                      type="number"
                      min="0"
                      step={topupChannel === "stripe" ? "0.01" : "1"}
                      className="pl-7"
                      value={topupAmount}
                      onChange={(e) => setTopupAmount(e.target.value)}
                    />
                  </div>
                  {topupCreditsPreview !== null && (
                    <p className="text-xs text-muted-foreground">
                      ≈ {formatCredits(topupCreditsPreview)} credits
                    </p>
                  )}
                  {topupMinMax && (
                    <p className="text-xs text-muted-foreground">{topupMinMax}</p>
                  )}
                </div>
              </div>
              <DialogFooter>
                <Button variant="outline" onClick={closeDialog}>
                  Cancel
                </Button>
                <Button onClick={handleCreateTopup} disabled={topup.isPending}>
                  {topup.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                  Pay
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
                        ? formatUSD(paymentResult.order.amount)
                        : formatYuan(paymentResult.order.amount)}
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
                        ? formatUSD(paymentResult.order.amount)
                        : formatYuan(paymentResult.order.amount)}
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
                    topup.reset();
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
