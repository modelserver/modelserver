import { useState, useEffect, useMemo } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { QRCodeSVG } from "qrcode.react";
import { ExternalLink, Loader2, Zap } from "lucide-react";

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

type PaymentChannel = "wechat" | "alipay";

const TX_PAGE_SIZE = 20;

function formatFen(fen: number): string {
  return `\u00A5${(fen / 100).toFixed(2)}`;
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

  // Local form state for the monthly limit — persisted on blur.
  const [monthlyLimitInput, setMonthlyLimitInput] = useState<string>("");
  useEffect(() => {
    if (settings) {
      setMonthlyLimitInput(((settings.monthly_limit_fen || 0) / 100).toFixed(2));
    }
  }, [settings?.monthly_limit_fen]);

  // Transactions pagination.
  const [txPage, setTxPage] = useState(1);
  const { data: txData, isLoading: txLoading } = useExtraUsageTransactions(
    projectId,
    txPage,
    TX_PAGE_SIZE,
  );

  // Top-up dialog.
  const [topupOpen, setTopupOpen] = useState(false);
  const [topupAmountYuan, setTopupAmountYuan] = useState("50");
  const [topupChannel, setTopupChannel] = useState<PaymentChannel>("wechat");
  const [paymentOrder, setPaymentOrder] = useState<Order | null>(null);
  const { data: statusData } = useExtraUsageTopupStatus(
    projectId,
    paymentOrder?.id ?? null,
  );

  useEffect(() => {
    if (!statusData?.data) return;
    const s = statusData.data.status;
    if (s === "delivered") {
      toast.success("Top-up successful");
      setPaymentOrder(null);
      setTopupOpen(false);
      qc.invalidateQueries({ queryKey: ["extra-usage", projectId] });
      qc.invalidateQueries({ queryKey: ["extra-usage-transactions", projectId] });
    } else if (s === "failed" || s === "cancelled") {
      toast.error(`Payment ${s}`);
      setPaymentOrder(null);
    }
  }, [statusData]);

  const monthlyPct = useMemo(() => {
    if (!settings || !settings.monthly_limit_fen) return 0;
    const pct = (settings.monthly_spent_fen / settings.monthly_limit_fen) * 100;
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
    const yuan = parseFloat(monthlyLimitInput);
    if (!isFinite(yuan) || yuan < 0) {
      toast.error("Invalid monthly limit");
      return;
    }
    try {
      await updateSettings.mutateAsync({ monthly_limit_fen: Math.round(yuan * 100) });
      toast.success("Monthly limit updated");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Update failed");
    }
  }

  async function handleCreateTopup() {
    const yuan = parseFloat(topupAmountYuan);
    const fen = Math.round(yuan * 100);
    if (!isFinite(yuan) || fen <= 0) {
      toast.error("Invalid amount");
      return;
    }
    if (settings && fen < settings.min_topup_fen) {
      toast.error(`Minimum top-up is ${formatFen(settings.min_topup_fen)}`);
      return;
    }
    if (settings && fen > settings.max_topup_fen) {
      toast.error(`Maximum top-up is ${formatFen(settings.max_topup_fen)}`);
      return;
    }
    try {
      const resp = await topup.mutateAsync({ amount_fen: fen, channel: topupChannel });
      if (resp.data.payment_url) {
        setPaymentOrder(resp.data);
      } else {
        toast.success("Order created");
        setTopupOpen(false);
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Top-up failed");
    }
  }

  const txs = txData?.data ?? [];
  const txMeta = txData?.meta;

  const txColumns: Column<ExtraUsageTransaction>[] = [
    { header: "Time", accessor: (t) => formatDateTime(t.created_at) },
    { header: "Type", accessor: (t) => txTypeBadge(t.type) },
    {
      header: "Amount",
      accessor: (t) => (
        <span className={t.amount_fen < 0 ? "text-destructive" : "text-emerald-600"}>
          {t.amount_fen < 0 ? "-" : "+"}
          {formatFen(Math.abs(t.amount_fen))}
        </span>
      ),
    },
    { header: "Balance after", accessor: (t) => formatFen(t.balance_after_fen) },
    { header: "Reason", accessor: (t) => t.reason || "—" },
    { header: "Description", accessor: (t) => t.description || "—" },
  ];

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
        <Card>
          <CardHeader>
            <CardTitle className="text-sm font-medium">Balance</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-3xl font-semibold">
              {formatFen(settings?.balance_fen ?? 0)}
            </div>
            <div className="mt-1 text-xs text-muted-foreground">
              {settings?.enabled ? "Enabled" : "Disabled"} · credit price{" "}
              {formatFen(settings?.credit_price_fen ?? 0)} per 1M credits
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="text-sm font-medium">This month</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="text-3xl font-semibold">
              {formatFen(settings?.monthly_spent_fen ?? 0)}
            </div>
            <div className="mt-2 h-2 w-full rounded bg-muted">
              <div
                className="h-2 rounded bg-primary"
                style={{ width: `${monthlyPct}%` }}
              />
            </div>
            <div className="mt-1 text-xs text-muted-foreground">
              {settings?.monthly_limit_fen
                ? `of ${formatFen(settings.monthly_limit_fen)} limit`
                : "no monthly limit set"}
            </div>
          </CardContent>
        </Card>
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
              <Label htmlFor="monthly-limit">Monthly limit (¥)</Label>
              <Input
                id="monthly-limit"
                type="number"
                min="0"
                step="0.01"
                value={monthlyLimitInput}
                onChange={(e) => setMonthlyLimitInput(e.target.value)}
              />
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

      <Dialog
        open={topupOpen}
        onOpenChange={(open) => {
          if (!open) {
            setTopupOpen(false);
            setPaymentOrder(null);
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Top up extra usage balance</DialogTitle>
            <DialogDescription>
              Payment is billed at official API prices. Amounts between{" "}
              {settings ? formatFen(settings.min_topup_fen) : "—"} and{" "}
              {settings ? formatFen(settings.max_topup_fen) : "—"} per top-up.
            </DialogDescription>
          </DialogHeader>
          {!paymentOrder && (
            <div className="space-y-4">
              <div>
                <Label htmlFor="topup-amount">Amount (¥)</Label>
                <Input
                  id="topup-amount"
                  type="number"
                  min="0"
                  step="1"
                  value={topupAmountYuan}
                  onChange={(e) => setTopupAmountYuan(e.target.value)}
                />
              </div>
              <div>
                <Label>Payment channel</Label>
                <div className="mt-1 flex gap-2">
                  <Button
                    variant={topupChannel === "wechat" ? "default" : "outline"}
                    onClick={() => setTopupChannel("wechat")}
                  >
                    WeChat Pay
                  </Button>
                  <Button
                    variant={topupChannel === "alipay" ? "default" : "outline"}
                    onClick={() => setTopupChannel("alipay")}
                  >
                    Alipay
                  </Button>
                </div>
              </div>
            </div>
          )}
          {paymentOrder?.payment_url && (
            <div className="flex flex-col items-center space-y-3">
              <QRCodeSVG value={paymentOrder.payment_url} size={180} />
              <p className="text-center text-sm text-muted-foreground">
                Scan to pay with {topupChannel === "wechat" ? "WeChat" : "Alipay"}.
              </p>
              <a
                className="inline-flex items-center text-sm text-primary"
                href={paymentOrder.payment_url}
                target="_blank"
                rel="noreferrer"
              >
                Open payment page
                <ExternalLink className="ml-1 h-3 w-3" />
              </a>
            </div>
          )}
          <DialogFooter>
            {!paymentOrder ? (
              <Button onClick={handleCreateTopup} disabled={topup.isPending}>
                {topup.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                Continue to payment
              </Button>
            ) : (
              <Button variant="outline" onClick={() => setPaymentOrder(null)}>
                Back
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
