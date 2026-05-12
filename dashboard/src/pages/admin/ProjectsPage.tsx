import { useMemo, useState } from "react";
import {
  useAllProjects,
  useAdminProjectsSubscriptionsOverview,
  type ProjectOwnerSnapshot,
} from "@/api/projects";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Pagination } from "@/components/shared/Pagination";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Avatar, AvatarImage, AvatarFallback } from "@/components/ui/avatar";
import type { Project } from "@/api/types";
import { useNavigate } from "react-router";
import type { CreditWindowStatus } from "@/api/subscriptions";

function initials(name?: string): string {
  return (
    name
      ?.split(" ")
      .map((w) => w[0])
      .join("")
      .toUpperCase()
      .slice(0, 2) ?? "?"
  );
}

function UsageBar({ percentage }: { percentage: number }) {
  const clamped = Math.min(percentage, 100);
  const barColor =
    percentage > 95
      ? "bg-red-500"
      : percentage > 80
        ? "bg-yellow-500"
        : "bg-primary";
  return (
    <div className="flex items-center gap-2 w-24">
      <div className="h-1.5 flex-1 rounded-full bg-muted overflow-hidden">
        <div
          className={`h-full rounded-full transition-all ${barColor}`}
          style={{ width: `${clamped}%` }}
        />
      </div>
      <span className="text-[10px] text-muted-foreground w-8 text-right">{percentage.toFixed(0)}%</span>
    </div>
  );
}

const PER_PAGE = 20;

export function AdminProjectsPage() {
  const [page, setPage] = useState(1);
  const { data: projectsData, isLoading: loadingProjects } = useAllProjects(page, PER_PAGE);
  const navigate = useNavigate();

  const projects = projectsData?.data ?? [];
  const meta = projectsData?.meta;

  const projectIds = useMemo(() => projects.map((p) => p.id), [projects]);
  const { data: overviewData } = useAdminProjectsSubscriptionsOverview(projectIds);

  const projectPlanMap = new Map<string, string>();
  const projectUsageMap = new Map<string, CreditWindowStatus[]>();
  const ownerMap = new Map<string, ProjectOwnerSnapshot>();
  const periodCreditsKMap = new Map<string, number>();
  for (const row of overviewData?.data ?? []) {
    if (row.display_name || row.plan_name) {
      projectPlanMap.set(row.project_id, row.display_name || row.plan_name || "");
    }
    if (row.windows && row.windows.length > 0) {
      projectUsageMap.set(row.project_id, row.windows);
    }
    if (row.owner) {
      ownerMap.set(row.project_id, row.owner);
    }
    if (row.period_credits_k != null) {
      periodCreditsKMap.set(row.project_id, row.period_credits_k);
    }
  }

  const columns: Column<Project>[] = [
    {
      header: "ID",
      accessor: (p) => (
        <code className="text-xs text-muted-foreground">{p.id.slice(0, 8)}</code>
      ),
      className: "w-24",
    },
    { header: "Name", accessor: "name" },
    {
      header: "Owner",
      accessor: (p) => {
        const owner = ownerMap.get(p.id);
        if (!owner) return <span className="text-muted-foreground">-</span>;
        const label = owner.nickname || owner.email || "";
        return (
          <div className="flex items-center gap-2">
            <Avatar className="h-6 w-6">
              {owner.picture && <AvatarImage src={owner.picture} alt={label} />}
              <AvatarFallback className="text-[10px]">
                {initials(owner.nickname)}
              </AvatarFallback>
            </Avatar>
            <span className="truncate">{label}</span>
          </div>
        );
      },
    },
    {
      header: "Plan",
      accessor: (p) => {
        const planName = projectPlanMap.get(p.id);
        return planName ? (
          <Badge variant="outline">{planName}</Badge>
        ) : (
          <span className="text-muted-foreground">—</span>
        );
      },
    },
    {
      header: "5h Usage",
      accessor: (p) => {
        const s = projectUsageMap.get(p.id)?.find((s) => s.window === "5h");
        if (!s) return <span className="text-muted-foreground">—</span>;
        return <UsageBar percentage={s.percentage} />;
      },
    },
    {
      header: "7d Usage",
      accessor: (p) => {
        const s = projectUsageMap.get(p.id)?.find((s) => s.window === "7d");
        if (!s) return <span className="text-muted-foreground">—</span>;
        return <UsageBar percentage={s.percentage} />;
      },
    },
    {
      header: "Credits",
      accessor: (p) => {
        const k = periodCreditsKMap.get(p.id);
        if (k == null) return <span className="text-muted-foreground">—</span>;
        return <span className="tabular-nums">{k.toLocaleString()}K</span>;
      },
      className: "text-right",
    },
    {
      header: "Status",
      accessor: (p) => <StatusBadge status={p.status} />,
    },
    {
      header: "Created",
      accessor: (p) => new Date(p.created_at).toLocaleDateString(),
    },
  ];

  const isLoading = loadingProjects;

  return (
    <div className="space-y-6">
      <PageHeader
        title="Projects"
        description="Manage all projects (superadmin only)"
      />
      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <p className="p-6 text-muted-foreground">Loading...</p>
          ) : (
            <DataTable
              columns={columns}
              data={projects}
              keyFn={(p) => p.id}
              emptyMessage="No projects"
              onRowClick={(p) => navigate(`/projects/${p.id}`)}
            />
          )}
        </CardContent>
      </Card>

      {meta && meta.total > 0 && (
        <Pagination
          page={page}
          totalPages={meta.total_pages}
          total={meta.total}
          perPage={meta.per_page}
          onPageChange={setPage}
        />
      )}
    </div>
  );
}
