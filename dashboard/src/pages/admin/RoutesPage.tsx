import { useState, useMemo } from "react";
import {
  useRoutingRoutes,
  useCreateRoutingRoute,
  useDeleteRoutingRoute,
  useUpstreamGroups,
} from "@/api/upstreams";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
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
import type { RoutingRoute, UpstreamGroupWithMembers } from "@/api/types";
import { Plus, MoreHorizontal, Trash2, Loader2 } from "lucide-react";
import { toast } from "sonner";

export function RoutesPage() {
  const { data: routesData, isLoading } = useRoutingRoutes();
  const { data: groupsData } = useUpstreamGroups();
  const createRoute = useCreateRoutingRoute();
  const deleteRoute = useDeleteRoutingRoute();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<RoutingRoute | null>(null);
  const [form, setForm] = useState({
    model_pattern: "",
    upstream_group_id: "",
    match_priority: 0,
    status: "active",
  });

  const routes = routesData?.data ?? [];
  const groups = groupsData?.data ?? [];

  const groupMap = useMemo(() => {
    const m = new Map<string, UpstreamGroupWithMembers>();
    for (const g of groups) m.set(g.id, g);
    return m;
  }, [groups]);

  function openCreate() {
    setForm({ model_pattern: "", upstream_group_id: "", match_priority: 0, status: "active" });
    setDialogOpen(true);
  }

  async function handleCreate() {
    try {
      await createRoute.mutateAsync(form);
      toast.success("Route created");
      setDialogOpen(false);
    } catch {
      toast.error("Failed to create route");
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    try {
      await deleteRoute.mutateAsync(deleteTarget.id);
      toast.success("Route deleted");
    } catch {
      toast.error("Failed to delete route");
    }
    setDeleteTarget(null);
  }

  const isSaving = createRoute.isPending;

  const columns: Column<RoutingRoute>[] = [
    {
      header: "ID",
      accessor: (r) => (
        <code className="text-xs text-muted-foreground">{r.id.slice(0, 8)}</code>
      ),
      className: "w-24",
    },
    {
      header: "Model Pattern",
      accessor: (r) => <code className="text-sm">{r.model_pattern}</code>,
    },
    {
      header: "Upstream Group",
      accessor: (r) => {
        const group = groupMap.get(r.upstream_group_id);
        return (
          <Badge variant="outline" className="text-xs">
            {group?.name ?? r.upstream_group_id.slice(0, 8)}
          </Badge>
        );
      },
    },
    {
      header: "Priority",
      accessor: (r) => String(r.match_priority),
      className: "text-right w-20",
    },
    {
      header: "Scope",
      accessor: (r) =>
        r.project_id ? (
          <Badge variant="outline" className="text-xs">Project</Badge>
        ) : (
          <Badge variant="secondary" className="text-xs">Global</Badge>
        ),
      className: "w-20",
    },
    {
      header: "Status",
      accessor: (r) => (
        <Badge variant={r.status === "active" ? "default" : "secondary"}>
          {r.status === "active" ? "Active" : "Disabled"}
        </Badge>
      ),
    },
    {
      header: "",
      accessor: (r) => (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={<Button variant="ghost" size="icon" className="h-8 w-8" />}
          >
            <MoreHorizontal className="h-4 w-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              className="text-destructive-foreground"
              onClick={() => setDeleteTarget(r)}
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
        title="Routes"
        description="Route model requests to upstream groups based on pattern matching (superadmin only)"
        actions={
          <Button onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            Add Route
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
              data={routes}
              keyFn={(r) => r.id}
              emptyMessage="No routes configured — requests will fall back to default upstream group selection"
            />
          )}
        </CardContent>
      </Card>

      {/* Create Dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>Create Route</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label>Model Pattern</Label>
              <Input
                value={form.model_pattern}
                onChange={(e) => setForm((p) => ({ ...p, model_pattern: e.target.value }))}
                placeholder="claude-sonnet-*"
              />
              <p className="text-xs text-muted-foreground">
                Supports glob patterns: * matches any sequence, ? matches a single character
              </p>
            </div>
            <div className="space-y-2">
              <Label>Match Priority</Label>
              <Input
                type="number"
                value={form.match_priority}
                onChange={(e) =>
                  setForm((p) => ({ ...p, match_priority: Number(e.target.value) || 0 }))
                }
              />
              <p className="text-xs text-muted-foreground">
                Higher priority routes are evaluated first
              </p>
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
            <div className="space-y-2">
              <Label>Upstream Group</Label>
              {groups.length === 0 ? (
                <p className="text-sm text-muted-foreground">No upstream groups available</p>
              ) : (
                <Select
                  value={form.upstream_group_id}
                  onValueChange={(v) => setForm((p) => ({ ...p, upstream_group_id: v ?? "" }))}
                >
                  <SelectTrigger>
                    <SelectValue placeholder="Select an upstream group" />
                  </SelectTrigger>
                  <SelectContent>
                    {groups.map((g) => (
                      <SelectItem key={g.id} value={g.id}>
                        {g.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleCreate}
              disabled={!form.model_pattern || !form.upstream_group_id || isSaving}
            >
              {isSaving ? "Saving..." : "Create"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation */}
      <Dialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Route</DialogTitle>
            <DialogDescription>
              Delete the route for pattern "{deleteTarget?.model_pattern}"?
              Requests will fall back to default upstream group selection.
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
    </div>
  );
}
