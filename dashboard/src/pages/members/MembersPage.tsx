import { useState, useMemo } from "react";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useAuth } from "@/hooks/useAuth";
import { useMembers, useMyMembership, useAddMember, useUpdateMember, useRemoveMember, useMembersUsage } from "@/api/members";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Pagination } from "@/components/shared/Pagination";
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
import type { ProjectMember, MemberUsage } from "@/api/types";
import { Plus, MoreHorizontal, Pencil } from "lucide-react";

const roles = ["owner", "maintainer", "developer"] as const;
const PER_PAGE = 20;

export function MembersPage() {
  const projectId = useCurrentProject();
  const { user: currentUser } = useAuth();
  const [page, setPage] = useState(1);
  const { data, isLoading } = useMembers(projectId, page, PER_PAGE);
  const addMember = useAddMember(projectId);
  const updateMember = useUpdateMember(projectId);
  const removeMember = useRemoveMember(projectId);

  const [showAdd, setShowAdd] = useState(false);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<string>("developer");
  const [addQuota, setAddQuota] = useState<string>("");

  // Quota dialog state
  const [showQuota, setShowQuota] = useState(false);
  const [quotaTarget, setQuotaTarget] = useState<ProjectMember | null>(null);
  const [quotaValue, setQuotaValue] = useState<string>("100");
  const [removeQuota, setRemoveQuota] = useState(false);

  const members = data?.data ?? [];
  const meta = data?.meta;

  // Fetch usage for current page members
  const userIds = useMemo(() => members.map((m) => m.user_id), [members]);
  const { data: usageData } = useMembersUsage(projectId, userIds);
  const usageMap = useMemo(() => {
    const m = new Map<string, MemberUsage>();
    for (const u of usageData?.data ?? []) {
      m.set(u.user_id, u);
    }
    return m;
  }, [usageData]);

  // Determine current user's role in this project (independent of pagination)
  const { data: myMembershipData } = useMyMembership(projectId);
  const currentRole = myMembershipData?.data?.role;
  const canManageQuota = currentRole === "owner" || currentRole === "maintainer";

  async function handleAdd() {
    const params: { email: string; role: string; credit_quota_percent?: number } = { email, role };
    if (addQuota !== "" && role !== "owner") {
      const parsed = parseFloat(addQuota);
      if (!isNaN(parsed) && parsed >= 0 && parsed <= 100) {
        params.credit_quota_percent = parsed;
      }
    }
    await addMember.mutateAsync(params);
    setShowAdd(false);
    setEmail("");
    setRole("developer");
    setAddQuota("");
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
      accessor: (m) => {
        const pct = m.credit_quota_percent ?? 100;
        const editable =
          canManageQuota &&
          m.role !== "owner" &&
          m.user_id !== currentUser?.id;
        if (editable) {
          return (
            <button
              type="button"
              className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-sm hover:bg-muted transition-colors"
              onClick={() => openQuotaDialog(m)}
            >
              {pct}%
              <Pencil className="h-3 w-3 text-muted-foreground" />
            </button>
          );
        }
        return `${pct}%`;
      },
    },
    {
      header: "Usage",
      accessor: (m) => {
        const usage = usageMap.get(m.user_id);
        if (!usage || usage.windows.length === 0) {
          return <span className="text-xs text-muted-foreground">—</span>;
        }
        return (
          <div className="flex flex-col gap-1 min-w-[120px]">
            {usage.windows.map((w) => {
              const pct = Math.min(w.percentage, 100);
              const isHigh = pct > 80;
              return (
                <div key={w.window} className="flex items-center gap-2">
                  <span className="text-xs text-muted-foreground w-6 shrink-0">{w.window}</span>
                  <div className="flex-1 h-2 rounded-full bg-muted overflow-hidden">
                    <div
                      className={`h-full rounded-full transition-all ${isHigh ? "bg-destructive" : "bg-primary"}`}
                      style={{ width: `${pct}%` }}
                    />
                  </div>
                  <span className={`text-xs tabular-nums w-12 text-right ${isHigh ? "text-destructive font-medium" : ""}`}>
                    {pct.toFixed(1)}%
                  </span>
                </div>
              );
            })}
          </div>
        );
      },
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
        {meta && meta.total_pages > 1 && (
          <div className="border-t px-4 py-3">
            <Pagination
              page={meta.page}
              totalPages={meta.total_pages}
              total={meta.total}
              perPage={meta.per_page}
              onPageChange={setPage}
            />
          </div>
        )}
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
            {role !== "owner" && (
              <div className="space-y-2">
                <Label htmlFor="add-quota">Credit Quota % (optional)</Label>
                <Input
                  id="add-quota"
                  type="number"
                  min={0}
                  max={100}
                  value={addQuota}
                  onChange={(e) => setAddQuota(e.target.value)}
                  placeholder="Leave empty for 100%"
                />
              </div>
            )}
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
