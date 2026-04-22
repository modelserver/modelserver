import { useState, useMemo } from "react";
import {
  useRoutingRoutes,
  useCreateRoutingRoute,
  useUpdateRoutingRoute,
  useDeleteRoutingRoute,
  useUpstreamGroups,
  useRequestKinds,
} from "@/api/upstreams";
import { useAllProjects } from "@/api/projects";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Pagination } from "@/components/shared/Pagination";
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
import type { RoutingRoute, UpstreamGroupWithMembers, Project } from "@/api/types";
import { ModelMultiSelect } from "@/components/shared/ModelCombobox";
import { Plus, MoreHorizontal, Pencil, Trash2, Loader2 } from "lucide-react";
import { toast } from "sonner";

const PER_PAGE = 20;

export function RoutesPage() {
  const [page, setPage] = useState(1);
  const { data: routesData, isLoading } = useRoutingRoutes(page, PER_PAGE);
  const { data: groupsData } = useUpstreamGroups(1, 100);
  const { data: projectsData } = useAllProjects(1, 100);
  const createRoute = useCreateRoutingRoute();
  const updateRoute = useUpdateRoutingRoute();
  const deleteRoute = useDeleteRoutingRoute();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<RoutingRoute | null>(null);
  // Canonical names picked from the catalog. Aliases submitted by clients
  // resolve to the canonical name at ingress; only canonical names are
  // stored here.
  const [form, setForm] = useState({
    model_names: [] as string[],
    request_kinds: ["anthropic_messages"] as string[],
    upstream_group_id: "",
    match_priority: 0,
    status: "active",
    project_id: "",
  });

  const { data: requestKindsData } = useRequestKinds();
  const requestKinds = requestKindsData?.data ?? [];

  const routes = routesData?.data ?? [];
  const meta = routesData?.meta;
  const groups = groupsData?.data ?? [];
  const projects = projectsData?.data ?? [];

  const groupMap = useMemo(() => {
    const m = new Map<string, UpstreamGroupWithMembers>();
    for (const g of groups) m.set(g.id, g);
    return m;
  }, [groups]);

  const projectMap = useMemo(() => {
    const m = new Map<string, Project>();
    for (const p of projects) m.set(p.id, p);
    return m;
  }, [projects]);

  // Mirror of the SQL inference table in migration 021. Returns the kinds
  // the chosen upstream group's members typically serve; an empty array
  // means "cross-family / unrecognised — no inference possible".
  const kindMismatch = useMemo(() => {
    if (!form.upstream_group_id || form.request_kinds.length === 0) return null;
    const group = groupMap.get(form.upstream_group_id);
    if (!group) return null;
    const providers = new Set<string>();
    for (const m of group.members ?? []) {
      if (m.upstream?.provider) providers.add(m.upstream.provider);
    }
    if (providers.size === 0) return null;
    const subsetOf = (allowed: string[]) =>
      [...providers].every((p) => allowed.includes(p));
    let inferred: string[] = [];
    if (subsetOf(["anthropic", "claudecode"])) {
      inferred = ["anthropic_messages", "anthropic_count_tokens"];
    } else if (subsetOf(["anthropic", "claudecode", "bedrock", "vertex-anthropic"])) {
      inferred = ["anthropic_messages"];
    } else if (providers.size === 1 && providers.has("openai")) {
      inferred = ["openai_responses"];
    } else if (providers.size === 1 && providers.has("vertex-openai")) {
      inferred = ["openai_chat_completions"];
    } else if (subsetOf(["gemini", "vertex-google"])) {
      inferred = ["google_generate_content"];
    }
    if (inferred.length === 0) return null;
    if (form.request_kinds.some((k) => inferred.includes(k))) return null;
    return inferred;
  }, [form.upstream_group_id, form.request_kinds, groupMap]);

  function openCreate() {
    setEditingId(null);
    setForm({
      model_names: [],
      request_kinds: ["anthropic_messages"],
      upstream_group_id: "",
      match_priority: 0,
      status: "active",
      project_id: "",
    });
    setDialogOpen(true);
  }

  function openEdit(route: RoutingRoute) {
    setEditingId(route.id);
    setForm({
      model_names: [...(route.model_names ?? [])],
      request_kinds: [...(route.request_kinds ?? [])],
      upstream_group_id: route.upstream_group_id,
      match_priority: route.match_priority,
      status: route.status,
      project_id: route.project_id ?? "",
    });
    setDialogOpen(true);
  }

  async function handleSave() {
    if (form.model_names.length === 0) {
      toast.error("At least one model name is required");
      return;
    }
    if (form.request_kinds.length === 0) {
      toast.error("At least one request kind is required");
      return;
    }
    const payload = {
      model_names: form.model_names,
      request_kinds: form.request_kinds,
      upstream_group_id: form.upstream_group_id,
      match_priority: form.match_priority,
      status: form.status,
      project_id: form.project_id,
    };
    try {
      if (editingId) {
        await updateRoute.mutateAsync({ id: editingId, ...payload });
        toast.success("Route updated");
      } else {
        await createRoute.mutateAsync(payload);
        toast.success("Route created");
      }
      setDialogOpen(false);
    } catch {
      toast.error(editingId ? "Failed to update route" : "Failed to create route");
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

  const isSaving = createRoute.isPending || updateRoute.isPending;

  const columns: Column<RoutingRoute>[] = [
    {
      header: "ID",
      accessor: (r) => (
        <code className="text-xs text-muted-foreground">{r.id.slice(0, 8)}</code>
      ),
      className: "w-24",
    },
    {
      header: "Model Names",
      accessor: (r) => (
        <code className="text-sm">{(r.model_names ?? []).join(", ")}</code>
      ),
    },
    {
      header: "Endpoints",
      accessor: (r) => (
        <div className="flex flex-wrap gap-1">
          {(r.request_kinds ?? []).map((k) => (
            <Badge key={k} variant="outline" className="text-xs font-mono">
              {k}
            </Badge>
          ))}
        </div>
      ),
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
      accessor: (r) => {
        if (!r.project_id) {
          return <Badge variant="secondary" className="text-xs">Global</Badge>;
        }
        const proj = projectMap.get(r.project_id);
        return (
          <Badge variant="outline" className="text-xs">
            {proj?.name ?? r.project_id.slice(0, 8)}
          </Badge>
        );
      },
      className: "w-28",
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
            <DropdownMenuItem onClick={() => openEdit(r)}>
              <Pencil className="mr-2 h-4 w-4" />
              Edit
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
        title="Routes"
        description="Route requests to upstream groups by canonical model name (superadmin only)"
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
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{editingId ? "Edit Route" : "Create Route"}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label>Project (optional)</Label>
              <Select
                value={form.project_id}
                onValueChange={(v) => setForm((p) => ({ ...p, project_id: v === "__global__" ? "" : (v ?? "") }))}
              >
                <SelectTrigger>
                  <SelectValue placeholder="Global (all projects)" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__global__">Global (all projects)</SelectItem>
                  {projects.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                Leave as Global to apply to all projects, or select a specific project
              </p>
            </div>
            <div className="space-y-2">
              <Label>Model Names</Label>
              <ModelMultiSelect
                value={form.model_names}
                onChange={(next) => setForm((p) => ({ ...p, model_names: next }))}
                placeholder="Pick one or more canonical models..."
              />
              <p className="text-xs text-muted-foreground">
                Canonical names from the Models catalog. Aliases resolve to
                canonical at ingress; only canonical names match here.
              </p>
            </div>
            <div className="space-y-2">
              <Label>Request Kinds</Label>
              <div className="flex flex-wrap gap-2">
                {requestKinds.map((kind) => {
                  const selected = form.request_kinds.includes(kind);
                  return (
                    <Button
                      key={kind}
                      type="button"
                      size="sm"
                      variant={selected ? "default" : "outline"}
                      onClick={() =>
                        setForm((p) => ({
                          ...p,
                          request_kinds: selected
                            ? p.request_kinds.filter((k) => k !== kind)
                            : [...p.request_kinds, kind],
                        }))
                      }
                    >
                      {kind}
                    </Button>
                  );
                })}
              </div>
              <p className="text-xs text-muted-foreground">
                Wire-level endpoints this route serves (e.g. anthropic_messages =
                /v1/messages, anthropic_count_tokens = /v1/messages/count_tokens).
                Pick at least one.
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
              {kindMismatch && (
                <p className="text-xs text-yellow-600 dark:text-yellow-400">
                  Heads up: the selected group's providers typically serve
                  {" "}<code>{kindMismatch.join(", ")}</code>, not the kinds
                  you've picked. The route will still save — verify this is
                  what you want.
                </p>
              )}
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleSave}
              disabled={form.model_names.length === 0 || form.request_kinds.length === 0 || !form.upstream_group_id || isSaving}
            >
              {isSaving ? "Saving..." : editingId ? "Save" : "Create"}
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
              Delete the route for models "{(deleteTarget?.model_names ?? []).join(", ")}"?
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
