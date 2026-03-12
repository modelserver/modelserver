import { useProjects } from "@/api/projects";
import { useUsers } from "@/api/users";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Card, CardContent } from "@/components/ui/card";
import { Avatar, AvatarImage, AvatarFallback } from "@/components/ui/avatar";
import type { Project, User } from "@/api/types";
import { useNavigate } from "react-router";

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
  const { data: projectsData, isLoading: loadingProjects } = useProjects();
  const { data: usersData, isLoading: loadingUsers } = useUsers();
  const navigate = useNavigate();

  const projects = projectsData?.data ?? [];
  const users = usersData?.data ?? [];

  const userMap = new Map<string, User>();
  for (const u of users) {
    userMap.set(u.id, u);
  }

  const columns: Column<Project>[] = [
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
                <AvatarImage src={owner.picture} alt={owner.name || owner.email} />
              )}
              <AvatarFallback className="text-[10px]">
                {initials(owner.name)}
              </AvatarFallback>
            </Avatar>
            <span className="truncate">{owner.name || owner.email}</span>
          </div>
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
