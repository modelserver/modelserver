import { useMemo, useState } from "react";
import {
  useModels,
  useCreateModel,
  useUpdateModel,
  useDeleteModel,
} from "@/api/models";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Textarea } from "@/components/ui/textarea";
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
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import type {
  Model,
  ModelListRow,
  ModelReferenceCounts,
  CreditRate,
  ImageCreditRate,
} from "@/api/types";
import {
  Plus,
  MoreHorizontal,
  Pencil,
  Trash2,
  Loader2,
  X,
  Info,
} from "lucide-react";
import { toast } from "sonner";

// ModelNamePattern mirrors the server's validateModelName check. Keeping it
// client-side too turns mistakes into instant feedback instead of a round
// trip that returns 400.
const MODEL_NAME_PATTERN = /^[a-z0-9._-]+$/;

interface FormState {
  name: string;
  display_name: string;
  description: string;
  aliases: string[];
  aliasDraft: string;
  rate_input: string;
  rate_output: string;
  rate_cache_creation: string;
  rate_cache_read: string;
  has_default_rate: boolean;
  image_text_input: string;
  image_text_cached_input: string;
  image_text_output: string;
  image_image_input: string;
  image_image_cached_input: string;
  image_image_output: string;
  has_default_image_rate: boolean;
  status: "active" | "disabled";
  metadata_json: string;
}

const emptyForm: FormState = {
  name: "",
  display_name: "",
  description: "",
  aliases: [],
  aliasDraft: "",
  rate_input: "0",
  rate_output: "0",
  rate_cache_creation: "0",
  rate_cache_read: "0",
  has_default_rate: false,
  image_text_input: "0",
  image_text_cached_input: "0",
  image_text_output: "0",
  image_image_input: "0",
  image_image_cached_input: "0",
  image_image_output: "0",
  has_default_image_rate: false,
  status: "active",
  metadata_json: "{}",
};

function fromModel(m: Model): FormState {
  const rate = m.default_credit_rate;
  const imageRate = m.default_image_credit_rate;
  return {
    name: m.name,
    display_name: m.display_name,
    description: m.description ?? "",
    aliases: [...(m.aliases ?? [])],
    aliasDraft: "",
    rate_input: rate ? String(rate.input_rate) : "0",
    rate_output: rate ? String(rate.output_rate) : "0",
    rate_cache_creation: rate ? String(rate.cache_creation_rate) : "0",
    rate_cache_read: rate ? String(rate.cache_read_rate) : "0",
    has_default_rate: !!rate,
    image_text_input: imageRate ? String(imageRate.text_input_rate) : "0",
    image_text_cached_input: imageRate ? String(imageRate.text_cached_input_rate) : "0",
    image_text_output: imageRate ? String(imageRate.text_output_rate) : "0",
    image_image_input: imageRate ? String(imageRate.image_input_rate) : "0",
    image_image_cached_input: imageRate ? String(imageRate.image_cached_input_rate) : "0",
    image_image_output: imageRate ? String(imageRate.image_output_rate) : "0",
    has_default_image_rate: !!imageRate,
    status: (m.status === "disabled" ? "disabled" : "active"),
    metadata_json: JSON.stringify(m.metadata ?? {}, null, 2),
  };
}

