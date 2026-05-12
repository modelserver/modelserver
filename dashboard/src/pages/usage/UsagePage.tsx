import { useState } from "react";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useUsageOverview, useUsageByModel, useDailyUsage, useUsageByMember } from "@/api/usage";
import { useExtraUsage } from "@/api/extra-usage";
import { Link } from "react-router";
import { PageHeader } from "@/components/layout/PageHeader";
import { StatCard } from "@/components/shared/StatCard";
import { DateRangePicker } from "@/components/shared/DateRangePicker";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Pagination } from "@/components/shared/Pagination";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Avatar, AvatarImage, AvatarFallback } from "@/components/ui/avatar";
import type { UsageSummary, UsageByMember } from "@/api/types";
import {
  LineChart,
  Line,
  PieChart,
  Pie,
  Cell,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
  Legend,
} from "recharts";

// Palette for the by-model pie. Cycled if the project uses more models than
// the palette has slots — the rarer models get repeated colors which is fine
// since they are sorted to the tail and rendered as smaller slices.
const MODEL_PIE_COLORS = [
  "oklch(0.488 0.243 264.376)", // blue
  "oklch(0.696 0.17 162.48)",   // green
  "oklch(0.769 0.188 70.08)",   // orange
  "oklch(0.627 0.265 303.9)",   // purple
  "oklch(0.645 0.246 16.439)",  // red
  "oklch(0.6 0.118 184.704)",   // teal
  "oklch(0.828 0.189 84.429)",  // yellow
  "oklch(0.55 0.027 264.364)",  // gray
];

