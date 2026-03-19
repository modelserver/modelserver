import { useState } from "react";
import { useUpstreams, useCreateUpstream, useUpdateUpstream, useDeleteUpstream, useTestUpstream } from "@/api/upstreams";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
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
import type { Upstream } from "@/api/types";
import { Plus, MoreHorizontal, Pencil, Trash2, Loader2, Zap } from "lucide-react";
import { toast } from "sonner";

export function UpstreamsPage() {
  const { data, isLoading } = useUpstreams();
  const createUpstream = useCreateUpstream();
  const updateUpstream = useUpdateUpstream();
  const deleteUpstream = useDeleteUpstream();
  const testUpstream = useTestUpstream();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<Upstream | null>(null);
  const [form, setForm] = useState({
    provider: "anthropic",
    name: "",
    base_url: "",
    api_key: "",
    supported_models: "",
    weight: "1",
    max_concurrent: "10",
    test_model: "",
    status: "active",
  });

  const upstreams = data?.data ?? [];

  function openCreate() {
    setEditingId(null);
    setForm({
      provider: "anthropic",
      name: "",
      base_url: "",
      api_key: "",
      supported_models: "",
      weight: "1",
      max_concurrent: "10",
      test_model: "",
      status: "active",
    });
    setDialogOpen(true);
  }

  function openEdit(u: Upstream) {
    setEditingId(u.id);
    setForm({
      provider: u.provider,
      name: u.name,
      base_url: u.base_url,
      api_key: "",
      supported_models: u.supported_models?.join(", ") ?? "",
      weight: String(u.weight),
      max_concurrent: String(u.max_concurrent),
      test_model: u.test_model ?? "",
      status: u.status,
    });
    setDialogOpen(true);
  }

  async function handleSave() {
    try {
      const models = form.supported_models
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      if (editingId) {
        const body: Record<string, unknown> = {
          id: editingId,
          name: form.name,
          provider: form.provider,
          base_url: form.base_url,
          supported_models: models,
          weight: Number(form.weight) || 1,
          max_concurrent: Number(form.max_concurrent) || 10,
          test_model: form.test_model || undefined,
          status: form.status,
        };
        if (form.api_key) body.api_key = form.api_key;
        await updateUpstream.mutateAsync(body as Parameters<typeof updateUpstream.mutateAsync>[0]);
        toast.success("Upstream updated");
      } else {
        await createUpstream.mutateAsync({
          provider: form.provider as Upstream["provider"],
          name: form.name,
          base_url: form.base_url,
          api_key: form.api_key,
          supported_models: models,
          weight: Number(form.weight) || 1,
          max_concurrent: Number(form.max_concurrent) || 10,
          test_model: form.test_model || undefined,
          status: form.status as Upstream["status"],
        });
        toast.success("Upstream created");
      }
      setDialogOpen(false);
    } catch {
      toast.error("Failed to save upstream");
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    try {
      await deleteUpstream.mutateAsync(deleteTarget.id);
      toast.success("Upstream deleted");
    } catch {
      toast.error("Failed to delete upstream");
    }
    setDeleteTarget(null);
  }

  async function handleTest(upstreamId: string, upstreamName: string) {
    try {
      const res = await testUpstream.mutateAsync(upstreamId);
      const r = res.data;
      if (r.success) {
        toast.success(`${upstreamName}: OK (${r.latency_ms}ms, model: ${r.model})`);
      } else {
        toast.error(`${upstreamName}: ${r.error ?? "test failed"}${r.latency_ms ? ` (${r.latency_ms}ms)` : ""}`);
      }
    } catch {
      toast.error(`${upstreamName}: connection test failed`);
    }
  }

  const isSaving = createUpstream.isPending || updateUpstream.isPending;

  const columns: Column<Upstream>[] = [
    {
      header: "ID",
      accessor: (u) => (
        <code className="text-xs text-muted-foreground">{u.id.slice(0, 8)}</code>
      ),
      className: "w-24",
    },
    { header: "Name", accessor: "name" },
    { header: "Provider", accessor: "provider" },
    {
      header: "Status",
      accessor: (u) => <StatusBadge status={u.status} />,
    },
    {
      header: "Models",
      accessor: (u) => u.supported_models?.join(", ") || "\u2014",
    },
    {
      header: "Weight",
      accessor: (u) => String(u.weight),
      className: "text-right",
    },
    {
      header: "Max Concurrent",
      accessor: (u) => String(u.max_concurrent),
      className: "text-right",
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
            <DropdownMenuItem onClick={() => openEdit(u)}>
              <Pencil className="mr-2 h-4 w-4" />
              Edit
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => handleTest(u.id, u.name)}
              disabled={testUpstream.isPending}
            >
              <Zap className="mr-2 h-4 w-4" />
              Test Connection
            </DropdownMenuItem>
            {u.status === "active" ? (
              <DropdownMenuItem
                onClick={() =>
                  updateUpstream.mutate({ id: u.id, status: "disabled" })
                }
              >
                Disable
              </DropdownMenuItem>
            ) : (
              <DropdownMenuItem
                onClick={() =>
                  updateUpstream.mutate({ id: u.id, status: "active" })
                }
              >
                Enable
              </DropdownMenuItem>
            )}
            <DropdownMenuItem
              className="text-destructive-foreground"
              onClick={() => setDeleteTarget(u)}
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
        title="Upstreams"
        description="Manage upstream AI provider endpoints (superadmin only)"
        actions={
          <Button onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            Add Upstream
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
              data={upstreams}
              keyFn={(u) => u.id}
              emptyMessage="No upstreams configured"
            />
          )}
        </CardContent>
      </Card>

      {/* Create / Edit Dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{editingId ? "Edit Upstream" : "Add Upstream"}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label>Provider</Label>
              <Select
                value={form.provider}
                onValueChange={(v) => setForm((p) => ({ ...p, provider: v ?? "anthropic" }))}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="anthropic">Anthropic</SelectItem>
                  <SelectItem value="openai">OpenAI</SelectItem>
                  <SelectItem value="gemini">Gemini</SelectItem>
                  <SelectItem value="bedrock">AWS Bedrock</SelectItem>
                  <SelectItem value="claudecode">Claude Code</SelectItem>
                  <SelectItem value="vertex">Google Vertex AI</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>Name</Label>
              <Input
                value={form.name}
                onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))}
                placeholder="anthropic-primary"
              />
            </div>
            <div className="space-y-2">
              <Label>Base URL</Label>
              <Input
                value={form.base_url}
                onChange={(e) => setForm((p) => ({ ...p, base_url: e.target.value }))}
                placeholder={form.provider === "vertex"
                  ? "https://REGION-aiplatform.googleapis.com/v1/projects/PROJECT/locations/REGION/publishers/anthropic/models"
                  : "https://api.anthropic.com"}
              />
            </div>
            <div className="space-y-2">
              <Label>{editingId ? "API Key (leave blank to keep current)" : "API Key"}</Label>
              {form.provider === "vertex" ? (
                <Textarea
                  value={form.api_key}
                  onChange={(e) => setForm((p) => ({ ...p, api_key: e.target.value }))}
                  placeholder="Paste service account JSON key here..."
                  rows={6}
                  className="font-mono text-xs"
                />
              ) : (
                <Input
                  type="password"
                  value={form.api_key}
                  onChange={(e) => setForm((p) => ({ ...p, api_key: e.target.value }))}
                  placeholder="sk-..."
                />
              )}
            </div>
            <div className="space-y-2">
              <Label>Supported Models (comma-separated)</Label>
              <Input
                value={form.supported_models}
                onChange={(e) => setForm((p) => ({ ...p, supported_models: e.target.value }))}
                placeholder="claude-opus-4, claude-sonnet-4"
              />
            </div>
            <div className="space-y-2">
              <Label>Test Model (optional)</Label>
              <Input
                value={form.test_model}
                onChange={(e) => setForm((p) => ({ ...p, test_model: e.target.value }))}
                placeholder="claude-haiku-4-5"
              />
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
                  <SelectItem value="draining">Draining</SelectItem>
                  <SelectItem value="disabled">Disabled</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>Weight</Label>
                <Input
                  type="number"
                  value={form.weight}
                  onChange={(e) => setForm((p) => ({ ...p, weight: e.target.value }))}
                />
              </div>
              <div className="space-y-2">
                <Label>Max Concurrent</Label>
                <Input
                  type="number"
                  value={form.max_concurrent}
                  onChange={(e) => setForm((p) => ({ ...p, max_concurrent: e.target.value }))}
                />
              </div>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleSave}
              disabled={!form.name || !form.base_url || (!editingId && !form.api_key) || isSaving}
            >
              {isSaving ? "Saving..." : editingId ? "Save Changes" : "Create Upstream"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation */}
      <Dialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Upstream</DialogTitle>
            <DialogDescription>
              Delete upstream "{deleteTarget?.name}"? This cannot be undone and may break
              existing routes that reference this upstream.
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
