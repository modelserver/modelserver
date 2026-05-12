import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useProject } from "@/api/projects";
import { useUsageOverview, useDailyUsage } from "@/api/usage";
import { useRequests } from "@/api/requests";
import { useMyQuota } from "@/api/members";
import { useSubscriptionUsage } from "@/api/subscriptions";
import { PageHeader } from "@/components/layout/PageHeader";
import { StatCard } from "@/components/shared/StatCard";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Tooltip as InfoTooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import type { Request } from "@/api/types";
import { useAuth } from "@/hooks/useAuth";
import { Activity, Zap, Clock, Coins, Receipt, Wallet, PiggyBank } from "lucide-react";
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
  Legend,
} from "recharts";

function formatNumber(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

function formatYuan(fen: number): string {
  const yuan = fen / 100;
  return `¥${yuan.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
}

function formatPeriod(startISO: string, endISO: string): string {
  const fmt = (s: string) =>
    new Date(s).toLocaleDateString("en-US", { year: "numeric", month: "short", day: "numeric" });
  return `${fmt(startISO)} – ${fmt(endISO)}`;
}

// formatResetIn returns a short "in 2d 3h" / "in 14m" string for a future
// timestamp. Returns empty for past timestamps so we don't show "in 0m".
function formatResetIn(iso: string): string {
  const diffMs = new Date(iso).getTime() - Date.now();
  if (diffMs <= 0) return "";
  const totalMin = Math.floor(diffMs / 60_000);
  const days = Math.floor(totalMin / (60 * 24));
  const hours = Math.floor((totalMin % (60 * 24)) / 60);
  const minutes = totalMin % 60;
  if (days > 0) return hours > 0 ? `in ${days}d ${hours}h` : `in ${days}d`;
  if (hours > 0) return minutes > 0 ? `in ${hours}h ${minutes}m` : `in ${hours}h`;
  return `in ${Math.max(1, minutes)}m`;
}

const recentColumns: Column<Request>[] = [
  { header: "Model", accessor: "model" },
  {
    header: "Status",
    accessor: (r) => <StatusBadge status={r.status} />,
  },
  {
    header: "Tokens",
    accessor: (r) => formatNumber(r.input_tokens + r.output_tokens),
    className: "text-right",
  },
  {
    header: "Latency",
    accessor: (r) => `${r.latency_ms}ms`,
    className: "text-right",
  },
  {
    header: "Time",
    accessor: (r) => new Date(r.created_at).toLocaleString(),
  },
];

export function OverviewPage() {
  const projectId = useCurrentProject();
  const { data: project } = useProject(projectId);
  const { data: usage } = useUsageOverview(projectId);
  const { data: daily } = useDailyUsage(projectId);
  const { data: recentData } = useRequests(projectId, { per_page: 5 });
  const { data: myQuotaData } = useMyQuota(projectId);
  const { data: subUsageData } = useSubscriptionUsage(projectId);
  const { user } = useAuth();

  const overview = usage?.data;
  const dailyData = daily?.data ?? [];
  const recentRequests = recentData?.data ?? [];
  const myQuota = myQuotaData?.data;
  const planWindows = subUsageData?.data ?? [];

  // The handler may align the window to the active subscription period
  // instead of the historical 30-day default. Drive the existing cards'
  // description from period_source so the label reflects the actual data.
  const periodLabel =
    overview?.period_source === "subscription" && overview.cost_breakdown
      ? `Current period · ${formatPeriod(
          overview.cost_breakdown.period_start,
          overview.cost_breakdown.period_end,
        )}`
      : "Last 30 days";

  return (
    <div className="space-y-6">
      <PageHeader
        title={project?.data.name ?? "Project"}
        description={project?.data.description}
      />

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          title="Total Requests"
          value={formatNumber(overview?.request_count ?? 0)}
          description={periodLabel}
          icon={<Activity className="h-4 w-4" />}
        />
        <StatCard
          title="Total Tokens"
          value={formatNumber(overview?.total_tokens ?? 0)}
          description={periodLabel}
          icon={<Zap className="h-4 w-4" />}
        />
        <StatCard
          title="Total Credits"
          value={`${(overview?.total_credits_k ?? 0).toLocaleString()}K`}
          description={periodLabel}
          icon={<Coins className="h-4 w-4" />}
        />
        <StatCard
          title="Avg Daily"
          value={formatNumber(
            dailyData.length > 0
              ? Math.round(
                  dailyData.reduce((s, d) => s + d.request_count, 0) /
                    dailyData.length,
                )
              : 0,
          )}
          description="Requests/day"
          icon={<Clock className="h-4 w-4" />}
        />
      </div>

      {overview?.cost_breakdown && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <InfoTooltip>
            <TooltipTrigger
              render={
                <button
                  type="button"
                  className="block w-full cursor-help rounded-lg text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                />
              }
            >
              <StatCard
                title="API Standard Price"
                value={formatYuan(overview.cost_breakdown.api_standard_fen)}
                description={`At official API pricing · ${formatPeriod(
                  overview.cost_breakdown.period_start,
                  overview.cost_breakdown.period_end,
                )}`}
                icon={<Receipt className="h-4 w-4" />}
              />
            </TooltipTrigger>
            <TooltipContent>
              <div className="space-y-0.5 text-xs">
                <div>Sum over the period of (tokens × catalog default rate).</div>
                <div>Period: {formatPeriod(
                  overview.cost_breakdown.period_start,
                  overview.cost_breakdown.period_end,
                )}</div>
              </div>
            </TooltipContent>
          </InfoTooltip>

          <InfoTooltip>
            <TooltipTrigger
              render={
                <button
                  type="button"
                  className="block w-full cursor-help rounded-lg text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                />
              }
            >
              <StatCard
                title="Period Paid"
                value={formatYuan(overview.cost_breakdown.actual_paid_fen)}
                description={
                  overview.cost_breakdown.has_active_subscription
                    ? `Subscription ${formatYuan(overview.cost_breakdown.subscription_fen)} + Extra ${formatYuan(overview.cost_breakdown.extra_usage_fen)}`
                    : `Extra usage ${formatYuan(overview.cost_breakdown.extra_usage_fen)}`
                }
                icon={<Wallet className="h-4 w-4" />}
              />
            </TooltipTrigger>
            <TooltipContent>
              <div className="space-y-0.5 text-xs">
                <div>actual_paid = subscription_price + extra_usage_spend</div>
                <div>
                  = {formatYuan(overview.cost_breakdown.subscription_fen)} +{" "}
                  {formatYuan(overview.cost_breakdown.extra_usage_fen)}
                </div>
              </div>
            </TooltipContent>
          </InfoTooltip>

          <InfoTooltip>
            <TooltipTrigger
              render={
                <button
                  type="button"
                  className="block w-full cursor-help rounded-lg text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                />
              }
            >
              {overview.cost_breakdown.saved_fen > 0 ? (
                <StatCard
                  title="Saved by Plan"
                  value={formatYuan(overview.cost_breakdown.saved_fen)}
                  description={
                    overview.cost_breakdown.api_standard_fen > 0
                      ? `↓ ${Math.round(
                          (overview.cost_breakdown.saved_fen / overview.cost_breakdown.api_standard_fen) * 100,
                        )}% off`
                      : ""
                  }
                  icon={<PiggyBank className="h-4 w-4" />}
                />
              ) : (
                <StatCard
                  title="Saved by Plan"
                  value="—"
                  description="Low usage this period — plan hasn't paid off yet"
                  icon={<PiggyBank className="h-4 w-4" />}
                />
              )}
            </TooltipTrigger>
            <TooltipContent>
              <div className="space-y-0.5 text-xs">
                <div>saved = max(0, api_standard − actual_paid)</div>
                <div>
                  = max(0, {formatYuan(overview.cost_breakdown.api_standard_fen)} −{" "}
                  {formatYuan(overview.cost_breakdown.actual_paid_fen)})
                </div>
              </div>
            </TooltipContent>
          </InfoTooltip>
        </div>
      )}

      {myQuota && myQuota.credit_quota_percent !== null && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">My Quota</CardTitle>
            <p className="text-sm text-muted-foreground">
              {myQuota.credit_quota_percent}% of project quota
            </p>
          </CardHeader>
          <CardContent className="space-y-4">
            {myQuota.windows.map((w) => {
              const resetIn = w.resets_at ? formatResetIn(w.resets_at) : "";
              return (
                <div key={w.window} className="space-y-1">
                  <div className="flex justify-between text-sm">
                    <span className="capitalize">{w.window}</span>
                    <span className="flex items-center gap-2 text-muted-foreground">
                      <span>
                        {user?.is_superadmin
                          ? `${Math.round(w.used ?? 0).toLocaleString()} / ${(w.limit ?? 0).toLocaleString()}`
                          : `${w.percentage.toFixed(2)}%`}
                      </span>
                      {resetIn && (
                        <span
                          className="text-xs"
                          title={w.resets_at ? new Date(w.resets_at).toLocaleString() : undefined}
                        >
                          · resets {resetIn}
                        </span>
                      )}
                    </span>
                  </div>
                  <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
                    <div
                      className="h-full bg-primary transition-all"
                      style={{
                        width: `${Math.min(w.percentage, 100)}%`,
                      }}
                    />
                  </div>
                </div>
              );
            })}
          </CardContent>
        </Card>
      )}

      {planWindows.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Plan Usage</CardTitle>
            <p className="text-sm text-muted-foreground">
              Project-wide credit windows from the active subscription.
            </p>
          </CardHeader>
          <CardContent className="space-y-4">
            {planWindows.map((w) => {
              const resetIn = w.resets_at ? formatResetIn(w.resets_at) : "";
              return (
                <div key={w.window} className="space-y-1">
                  <div className="flex justify-between text-sm">
                    <span className="capitalize">{w.window}</span>
                    <span className="flex items-center gap-2 text-muted-foreground">
                      <span>{w.percentage.toFixed(2)}%</span>
                      {resetIn && (
                        <span
                          className="text-xs"
                          title={w.resets_at ? new Date(w.resets_at).toLocaleString() : undefined}
                        >
                          · resets {resetIn}
                        </span>
                      )}
                    </span>
                  </div>
                  <div className="h-2 w-full overflow-hidden rounded-full bg-muted">
                    <div
                      className="h-full bg-primary transition-all"
                      style={{ width: `${Math.min(w.percentage, 100)}%` }}
                    />
                  </div>
                </div>
              );
            })}
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Daily Requests & Credits</CardTitle>
        </CardHeader>
        <CardContent>
          {dailyData.length > 0 ? (
            <ResponsiveContainer width="100%" height={250}>
              <LineChart data={dailyData}>
                <CartesianGrid strokeDasharray="3 3" opacity={0.1} />
                <XAxis
                  dataKey="date"
                  tickFormatter={(d: string) => d.slice(5)}
                  fontSize={12}
                  stroke="currentColor"
                  opacity={0.5}
                />
                <YAxis
                  yAxisId="left"
                  fontSize={12}
                  stroke="currentColor"
                  opacity={0.5}
                />
                <YAxis
                  yAxisId="right"
                  orientation="right"
                  fontSize={12}
                  stroke="currentColor"
                  opacity={0.5}
                />
                <Tooltip
                  contentStyle={{
                    backgroundColor: "hsl(var(--card))",
                    border: "1px solid hsl(var(--border))",
                    borderRadius: "var(--radius)",
                  }}
                />
                <Legend />
                <Line
                  yAxisId="left"
                  type="monotone"
                  dataKey="request_count"
                  stroke="oklch(0.488 0.243 264.376)"
                  strokeWidth={2}
                  dot={false}
                  name="Requests"
                />
                <Line
                  yAxisId="right"
                  type="monotone"
                  dataKey="total_credits_k"
                  stroke="oklch(0.696 0.17 162.48)"
                  strokeWidth={2}
                  dot={false}
                  name="Credits (K)"
                />
              </LineChart>
            </ResponsiveContainer>
          ) : (
            <p className="py-8 text-center text-muted-foreground">
              No data yet
            </p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Recent Requests</CardTitle>
        </CardHeader>
        <CardContent>
          <DataTable
            columns={recentColumns}
            data={recentRequests}
            keyFn={(r) => r.id}
            emptyMessage="No requests yet"
          />
        </CardContent>
      </Card>
    </div>
  );
}
