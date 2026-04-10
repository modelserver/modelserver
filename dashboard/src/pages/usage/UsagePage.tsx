import { useState } from "react";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useUsageOverview, useUsageByModel, useDailyUsage, useUsageByMember } from "@/api/usage";
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
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from "recharts";

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
  const { data: byMember } = useUsageByMember(
    projectId,
    sinceISO,
    untilISO,
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
    { header: "Requests", accessor: (r) => formatNumber(r.request_count), className: "text-right" },
    { header: "Input Tokens", accessor: (r) => formatNumber(r.total_input_tokens), className: "text-right" },
    { header: "Output Tokens", accessor: (r) => formatNumber(r.total_output_tokens), className: "text-right" },
    { header: "Avg Latency", accessor: (r) => `${Math.round(r.avg_latency_ms)}ms`, className: "text-right" },
  ];

  // Backend sorts by total_tokens DESC with a stable tiebreaker, so the row's
  // position within the full sorted list is (page-1)*perPage + localIndex.
  // That global rank is what decides the 🥇🥈🥉 medals — only rows on page 1
  // that fall into the top three overall receive them.
  const pageOffset = (memberPage - 1) * MEMBER_PER_PAGE;
  const rankByUserId = new Map<string, number>(
    memberData.map((r, i) => [r.user_id, pageOffset + i]),
  );
  const medals = ["🥇", "🥈", "🥉"];

  const memberColumns: Column<UsageByMember>[] = [
    {
      header: "Member",
      accessor: (r) => {
        const name = r.nickname || r.email || r.user_id;
        const initials = (r.nickname || r.email || "?").slice(0, 2).toUpperCase();
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
    { header: "Email", accessor: (r) => r.email || "—" },
    { header: "Requests", accessor: (r) => formatNumber(r.request_count), className: "text-right" },
    { header: "Tokens", accessor: (r) => formatNumber(r.total_tokens), className: "text-right" },
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

      <div className="grid gap-4 sm:grid-cols-2">
        <StatCard title="Total Requests" value={formatNumber(stats?.request_count ?? 0)} />
        <StatCard title="Total Tokens" value={formatNumber(stats?.total_tokens ?? 0)} />
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
                <YAxis fontSize={12} stroke="currentColor" opacity={0.5} />
                <Tooltip contentStyle={tooltipStyle} />
                <Line
                  type="monotone"
                  dataKey="request_count"
                  stroke="oklch(0.488 0.243 264.376)"
                  strokeWidth={2}
                  dot={false}
                  name="Requests"
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
        </CardHeader>
        <CardContent>
          {modelData.length > 0 ? (
            <>
              <ResponsiveContainer width="100%" height={250}>
                <BarChart data={modelData}>
                  <XAxis dataKey="model" fontSize={12} stroke="currentColor" opacity={0.5} />
                  <YAxis fontSize={12} stroke="currentColor" opacity={0.5} />
                  <Tooltip contentStyle={tooltipStyle} />
                  <Bar
                    dataKey="request_count"
                    fill="oklch(0.488 0.243 264.376)"
                    radius={[4, 4, 0, 0]}
                    name="Requests"
                  />
                </BarChart>
              </ResponsiveContainer>
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
