import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useProject } from "@/api/projects";
import { useUsageOverview, useDailyUsage } from "@/api/usage";
import { useRequests } from "@/api/requests";
import { useMyQuota } from "@/api/members";
import { PageHeader } from "@/components/layout/PageHeader";
import { StatCard } from "@/components/shared/StatCard";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { Request } from "@/api/types";
import { useAuth } from "@/hooks/useAuth";
import { Activity, Zap, Clock, Coins } from "lucide-react";
import { BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, Legend } from "recharts";

function formatNumber(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

function formatCredits(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(2)}K`;
  return n.toFixed(2);
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
  const { user } = useAuth();

  const overview = usage?.data;
  const dailyData = daily?.data ?? [];
  const recentRequests = recentData?.data ?? [];
  const myQuota = myQuotaData?.data;

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
          description="Last 30 days"
          icon={<Activity className="h-4 w-4" />}
        />
        <StatCard
          title="Total Tokens"
          value={formatNumber(overview?.total_tokens ?? 0)}
          description="Last 30 days"
          icon={<Zap className="h-4 w-4" />}
        />
        <StatCard
          title="Total Credits"
          value={formatCredits(overview?.total_credits ?? 0)}
          description="Last 30 days"
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

      {myQuota && myQuota.credit_quota_percent !== null && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">My Quota</CardTitle>
            <p className="text-sm text-muted-foreground">
              {myQuota.credit_quota_percent}% of project quota
            </p>
          </CardHeader>
          <CardContent className="space-y-4">
            {myQuota.windows.map((w) => (
              <div key={w.window} className="space-y-1">
                <div className="flex justify-between text-sm">
                  <span className="capitalize">{w.window}</span>
                  <span className="text-muted-foreground">
                    {user?.is_superadmin
                      ? `${Math.round(w.used ?? 0).toLocaleString()} / ${(w.limit ?? 0).toLocaleString()}`
                      : `${w.percentage.toFixed(2)}%`}
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
            ))}
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
              <BarChart data={dailyData}>
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
                <Bar
                  yAxisId="left"
                  dataKey="request_count"
                  name="Requests"
                  fill="oklch(0.488 0.243 264.376)"
                  radius={[4, 4, 0, 0]}
                />
                <Bar
                  yAxisId="right"
                  dataKey="total_credits"
                  name="Credits"
                  fill="oklch(0.696 0.17 162.48)"
                  radius={[4, 4, 0, 0]}
                />
              </BarChart>
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
