import { useState, useMemo } from "react";
import {
  useChannels,
  useChannelRoutes,
  useCreateChannelRoute,
  useUpdateChannelRoute,
  useDeleteChannelRoute,
} from "@/api/channels";
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
import type { ChannelRoute, Channel } from "@/api/types";
import { Plus, MoreHorizontal, Pencil, Trash2, Loader2 } from "lucide-react";
import { toast } from "sonner";

export function RoutesPage() {
  const { data: routesData, isLoading } = useChannelRoutes();
  const { data: channelsData } = useChannels();
  const createRoute = useCreateChannelRoute();
  const updateRoute = useUpdateChannelRoute();
  const deleteRoute = useDeleteChannelRoute();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ChannelRoute | null>(null);
  const [form, setForm] = useState({
    model_pattern: "",
    channel_ids: [] as string[],
    match_priority: 0,
    enabled: true,
  });

  const routes = routesData?.data ?? [];
  const channels = channelsData?.data ?? [];

  const channelMap = useMemo(() => {
    const m = new Map<string, Channel>();
    for (const c of channels) m.set(c.id, c);
    return m;
  }, [channels]);

  function openCreate() {
    setEditingId(null);
    setForm({ model_pattern: "", channel_ids: [], match_priority: 0, enabled: true });
    setDialogOpen(true);
  }

  function openEdit(r: ChannelRoute) {
    setEditingId(r.id);
    setForm({
      model_pattern: r.model_pattern,
      channel_ids: r.channel_ids,
      match_priority: r.match_priority,
      enabled: r.enabled,
    });
    setDialogOpen(true);
  }

  async function handleSave() {
    try {
      if (editingId) {
        await updateRoute.mutateAsync({ routeId: editingId, ...form });
        toast.success("Route updated");
      } else {
        await createRoute.mutateAsync(form);
        toast.success("Route created");
      }
      setDialogOpen(false);
    } catch {
      toast.error("Failed to save route");
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

  async function toggleEnabled(r: ChannelRoute) {
    try {
      await updateRoute.mutateAsync({ routeId: r.id, enabled: !r.enabled });
      toast.success(r.enabled ? "Route disabled" : "Route enabled");
    } catch {
      toast.error("Failed to update route");
    }
  }

  function toggleChannel(channelId: string) {
    setForm((prev) => ({
      ...prev,
      channel_ids: prev.channel_ids.includes(channelId)
        ? prev.channel_ids.filter((id) => id !== channelId)
        : [...prev.channel_ids, channelId],
    }));
  }

  const isSaving = createRoute.isPending || updateRoute.isPending;

  const columns: Column<ChannelRoute>[] = [
    {
      header: "Model Pattern",
      accessor: (r) => <code className="text-sm">{r.model_pattern}</code>,
    },
    {
      header: "Channels",
      accessor: (r) => (
        <div className="flex flex-wrap gap-1">
          {r.channel_ids.map((id) => {
            const ch = channelMap.get(id);
            return (
              <Badge key={id} variant="outline" className="text-xs">
                {ch?.name ?? id.slice(0, 8)}
              </Badge>
            );
          })}
        </div>
      ),
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
        <Badge variant={r.enabled ? "default" : "secondary"}>
          {r.enabled ? "Enabled" : "Disabled"}
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
            <DropdownMenuItem onClick={() => openEdit(r)}>
              <Pencil className="mr-2 h-4 w-4" />
              Edit
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => toggleEnabled(r)}>
              {r.enabled ? "Disable" : "Enable"}
            </DropdownMenuItem>
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
        title="Channel Routes"
        description="Route model requests to specific channels based on pattern matching (superadmin only)"
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
              emptyMessage="No routes configured — requests will fall back to all matching channels"
            />
          )}
        </CardContent>
      </Card>

      {/* Create / Edit Dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{editingId ? "Edit Route" : "Create Route"}</DialogTitle>
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
              <Label>Enabled</Label>
              <Select
                value={form.enabled ? "yes" : "no"}
                onValueChange={(v) => setForm((p) => ({ ...p, enabled: v === "yes" }))}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="yes">Enabled</SelectItem>
                  <SelectItem value="no">Disabled</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>Channels</Label>
              {channels.length === 0 ? (
                <p className="text-sm text-muted-foreground">No channels available</p>
              ) : (
                <div className="space-y-1 max-h-48 overflow-y-auto rounded border p-2">
                  {channels.map((ch) => (
                    <label
                      key={ch.id}
                      className="flex items-center gap-2 rounded px-2 py-1.5 hover:bg-accent cursor-pointer"
                    >
                      <input
                        type="checkbox"
                        checked={form.channel_ids.includes(ch.id)}
                        onChange={() => toggleChannel(ch.id)}
                        className="rounded"
                      />
                      <span className="text-sm">{ch.name}</span>
                      <span className="text-xs text-muted-foreground ml-auto">{ch.provider}</span>
                    </label>
                  ))}
                </div>
              )}
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleSave}
              disabled={!form.model_pattern || form.channel_ids.length === 0 || isSaving}
            >
              {isSaving ? "Saving..." : editingId ? "Update" : "Create"}
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
              Requests will fall back to the default channel selection.
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
