import { useState } from "react";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useUsageOverview, useUsageByModel, useDailyUsage, useUsageByKey } from "@/api/usage";
import { PageHeader } from "@/components/layout/PageHeader";
import { StatCard } from "@/components/shared/StatCard";
import { DateRangePicker } from "@/components/shared/DateRangePicker";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { UsageSummary, UsageByKey } from "@/api/types";
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

export function UsagePage() {
  const projectId = useCurrentProject();
  const [since, setSince] = useState(defaultSince);
  const [until, setUntil] = useState(defaultUntil);

  const sinceISO = `${since}T00:00:00Z`;
  const untilISO = `${until}T23:59:59Z`;

  const { data: overview } = useUsageOverview(projectId, sinceISO, untilISO);
  const { data: daily } = useDailyUsage(projectId, sinceISO, untilISO);
  const { data: byModel } = useUsageByModel(projectId, sinceISO, untilISO);
  const { data: byKey } = useUsageByKey(projectId, sinceISO, untilISO);

  const stats = overview?.data;
  const dailyData = daily?.data ?? [];
  const modelData = byModel?.data ?? [];
  const keyData = byKey?.data ?? [];

  const modelColumns: Column<UsageSummary>[] = [
    { header: "Model", accessor: "model" },
    { header: "Requests", accessor: (r) => formatNumber(r.request_count), className: "text-right" },
    { header: "Input Tokens", accessor: (r) => formatNumber(r.total_input_tokens), className: "text-right" },
    { header: "Output Tokens", accessor: (r) => formatNumber(r.total_output_tokens), className: "text-right" },
    { header: "Avg Latency", accessor: (r) => `${Math.round(r.avg_latency_ms)}ms`, className: "text-right" },
  ];

  const keyColumns: Column<UsageByKey>[] = [
    { header: "Key Name", accessor: "api_key_name" },
    { header: "Key", accessor: (r) => `ms-...${r.key_suffix}` },
    { header: "Requests", accessor: (r) => formatNumber(r.request_count), className: "text-right" },
    { header: "Tokens", accessor: (r) => formatNumber(r.total_tokens), className: "text-right" },
  ];

  return (
    <div className="space-y-6">
      <PageHeader title="Usage Analytics" />

      <DateRangePicker
        since={since}
        until={until}
        onSinceChange={setSince}
        onUntilChange={setUntil}
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
          <CardTitle className="text-base">Usage by API Key</CardTitle>
        </CardHeader>
        <CardContent>
          <DataTable
            columns={keyColumns}
            data={keyData}
            keyFn={(r) => r.api_key_id}
            emptyMessage="No data"
          />
        </CardContent>
      </Card>
    </div>
  );
}
