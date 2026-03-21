import { useState } from "react";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useAuth } from "@/hooks/useAuth";
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
import { Avatar, AvatarImage, AvatarFallback } from "@/components/ui/avatar";
import type { ProjectMember } from "@/api/types";
import { Plus, MoreHorizontal } from "lucide-react";

const roles = ["owner", "maintainer", "developer"] as const;

export function MembersPage() {
  const projectId = useCurrentProject();
  const { user: currentUser } = useAuth();
  const { data, isLoading } = useMembers(projectId);
  const addMember = useAddMember(projectId);
  const updateMember = useUpdateMember(projectId);
  const removeMember = useRemoveMember(projectId);

  const [showAdd, setShowAdd] = useState(false);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<string>("developer");

  // Quota dialog state
  const [showQuota, setShowQuota] = useState(false);
  const [quotaTarget, setQuotaTarget] = useState<ProjectMember | null>(null);
  const [quotaValue, setQuotaValue] = useState<string>("100");
  const [removeQuota, setRemoveQuota] = useState(false);

  const members = data?.data ?? [];

  // Determine current user's role in this project
  const currentMember = members.find((m) => m.user_id === currentUser?.id);
  const currentRole = currentMember?.role;
  const canManageQuota = currentRole === "owner" || currentRole === "maintainer";

  async function handleAdd() {
    await addMember.mutateAsync({ email, role });
    setShowAdd(false);
    setEmail("");
    setRole("developer");
  }

  function openQuotaDialog(m: ProjectMember) {
    setQuotaTarget(m);
    setQuotaValue(String(m.credit_quota_percent ?? 100));
    setRemoveQuota(false);
    setShowQuota(true);
  }

  async function handleSetQuota() {
    if (!quotaTarget) return;
    if (removeQuota) {
      await updateMember.mutateAsync({ userId: quotaTarget.user_id, clear_quota: true });
    } else {
      const parsed = parseFloat(quotaValue);
      if (isNaN(parsed) || parsed < 0 || parsed > 100) return;
      await updateMember.mutateAsync({ userId: quotaTarget.user_id, credit_quota_percent: parsed });
    }
    setShowQuota(false);
    setQuotaTarget(null);
  }

  const columns: Column<ProjectMember>[] = [
    {
      header: "User",
      accessor: (m) => {
        const name = m.user?.nickname || m.user?.email || m.user_id;
        const initials = (m.user?.nickname || m.user?.email || "?")
          .slice(0, 2)
          .toUpperCase();
        return (
          <div className="flex items-center gap-2">
            <Avatar size="sm">
              {m.user?.picture && <AvatarImage src={m.user.picture} alt={name} />}
              <AvatarFallback>{initials}</AvatarFallback>
            </Avatar>
            <span>{name}</span>
          </div>
        );
      },
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
      header: "Quota",
      accessor: (m) => `${m.credit_quota_percent ?? 100}%`,
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
            {canManageQuota &&
              m.role !== "owner" &&
              m.user_id !== currentUser?.id && (
                <DropdownMenuItem onClick={() => openQuotaDialog(m)}>
                  Set Quota
                </DropdownMenuItem>
              )}
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

      {/* Add Member Dialog */}
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

      {/* Set Quota Dialog */}
      <Dialog open={showQuota} onOpenChange={setShowQuota}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              Set Quota for{" "}
              {quotaTarget?.user?.nickname ||
                quotaTarget?.user?.email ||
                quotaTarget?.user_id}
            </DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="quota-value">Credit Quota (%)</Label>
              <Input
                id="quota-value"
                type="number"
                min={0}
                max={100}
                value={quotaValue}
                onChange={(e) => setQuotaValue(e.target.value)}
                disabled={removeQuota}
                placeholder="0–100"
              />
            </div>
            <div className="flex items-center gap-2">
              <input
                id="remove-quota"
                type="checkbox"
                checked={removeQuota}
                onChange={(e) => setRemoveQuota(e.target.checked)}
                className="h-4 w-4 rounded border border-input accent-primary"
              />
              <Label htmlFor="remove-quota">
                Remove quota (reset to 100%)
              </Label>
            </div>
          </div>
          <DialogFooter>
            <Button
              onClick={handleSetQuota}
              disabled={updateMember.isPending}
            >
              {updateMember.isPending ? "Saving..." : "Save Quota"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
