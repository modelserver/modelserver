import { useUsers, useUpdateUser } from "@/api/users";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Avatar, AvatarImage, AvatarFallback } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";
import type { User } from "@/api/types";
import { MoreHorizontal } from "lucide-react";

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

export function UsersPage() {
  const { data, isLoading } = useUsers();
  const updateUser = useUpdateUser();
  const users = data?.data ?? [];

  const columns: Column<User>[] = [
    {
      header: "ID",
      accessor: (u) => (
        <code className="text-xs text-muted-foreground">{u.id.slice(0, 8)}</code>
      ),
      className: "w-24",
    },
    {
      header: "User",
      accessor: (u) => (
        <div className="flex items-center gap-2">
          <Avatar className="h-7 w-7">
            {u.picture && (
              <AvatarImage src={u.picture} alt={u.nickname || u.email} />
            )}
            <AvatarFallback className="text-[10px]">
              {initials(u.nickname)}
            </AvatarFallback>
          </Avatar>
          <div className="min-w-0">
            <p className="truncate text-sm font-medium">{u.nickname || u.email}</p>
            {u.nickname && (
              <p className="truncate text-xs text-muted-foreground">{u.email}</p>
            )}
          </div>
        </div>
      ),
    },
    {
      header: "Status",
      accessor: (u) => <StatusBadge status={u.status} />,
    },
    {
      header: "Role",
      accessor: (u) =>
        u.is_superadmin ? (
          <Badge variant="outline" className="bg-primary/10 text-primary">
            Superadmin
          </Badge>
        ) : (
          <Badge variant="outline">User</Badge>
        ),
    },
    {
      header: "Projects",
      accessor: (u) => String(u.max_projects),
      className: "text-right",
    },
    {
      header: "Joined",
      accessor: (u) => new Date(u.created_at).toLocaleDateString(),
    },
    {
      header: "",
      accessor: (u) => (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={<Button variant="ghost" size="icon" className="h-8 w-8" />}
          >
            <MoreHorizontal className="h-4 w-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {u.status === "active" ? (
              <DropdownMenuItem
                onClick={() =>
                  updateUser.mutate({ userId: u.id, status: "disabled" })
                }
              >
                Disable
              </DropdownMenuItem>
            ) : (
              <DropdownMenuItem
                onClick={() =>
                  updateUser.mutate({ userId: u.id, status: "active" })
                }
              >
                Enable
              </DropdownMenuItem>
            )}
            <DropdownMenuItem
              onClick={() =>
                updateUser.mutate({
                  userId: u.id,
                  is_superadmin: !u.is_superadmin,
                })
              }
            >
              {u.is_superadmin ? "Remove superadmin" : "Make superadmin"}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      ),
      className: "w-12",
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader title="Users" description="Manage all system users (superadmin only)" />
      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <p className="p-6 text-muted-foreground">Loading...</p>
          ) : (
            <DataTable
              columns={columns}
              data={users}
              keyFn={(u) => u.id}
              emptyMessage="No users"
            />
          )}
        </CardContent>
      </Card>
    </div>
  );
}
