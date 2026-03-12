import { useState } from "react";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useMembers, useAddMember, useUpdateMember, useRemoveMember } from "@/api/members";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import type { ProjectMember } from "@/api/types";
import { Plus, MoreHorizontal } from "lucide-react";

const roles = ["owner", "maintainer", "developer"] as const;

export function MembersPage() {
  const projectId = useCurrentProject();
  const { data, isLoading } = useMembers(projectId);
  const addMember = useAddMember(projectId);
  const updateMember = useUpdateMember(projectId);
  const removeMember = useRemoveMember(projectId);

  const [showAdd, setShowAdd] = useState(false);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<string>("developer");

  const members = data?.data ?? [];

  async function handleAdd() {
    await addMember.mutateAsync({ email, role });
    setShowAdd(false);
    setEmail("");
    setRole("developer");
  }

  const columns: Column<ProjectMember>[] = [
    {
      header: "User",
      accessor: (m) => m.user?.nickname || m.user?.email || m.user_id,
    },
    {
      header: "Email",
      accessor: (m) => m.user?.email ?? "",
    },
    {
      header: "Role",
      accessor: (m) => (
        <Badge variant="outline" className="capitalize">
          {m.role}
        </Badge>
      ),
    },
    {
      header: "Joined",
      accessor: (m) => new Date(m.created_at).toLocaleDateString(),
    },
    {
      header: "",
      accessor: (m) => (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={<Button variant="ghost" size="icon" className="h-8 w-8" />}
          >
            <MoreHorizontal className="h-4 w-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {roles
              .filter((r) => r !== m.role)
              .map((r) => (
                <DropdownMenuItem
                  key={r}
                  onClick={() =>
                    updateMember.mutate({ userId: m.user_id, role: r })
                  }
                >
                  Change to {r}
                </DropdownMenuItem>
              ))}
            <DropdownMenuItem
              className="text-destructive-foreground"
              onClick={() => removeMember.mutate(m.user_id)}
            >
              Remove
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      ),
      className: "w-12",
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Members"
        description="Manage project members and roles"
        actions={
          <Button onClick={() => setShowAdd(true)}>
            <Plus className="mr-2 h-4 w-4" />
            Add Member
          </Button>
        }
      />

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <p className="p-6 text-muted-foreground">Loading...</p>
          ) : (
            <DataTable
              columns={columns}
              data={members}
              keyFn={(m) => m.user_id}
              emptyMessage="No members"
            />
          )}
        </CardContent>
      </Card>

      <Dialog open={showAdd} onOpenChange={setShowAdd}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add Member</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="member-email">Email</Label>
              <Input
                id="member-email"
                type="email"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder="user@example.com"
              />
            </div>
            <div className="space-y-2">
              <Label>Role</Label>
              <Select value={role} onValueChange={(v) => { if (v) setRole(v); }}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {roles.map((r) => (
                    <SelectItem key={r} value={r} className="capitalize">
                      {r}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button
              onClick={handleAdd}
              disabled={!email || addMember.isPending}
            >
              {addMember.isPending ? "Adding..." : "Add Member"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
