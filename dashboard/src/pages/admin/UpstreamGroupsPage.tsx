import { useState, useEffect } from "react";
import { useUpstreamGroups, useCreateUpstreamGroup, useDeleteUpstreamGroup, useAddGroupMember, useRemoveGroupMember, useUpstreams } from "@/api/upstreams";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Pagination } from "@/components/shared/Pagination";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogDescription,
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
import type { UpstreamGroupWithMembers } from "@/api/types";
import { Plus, MoreHorizontal, Trash2, Loader2, Users, X } from "lucide-react";
import { toast } from "sonner";

const lbPolicyLabels: Record<string, string> = {
  weighted_random: "Weighted Random",
  round_robin: "Round Robin",
  least_conn: "Least Connections",
};

const PER_PAGE = 20;

export function UpstreamGroupsPage() {
  const [page, setPage] = useState(1);
  const { data, isLoading } = useUpstreamGroups(page, PER_PAGE);
  const createGroup = useCreateUpstreamGroup();
  const deleteGroup = useDeleteUpstreamGroup();
  const addMember = useAddGroupMember();
  const removeMember = useRemoveGroupMember();
  const { data: upstreamsData } = useUpstreams(1, 100);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<UpstreamGroupWithMembers | null>(null);
  const [managingGroup, setManagingGroup] = useState<UpstreamGroupWithMembers | null>(null);
  const [memberForm, setMemberForm] = useState({ upstream_id: "", weight: "", is_backup: false });
  const [form, setForm] = useState({
    name: "",
    lb_policy: "weighted_random",
    status: "active",
  });

  const groups = data?.data ?? [];
  const meta = data?.meta;
  const allUpstreams = upstreamsData?.data ?? [];

  // Keep managingGroup in sync with fresh data after mutations
  useEffect(() => {
    if (managingGroup) {
      const fresh = groups.find((g) => g.id === managingGroup.id);
      if (fresh) setManagingGroup(fresh);
    }
  }, [groups]);

  function openCreate() {
    setForm({ name: "", lb_policy: "weighted_random", status: "active" });
    setDialogOpen(true);
  }

  async function handleCreate() {
    try {
      await createGroup.mutateAsync({
        name: form.name,
        lb_policy: form.lb_policy,
        status: form.status,
      });
      toast.success("Upstream group created");
      setDialogOpen(false);
    } catch {
      toast.error("Failed to create upstream group");
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    try {
      await deleteGroup.mutateAsync(deleteTarget.id);
      toast.success("Upstream group deleted");
    } catch {
      toast.error("Failed to delete upstream group");
    }
    setDeleteTarget(null);
  }

  async function handleAddMember() {
    if (!managingGroup || !memberForm.upstream_id) return;
    try {
      await addMember.mutateAsync({
        groupId: managingGroup.id,
        upstream_id: memberForm.upstream_id,
        weight: memberForm.weight ? Number(memberForm.weight) : undefined,
        is_backup: memberForm.is_backup,
      });
      toast.success("Member added");
      setMemberForm({ upstream_id: "", weight: "", is_backup: false });
    } catch {
      toast.error("Failed to add member");
    }
  }

  async function handleRemoveMember(upstreamId: string) {
    if (!managingGroup) return;
    try {
      await removeMember.mutateAsync({ groupId: managingGroup.id, upstreamId });
      toast.success("Member removed");
    } catch {
      toast.error("Failed to remove member");
    }
  }

  const memberIds = new Set(managingGroup?.members?.map((m) => m.upstream_id) ?? []);
  const availableUpstreams = allUpstreams.filter((u) => !memberIds.has(u.id));

  const columns: Column<UpstreamGroupWithMembers>[] = [
    {
      header: "ID",
      accessor: (g) => (
        <code className="text-xs text-muted-foreground">{g.id.slice(0, 8)}</code>
      ),
      className: "w-24",
    },
    { header: "Name", accessor: "name" },
    {
      header: "LB Policy",
      accessor: (g) => (
        <Badge variant="outline">{lbPolicyLabels[g.lb_policy] ?? g.lb_policy}</Badge>
      ),
    },
    {
      header: "Members",
      accessor: (g) => {
        const count = g.members?.length ?? 0;
        return (
          <span className="text-sm">
            {count} {count === 1 ? "member" : "members"}
          </span>
        );
      },
    },
    {
      header: "Retry Policy",
      accessor: (g) => {
        if (!g.retry_policy) return <span className="text-muted-foreground">{"\u2014"}</span>;
        return (
          <span className="text-sm">
            {g.retry_policy.max_retries} retries
          </span>
        );
      },
    },
    {
      header: "Status",
      accessor: (g) => <StatusBadge status={g.status} />,
    },
    {
      header: "",
      accessor: (g) => (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={<Button variant="ghost" size="icon" className="h-8 w-8" />}
          >
            <MoreHorizontal className="h-4 w-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              onClick={() => {
                setMemberForm({ upstream_id: "", weight: "", is_backup: false });
                setManagingGroup(g);
              }}
            >
              <Users className="mr-2 h-4 w-4" />
              Manage Members
            </DropdownMenuItem>
            <DropdownMenuItem
              className="text-destructive-foreground"
              onClick={() => setDeleteTarget(g)}
            >
              <Trash2 className="mr-2 h-4 w-4" />
              Delete
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
        title="Upstream Groups"
        description="Manage groups of upstreams with load balancing policies (superadmin only)"
        actions={
          <Button onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            Add Group
          </Button>
        }
      />

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <div className="flex items-center gap-2 p-6 text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading...
            </div>
          ) : (
            <DataTable
              columns={columns}
              data={groups}
              keyFn={(g) => g.id}
              emptyMessage="No upstream groups configured"
              onRowClick={(g) => {
                setMemberForm({ upstream_id: "", weight: "", is_backup: false });
                setManagingGroup(g);
              }}
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

      {/* Create Dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>Add Upstream Group</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label>Name</Label>
              <Input
                value={form.name}
                onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))}
                placeholder="primary-group"
              />
            </div>
            <div className="space-y-2">
              <Label>Load Balancing Policy</Label>
              <Select
                value={form.lb_policy}
                onValueChange={(v) => setForm((p) => ({ ...p, lb_policy: v ?? "weighted_random" }))}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="weighted_random">Weighted Random</SelectItem>
                  <SelectItem value="round_robin">Round Robin</SelectItem>
                  <SelectItem value="least_conn">Least Connections</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>Status</Label>
              <Select
                value={form.status}
                onValueChange={(v) => setForm((p) => ({ ...p, status: v ?? "active" }))}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="active">Active</SelectItem>
                  <SelectItem value="disabled">Disabled</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleCreate}
              disabled={!form.name || createGroup.isPending}
            >
              {createGroup.isPending ? "Creating..." : "Create Group"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation */}
      <Dialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Upstream Group</DialogTitle>
            <DialogDescription>
              Delete group "{deleteTarget?.name}"? This cannot be undone and may break
              existing routes that reference this group.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleDelete}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {/* Manage Members Dialog */}
      <Dialog open={!!managingGroup} onOpenChange={(open) => !open && setManagingGroup(null)}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Manage Members — {managingGroup?.name}</DialogTitle>
            <DialogDescription>
              Add or remove upstreams from this group.
            </DialogDescription>
          </DialogHeader>

          {/* Current members */}
          <div className="space-y-2">
            <Label>Current Members</Label>
            {(!managingGroup?.members || managingGroup.members.length === 0) ? (
              <p className="text-sm text-muted-foreground py-2">No members yet. Add an upstream below.</p>
            ) : (
              <div className="space-y-1">
                {managingGroup.members.map((m) => (
                  <div key={m.upstream_id} className="flex items-center justify-between rounded-md border px-3 py-2 text-sm">
                    <div className="flex items-center gap-2">
                      <span className="font-medium">{m.upstream?.name ?? m.upstream_id.slice(0, 8)}</span>
                      {m.upstream?.provider && (
                        <Badge variant="outline" className="text-xs">{m.upstream.provider}</Badge>
                      )}
                      <span className="text-muted-foreground">
                        w: {m.weight ?? "default"}
                      </span>
                      {m.is_backup && <Badge variant="secondary" className="text-xs">backup</Badge>}
                    </div>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 text-muted-foreground hover:text-destructive-foreground"
                      onClick={() => handleRemoveMember(m.upstream_id)}
                      disabled={removeMember.isPending}
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </div>

          {/* Add member form */}
          <div className="space-y-3 border-t pt-4">
            <Label>Add Member</Label>
            <div className="flex gap-2">
              <Select
                value={memberForm.upstream_id}
                onValueChange={(v) => setMemberForm((p) => ({ ...p, upstream_id: v ?? "" }))}
              >
                <SelectTrigger className="flex-1">
                  <SelectValue placeholder="Select upstream..." />
                </SelectTrigger>
                <SelectContent>
                  {availableUpstreams.map((u) => (
                    <SelectItem key={u.id} value={u.id}>
                      {u.name} ({u.provider})
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Input
                className="w-20"
                placeholder="Weight"
                type="number"
                min={0}
                value={memberForm.weight}
                onChange={(e) => setMemberForm((p) => ({ ...p, weight: e.target.value }))}
              />
            </div>
            <div className="flex items-center gap-4">
              <label className="flex items-center gap-2 text-sm cursor-pointer">
                <input
                  type="checkbox"
                  checked={memberForm.is_backup}
                  onChange={(e) => setMemberForm((p) => ({ ...p, is_backup: e.target.checked }))}
                  className="rounded"
                />
                Is Backup
              </label>
              <Button
                size="sm"
                onClick={handleAddMember}
                disabled={!memberForm.upstream_id || addMember.isPending}
              >
                {addMember.isPending ? "Adding..." : "Add"}
              </Button>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
