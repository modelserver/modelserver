import { useState } from "react";
import { useAllProjects } from "@/api/projects";
import { useUsers } from "@/api/users";
import { usePlans } from "@/api/plans";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Pagination } from "@/components/shared/Pagination";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Avatar, AvatarImage, AvatarFallback } from "@/components/ui/avatar";
import type { Project, User, Plan } from "@/api/types";
import { useNavigate } from "react-router";
import { useQueries } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { DataResponse, Subscription } from "@/api/types";
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
  const { data: usersData, isLoading: loadingUsers } = useUsers(1, 100);
  const { data: plansData } = usePlans(1, 100);
  const navigate = useNavigate();

  const projects = projectsData?.data ?? [];
  const meta = projectsData?.meta;
  const users = usersData?.data ?? [];
  const plans = plansData?.data ?? [];

  const planMap = new Map<string, Plan>();
  for (const p of plans) planMap.set(p.id, p);

  // Fetch active subscription for each project in parallel.
  const subQueries = useQueries({
    queries: projects.map((p) => ({
      queryKey: ["subscriptions", p.id],
      queryFn: () =>
        api.get<DataResponse<Subscription[]>>(
          `/api/v1/projects/${p.id}/subscriptions`,
        ),
      enabled: projects.length > 0,
    })),
  });

  // Fetch usage for each project in parallel.
  const usageQueries = useQueries({
    queries: projects.map((p) => ({
      queryKey: ["subscription-usage", p.id],
      queryFn: () =>
        api.get<DataResponse<CreditWindowStatus[]>>(
          `/api/v1/projects/${p.id}/subscription/usage`,
        ),
      enabled: projects.length > 0,
    })),
  });

  // Map project ID -> active subscription's plan name.
  const projectPlanMap = new Map<string, string>();
  for (let i = 0; i < projects.length; i++) {
    const subs = subQueries[i]?.data?.data ?? [];
    const active = subs.find((s) => s.status === "active");
    if (active) {
      const plan = active.plan_id ? planMap.get(active.plan_id) : undefined;
      projectPlanMap.set(projects[i]!.id, plan?.display_name || active.plan_name);
    }
  }

  // Map project ID -> usage statuses.
  const projectUsageMap = new Map<string, CreditWindowStatus[]>();
  for (let i = 0; i < projects.length; i++) {
    const statuses = usageQueries[i]?.data?.data;
    if (statuses && statuses.length > 0) {
      projectUsageMap.set(projects[i]!.id, statuses);
    }
  }

  const userMap = new Map<string, User>();
  for (const u of users) {
    userMap.set(u.id, u);
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
        const owner = userMap.get(p.created_by);
        if (!owner) return <span className="text-muted-foreground">-</span>;
        return (
          <div className="flex items-center gap-2">
            <Avatar className="h-6 w-6">
              {owner.picture && (
                <AvatarImage src={owner.picture} alt={owner.nickname || owner.email} />
              )}
              <AvatarFallback className="text-[10px]">
                {initials(owner.nickname)}
              </AvatarFallback>
            </Avatar>
            <span className="truncate">{owner.nickname || owner.email}</span>
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
      header: "Status",
      accessor: (p) => <StatusBadge status={p.status} />,
    },
    {
      header: "Created",
      accessor: (p) => new Date(p.created_at).toLocaleDateString(),
    },
  ];

  const isLoading = loadingProjects || loadingUsers;

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