function formatNumber(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

function defaultSince() {
  const d = new Date();
  d.setDate(d.getDate() - 30);
  return d.toISOString().split("T")[0]!;
}

function defaultUntil() {
  return new Date().toISOString().split("T")[0]!;
}

const tooltipStyle = {
  backgroundColor: "hsl(var(--card))",
  border: "1px solid hsl(var(--border))",
  borderRadius: "var(--radius)",
};

const MEMBER_PER_PAGE = 20;

export function UsagePage() {
  const projectId = useCurrentProject();
  const [since, setSince] = useState(defaultSince);
  const [until, setUntil] = useState(defaultUntil);
  const [memberPage, setMemberPage] = useState(1);

  // Reset to page 1 whenever the date range changes, otherwise a narrower
  // range could leave the user stranded past the new last page.
  function handleSinceChange(v: string) {
    setSince(v);
    setMemberPage(1);
  }
  function handleUntilChange(v: string) {
    setUntil(v);
    setMemberPage(1);
  }

  const sinceISO = `${since}T00:00:00Z`;
  const untilISO = `${until}T23:59:59Z`;

  const { data: overview } = useUsageOverview(projectId, sinceISO, untilISO);
  const { data: daily } = useDailyUsage(projectId, sinceISO, untilISO);
  const { data: byModel } = useUsageByModel(projectId, sinceISO, untilISO);
  // Member ranking is pinned to the active subscription period server-side,
  // so don't forward the date picker — it would just churn the cache key
  // without changing the response.
  const { data: byMember } = useUsageByMember(
    projectId,
    undefined,
    undefined,
    memberPage,
    MEMBER_PER_PAGE,
  );

  const stats = overview?.data;
  const dailyData = daily?.data ?? [];
  const modelData = byModel?.data ?? [];
  const memberData = byMember?.data ?? [];
  const memberMeta = byMember?.meta;

  const modelColumns: Column<UsageSummary>[] = [
    { header: "Model", accessor: "model" },
    {
      header: "Credits",
      accessor: (r) => `${(r.total_credits_k ?? 0).toLocaleString()}K`,
      className: "text-right",
    },
    { header: "Requests", accessor: (r) => formatNumber(r.request_count), className: "text-right" },
    { header: "Input Tokens", accessor: (r) => formatNumber(r.total_input_tokens), className: "text-right" },
    { header: "Output Tokens", accessor: (r) => formatNumber(r.total_output_tokens), className: "text-right" },
    { header: "Avg Latency", accessor: (r) => `${Math.round(r.avg_latency_ms)}ms`, className: "text-right" },
  ];

  // Backend already returns rows sorted by credits desc. For the pie we drop
  // any models with zero credits in the period (no slice would render).
  const pieData = modelData.filter((r) => r.total_credits_k > 0);
  const totalCreditsK = pieData.reduce((s, r) => s + r.total_credits_k, 0);

  // Backend sorts by total credits consumed within the active subscription
  // period (DESC, stable tiebreaker on user_id), so the row's position in the
  // sorted list is (page-1)*perPage + localIndex. That global rank is what
  // decides the 🥇🥈🥉 medals — only rows on page 1 that fall into the top
  // three overall receive them.
  const pageOffset = (memberPage - 1) * MEMBER_PER_PAGE;
  const rankByUserId = new Map<string, number>(
    memberData.map((r, i) => [r.user_id, pageOffset + i]),
  );
  const medals = ["🥇", "🥈", "🥉"];

  const memberColumns: Column<UsageByMember>[] = [
    {
      header: "Member",
      accessor: (r) => {
        const name = r.nickname || r.user_id.slice(0, 8);
        const initials = (r.nickname || "?").slice(0, 2).toUpperCase();
        const rank = rankByUserId.get(r.user_id) ?? -1;
        const medal = rank >= 0 && rank < medals.length ? medals[rank] : null;
        return (
          <div className="flex items-center gap-2">
            {medal && <span className="text-lg leading-none">{medal}</span>}
            <Avatar size="sm">
              {r.picture && <AvatarImage src={r.picture} alt={name} />}
              <AvatarFallback>{initials}</AvatarFallback>
            </Avatar>
            <span>{name}</span>
          </div>
        );
      },
    },
    { header: "Requests", accessor: (r) => formatNumber(r.request_count), className: "text-right" },
    { header: "Tokens", accessor: (r) => formatNumber(r.total_tokens), className: "text-right" },
    {
      header: "Credits",
      accessor: (r) => `${(r.total_credits_k ?? 0).toLocaleString()}K`,
      className: "text-right",
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader title="Usage Analytics" />

      <DateRangePicker
        since={since}
        until={until}
        onSinceChange={handleSinceChange}
        onUntilChange={handleUntilChange}
      />

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard title="Total Requests" value={formatNumber(stats?.request_count ?? 0)} />
        <StatCard title="Total Tokens" value={formatNumber(stats?.total_tokens ?? 0)} />
        <StatCard title="Total Credits" value={`${(stats?.total_credits_k ?? 0).toLocaleString()}K`} />
        <ExtraUsageCard projectId={projectId} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Daily Usage</CardTitle>
        </CardHeader>
        <CardContent>
          {dailyData.length > 0 ? (
            <ResponsiveContainer width="100%" height={300}>
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
                <Tooltip contentStyle={tooltipStyle} />
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
            <p className="py-8 text-center text-muted-foreground">No data</p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Usage by Model</CardTitle>
          <p className="text-xs text-muted-foreground">
            Share of credits consumed per model in the selected window.
          </p>
        </CardHeader>
        <CardContent>
          {modelData.length > 0 ? (
            <>
              {pieData.length > 0 ? (
                <ResponsiveContainer width="100%" height={280}>
                  <PieChart>
                    <Pie
                      data={pieData}
                      dataKey="total_credits_k"
                      nameKey="model"
                      cx="50%"
                      cy="50%"
                      innerRadius={55}
                      outerRadius={100}
                      paddingAngle={1}
                      label={(entry) => {
                        const pct = totalCreditsK > 0
                          ? ((entry.total_credits_k / totalCreditsK) * 100)
                          : 0;
                        return pct >= 5 ? `${pct.toFixed(0)}%` : "";
                      }}
                      labelLine={false}
                    >
                      {pieData.map((_, i) => (
                        <Cell
                          key={i}
                          fill={MODEL_PIE_COLORS[i % MODEL_PIE_COLORS.length]}
                        />
                      ))}
                    </Pie>
                    <Tooltip
                      contentStyle={tooltipStyle}
                      formatter={(value: number, _name, item) => {
                        const pct = totalCreditsK > 0
                          ? ((value / totalCreditsK) * 100).toFixed(1)
                          : "0";
                        return [
                          `${value.toLocaleString()}K credits · ${pct}%`,
                          item?.payload?.model ?? "",
                        ];
                      }}
                    />
                    <Legend
                      verticalAlign="middle"
                      align="right"
                      layout="vertical"
                      iconType="circle"
                      wrapperStyle={{ fontSize: 12, paddingLeft: 16 }}
                    />
                  </PieChart>
                </ResponsiveContainer>
              ) : (
                <p className="py-8 text-center text-muted-foreground">
                  No credits consumed in the selected window.
                </p>
              )}
              <div className="mt-4">
                <DataTable
                  columns={modelColumns}
                  data={modelData}
                  keyFn={(r) => r.model}
                />
              </div>
            </>
          ) : (
            <p className="py-8 text-center text-muted-foreground">No data</p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Usage by Member</CardTitle>
          <p className="text-xs text-muted-foreground">
            Ranked by credits consumed in the current subscription period.
          </p>
        </CardHeader>
        <CardContent className="p-0">
          <DataTable
            columns={memberColumns}
            data={memberData}
            keyFn={(r) => r.user_id}
            emptyMessage="No data"
          />
        </CardContent>
        {memberMeta && memberMeta.total_pages > 1 && (
          <div className="border-t px-4 py-3">
            <Pagination
              page={memberMeta.page}
              totalPages={memberMeta.total_pages}
              total={memberMeta.total}
              perPage={memberMeta.per_page}
              onPageChange={setMemberPage}
            />
          </div>
        )}
      </Card>
    </div>
  );
}


function formatFen(fen: number): string {
  return `¥${(fen / 100).toFixed(2)}`;
}

function ExtraUsageCard({ projectId }: { projectId: string }) {
  const { data } = useExtraUsage(projectId);
  const s = data?.data;
  return (
    <Card className="flex flex-col">
      <CardHeader>
        <CardTitle className="text-sm font-medium">Extra Usage</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-1 flex-col justify-between">
        <div>
          <div className="text-2xl font-semibold">
            {formatFen(s?.balance_fen ?? 0)}
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {s?.enabled ? "Enabled" : "Disabled"} · this month{" "}
            {formatFen(s?.monthly_spent_fen ?? 0)}
          </div>
        </div>
        <Link
          to={`/projects/${projectId}/extra-usage`}
          className="mt-3 text-sm text-primary hover:underline"
        >
          Manage extra usage →
        </Link>
      </CardContent>
    </Card>
  );
}