export function ModelsPage() {
  const { data, isLoading } = useModels();
  const createModel = useCreateModel();
  const updateModel = useUpdateModel();
  const deleteModel = useDeleteModel();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingName, setEditingName] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ModelListRow | null>(null);
  const [form, setForm] = useState<FormState>(emptyForm);

  const rows = data?.data ?? [];

  function openCreate() {
    setEditingName(null);
    setForm({ ...emptyForm });
    setDialogOpen(true);
  }

  function openEdit(row: ModelListRow) {
    setEditingName(row.name);
    setForm(fromModel(row));
    setDialogOpen(true);
  }

  function addAlias() {
    const a = form.aliasDraft.trim();
    if (!a) return;
    if (!MODEL_NAME_PATTERN.test(a)) {
      toast.error(`Alias ${a} must be lowercase [a-z0-9._-]`);
      return;
    }
    if (a === form.name) {
      toast.error("Alias cannot equal the canonical name");
      return;
    }
    if (form.aliases.includes(a)) {
      toast.error("Alias already added");
      return;
    }
    setForm((p) => ({ ...p, aliases: [...p.aliases, a], aliasDraft: "" }));
  }

  function removeAlias(a: string) {
    setForm((p) => ({ ...p, aliases: p.aliases.filter((x) => x !== a) }));
  }

  async function handleSave() {
    // Shared validation for both create and patch paths.
    if (!editingName) {
      if (!form.name) {
        toast.error("Canonical name is required");
        return;
      }
      if (!MODEL_NAME_PATTERN.test(form.name)) {
        toast.error("Canonical name must be lowercase [a-z0-9._-]");
        return;
      }
    }
    for (const a of form.aliases) {
      if (!MODEL_NAME_PATTERN.test(a) || a === form.name) {
        toast.error(`Invalid alias: ${a}`);
        return;
      }
    }
    let metadata: Record<string, unknown> = {};
    try {
      metadata = form.metadata_json.trim() ? JSON.parse(form.metadata_json) : {};
    } catch {
      toast.error("Metadata must be valid JSON");
      return;
    }
    const defaultRate: CreditRate | undefined = form.has_default_rate
      ? {
          input_rate: Number(form.rate_input) || 0,
          output_rate: Number(form.rate_output) || 0,
          cache_creation_rate: Number(form.rate_cache_creation) || 0,
          cache_read_rate: Number(form.rate_cache_read) || 0,
        }
      : undefined;
    if (defaultRate) {
      for (const v of Object.values(defaultRate)) {
        if ((v as number) < 0) {
          toast.error("Credit rates must be non-negative");
          return;
        }
      }
    }
    const defaultImageRate: ImageCreditRate | undefined = form.has_default_image_rate
      ? {
          text_input_rate: Number(form.image_text_input) || 0,
          text_cached_input_rate: Number(form.image_text_cached_input) || 0,
          text_output_rate: Number(form.image_text_output) || 0,
          image_input_rate: Number(form.image_image_input) || 0,
          image_cached_input_rate: Number(form.image_image_cached_input) || 0,
          image_output_rate: Number(form.image_image_output) || 0,
        }
      : undefined;
    if (defaultImageRate) {
      for (const v of Object.values(defaultImageRate)) {
        if ((v as number) < 0) {
          toast.error("Image credit rates must be non-negative");
          return;
        }
      }
    }

    try {
      if (editingName) {
        // Explicit null clears the column when the toggle is off. Sending
        // undefined would drop the key from the JSON body and leave the
        // existing rate intact, so the user could never unset a rate
        // they had previously set.
        await updateModel.mutateAsync({
          name: editingName,
          display_name: form.display_name || editingName,
          description: form.description,
          aliases: form.aliases,
          default_credit_rate: form.has_default_rate ? defaultRate : null,
          default_image_credit_rate: form.has_default_image_rate ? defaultImageRate : null,
          status: form.status,
          metadata,
        });
        toast.success("Model updated");
      } else {
        await createModel.mutateAsync({
          name: form.name,
          display_name: form.display_name || form.name,
          description: form.description,
          aliases: form.aliases,
          default_credit_rate: defaultRate,
          default_image_credit_rate: defaultImageRate,
          status: form.status,
          metadata,
        });
        toast.success("Model created");
      }
      setDialogOpen(false);
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to save model";
      toast.error(msg);
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    try {
      await deleteModel.mutateAsync(deleteTarget.name);
      toast.success("Model deleted");
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : "Failed to delete model";
      toast.error(msg);
    }
    setDeleteTarget(null);
  }

  const isSaving = createModel.isPending || updateModel.isPending;

  const columns = useMemo<Column<ModelListRow>[]>(
    () => [
      {
        header: "Name",
        accessor: (m) => (
          <div className="flex flex-col">
            <code className="text-sm">{m.name}</code>
            {m.aliases && m.aliases.length > 0 && (
              <span className="text-xs text-muted-foreground">
                aliases: {m.aliases.join(", ")}
              </span>
            )}
          </div>
        ),
      },
      {
        header: "Display Name",
        accessor: (m) => m.display_name,
      },
      {
        header: "Default Rate",
        accessor: (m) => {
          const r = m.default_credit_rate;
          const ir = m.default_image_credit_rate;
          if (!r && !ir) return <span className="text-muted-foreground">—</span>;
          return (
            <div className="flex flex-col gap-1">
              {r && (
                <code className="text-xs">
                  text in={r.input_rate} out={r.output_rate}
                </code>
              )}
              {ir && (
                <code className="text-xs">
                  image in={ir.image_input_rate} out={ir.image_output_rate}
                </code>
              )}
            </div>
          );
        },
        className: "w-48",
      },
      {
        header: "References",
        accessor: (m) => {
          const c = m.reference_counts;
          const total = refTotal(c);
          if (total === 0) {
            return <span className="text-muted-foreground">0</span>;
          }
          return (
            <Tooltip>
              <TooltipTrigger
                render={
                  <button
                    type="button"
                    className="flex items-center gap-1 text-sm underline-offset-2 hover:underline"
                  />
                }
              >
                <span>{total}</span>
                <Info className="h-3 w-3 text-muted-foreground" />
              </TooltipTrigger>
              <TooltipContent>
                <div className="text-xs">
                  <div>upstreams: {c.upstreams}</div>
                  <div>routes: {c.routes}</div>
                  <div>plans: {c.plans}</div>
                  <div>policies: {c.policies}</div>
                  <div>api_keys: {c.api_keys}</div>
                </div>
              </TooltipContent>
            </Tooltip>
          );
        },
        className: "w-28",
      },
      {
        header: "Status",
        accessor: (m) => (
          <Badge variant={m.status === "active" ? "default" : "secondary"}>
            {m.status === "active" ? "Active" : "Disabled"}
          </Badge>
        ),
        className: "w-24",
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
              <DropdownMenuItem onClick={() => openEdit(m)}>
                <Pencil className="mr-2 h-4 w-4" />
                Edit
              </DropdownMenuItem>
              <DropdownMenuItem
                className="text-destructive-foreground"
                disabled={refTotal(m.reference_counts) > 0}
                onClick={() => setDeleteTarget(m)}
              >
                <Trash2 className="mr-2 h-4 w-4" />
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        ),
        className: "w-12",
      },
    ],
    [],
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title="Models"
        description="Global model catalog — canonical names and aliases referenced by routes, upstreams, plans, and policies."
        actions={
          <Button onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            Add Model
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
              data={rows}
              keyFn={(m) => m.name}
              emptyMessage="No models in the catalog yet — add one to start routing."
            />
          )}
        </CardContent>
      </Card>

      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>{editingName ? "Edit Model" : "Add Model"}</DialogTitle>
          </DialogHeader>
          <div className="grid grid-cols-2 gap-4 py-4">
            <div className="space-y-2">
              <Label>Canonical Name</Label>
              <Input
                value={form.name}
                disabled={!!editingName}
                onChange={(e) =>
                  setForm((p) => ({ ...p, name: e.target.value }))
                }
                placeholder="claude-opus-4-7"
              />
              <p className="text-xs text-muted-foreground">
                Lowercase [a-z0-9._-]. Immutable after creation.
              </p>
            </div>
            <div className="space-y-2">
              <Label>Display Name</Label>
              <Input
                value={form.display_name}
                onChange={(e) =>
                  setForm((p) => ({ ...p, display_name: e.target.value }))
                }
                placeholder="Claude Opus 4.7"
              />
            </div>
            <div className="col-span-2 space-y-2">
              <Label>Description</Label>
              <Textarea
                rows={2}
                value={form.description}
                onChange={(e) =>
                  setForm((p) => ({ ...p, description: e.target.value }))
                }
                placeholder="Optional notes shown in the admin UI"
              />
            </div>
            <div className="col-span-2 space-y-2">
              <Label>Aliases</Label>
              <div className="flex flex-wrap gap-1">
                {form.aliases.map((a) => (
                  <Badge key={a} variant="secondary" className="gap-1">
                    <code className="text-xs">{a}</code>
                    <button
                      type="button"
                      className="hover:text-destructive-foreground"
                      onClick={() => removeAlias(a)}
                      aria-label={`Remove alias ${a}`}
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </Badge>
                ))}
              </div>
              <div className="flex gap-2">
                <Input
                  value={form.aliasDraft}
                  onChange={(e) =>
                    setForm((p) => ({ ...p, aliasDraft: e.target.value }))
                  }
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      addAlias();
                    }
                  }}
                  placeholder="claude-opus-latest"
                  className="flex-1"
                />
                <Button type="button" variant="outline" onClick={addAlias}>
                  Add
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                Client-supplied aliases are resolved to the canonical name at ingress.
              </p>
            </div>
            <div className="col-span-2 space-y-2">
              <div className="flex items-center gap-2">
                <input
                  type="checkbox"
                  id="has-default-rate"
                  checked={form.has_default_rate}
                  onChange={(e) =>
                    setForm((p) => ({ ...p, has_default_rate: e.target.checked }))
                  }
                />
                <Label htmlFor="has-default-rate">
                  Set catalog default credit rate
                </Label>
              </div>
              <p className="text-xs text-muted-foreground">
                Fallback used for billing when a plan has no per-model override.
                Applied before the plan's <code>_default</code> safety net.
              </p>
              {form.has_default_rate && (
                <div className="grid grid-cols-4 gap-2 pt-2">
                  <div className="space-y-1">
                    <Label className="text-xs">Input</Label>
                    <Input
                      type="number"
                      step="0.001"
                      value={form.rate_input}
                      onChange={(e) =>
                        setForm((p) => ({ ...p, rate_input: e.target.value }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Output</Label>
                    <Input
                      type="number"
                      step="0.001"
                      value={form.rate_output}
                      onChange={(e) =>
                        setForm((p) => ({ ...p, rate_output: e.target.value }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Cache Creation</Label>
                    <Input
                      type="number"
                      step="0.001"
                      value={form.rate_cache_creation}
                      onChange={(e) =>
                        setForm((p) => ({
                          ...p,
                          rate_cache_creation: e.target.value,
                        }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Cache Read</Label>
                    <Input
                      type="number"
                      step="0.001"
                      value={form.rate_cache_read}
                      onChange={(e) =>
                        setForm((p) => ({
                          ...p,
                          rate_cache_read: e.target.value,
                        }))
                      }
                    />
                  </div>
                </div>
              )}
            </div>
            <div className="col-span-2 space-y-2">
              <div className="flex items-center gap-2">
                <input
                  type="checkbox"
                  id="has-default-image-rate"
                  checked={form.has_default_image_rate}
                  onChange={(e) =>
                    setForm((p) => ({
                      ...p,
                      has_default_image_rate: e.target.checked,
                    }))
                  }
                />
                <Label htmlFor="has-default-image-rate">
                  Set image billing rate
                </Label>
              </div>
              {form.has_default_image_rate && (
                <div className="grid grid-cols-3 gap-2 pt-2">
                  <div className="space-y-1">
                    <Label className="text-xs">Text Input</Label>
                    <Input
                      type="number"
                      step="0.001"
                      value={form.image_text_input}
                      onChange={(e) =>
                        setForm((p) => ({
                          ...p,
                          image_text_input: e.target.value,
                        }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Text Cached Input</Label>
                    <Input
                      type="number"
                      step="0.001"
                      value={form.image_text_cached_input}
                      onChange={(e) =>
                        setForm((p) => ({
                          ...p,
                          image_text_cached_input: e.target.value,
                        }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Text Output</Label>
                    <Input
                      type="number"
                      step="0.001"
                      value={form.image_text_output}
                      onChange={(e) =>
                        setForm((p) => ({
                          ...p,
                          image_text_output: e.target.value,
                        }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Image Input</Label>
                    <Input
                      type="number"
                      step="0.001"
                      value={form.image_image_input}
                      onChange={(e) =>
                        setForm((p) => ({
                          ...p,
                          image_image_input: e.target.value,
                        }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Image Cached Input</Label>
                    <Input
                      type="number"
                      step="0.001"
                      value={form.image_image_cached_input}
                      onChange={(e) =>
                        setForm((p) => ({
                          ...p,
                          image_image_cached_input: e.target.value,
                        }))
                      }
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Image Output</Label>
                    <Input
                      type="number"
                      step="0.001"
                      value={form.image_image_output}
                      onChange={(e) =>
                        setForm((p) => ({
                          ...p,
                          image_image_output: e.target.value,
                        }))
                      }
                    />
                  </div>
                </div>
              )}
            </div>
            <div className="space-y-2">
              <Label>Status</Label>
              <Select
                value={form.status}
                onValueChange={(v) =>
                  setForm((p) => ({
                    ...p,
                    status: v === "disabled" ? "disabled" : "active",
                  }))
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="active">Active</SelectItem>
                  <SelectItem value="disabled">Disabled</SelectItem>
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                Disabled models return 400 at ingress without touching any upstream.
              </p>
            </div>
            <div className="col-span-2 space-y-2">
              <Label>Metadata (JSON)</Label>
              <Textarea
                rows={5}
                className="font-mono text-xs"
                value={form.metadata_json}
                onChange={(e) =>
                  setForm((p) => ({ ...p, metadata_json: e.target.value }))
                }
                placeholder='{"context_window": 200000, "capabilities": ["vision", "tools"]}'
              />
              <p className="text-xs text-muted-foreground">
                Free-form JSON. Known keys: context_window, capabilities,
                provider_hint, icon, category, replaced_by.
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button
              onClick={handleSave}
              disabled={isSaving || (!editingName && !form.name.trim())}
            >
              {isSaving ? "Saving..." : editingName ? "Save" : "Create"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={!!deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Model</DialogTitle>
            <DialogDescription>
              Delete catalog entry <code>{deleteTarget?.name}</code>?
              {deleteTarget && refTotal(deleteTarget.reference_counts) > 0 && (
                <>
                  {" "}This model is referenced by{" "}
                  {renderRefSummary(deleteTarget.reference_counts)} — the server
                  will reject the delete. Set status to disabled instead, or
                  clear the references first.
                </>
              )}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleDelete}
              disabled={
                !!deleteTarget && refTotal(deleteTarget.reference_counts) > 0
              }
            >
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function refTotal(c: ModelReferenceCounts): number {
  return c.upstreams + c.routes + c.plans + c.policies + c.api_keys;
}

function renderRefSummary(c: ModelReferenceCounts): string {
  const parts: string[] = [];
  if (c.upstreams) parts.push(`${c.upstreams} upstream(s)`);
  if (c.routes) parts.push(`${c.routes} route(s)`);
  if (c.plans) parts.push(`${c.plans} plan(s)`);
  if (c.policies) parts.push(`${c.policies} policy/policies`);
  if (c.api_keys) parts.push(`${c.api_keys} api_key(s)`);
  return parts.join(", ");
}
