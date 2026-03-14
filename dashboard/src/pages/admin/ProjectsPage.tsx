import { useAllProjects } from "@/api/projects";
import { useUsers } from "@/api/users";
import { usePlans } from "@/api/plans";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Avatar, AvatarImage, AvatarFallback } from "@/components/ui/avatar";
import type { Project, User, Plan } from "@/api/types";
import { useNavigate } from "react-router";
import { useQueries } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { DataResponse, Subscription } from "@/api/types";

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

export function AdminProjectsPage() {
  const { data: projectsData, isLoading: loadingProjects } = useAllProjects();
  const { data: usersData, isLoading: loadingUsers } = useUsers();
  const { data: plansData } = usePlans();
  const navigate = useNavigate();

  const projects = projectsData?.data ?? [];
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
    </div>
  );
}
