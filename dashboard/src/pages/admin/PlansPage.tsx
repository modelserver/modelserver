import { useState } from "react";
import { usePlans, useCreatePlan, useUpdatePlan, useDeletePlan } from "@/api/plans";
import type { Plan, CreditRule, CreditRate, ClassicRule } from "@/api/types";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Pagination } from "@/components/shared/Pagination";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
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
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { ModelSingleSelect } from "@/components/shared/ModelCombobox";
import { Plus, MoreHorizontal, Pencil, Trash2, Loader2, X } from "lucide-react";
import { toast } from "sonner";

// Default model credit rates for subscription plans.
// Rates are per-token, normalized so Haiku input = 2/15.
// Cache writes are charged at the regular input price (not 1.25x like API).
// Cache reads are entirely free on subscription plans.
const DEFAULT_MODEL_CREDIT_RATES: Record<string, CreditRate> = {
  "claude-opus-4-7": {
    input_rate: 0.667,
    output_rate: 3.333,
    cache_creation_rate: 0.667,
    cache_read_rate: 0,
  },
  "claude-opus-4-6": {
    input_rate: 0.667,
    output_rate: 3.333,
    cache_creation_rate: 0.667,
    cache_read_rate: 0,
  },
  "claude-sonnet-4-6": {
    input_rate: 0.4,
    output_rate: 2.0,
    cache_creation_rate: 0.4,
    cache_read_rate: 0,
  },
  "claude-haiku-4-5": {
    input_rate: 0.133,
    output_rate: 0.667,
    cache_creation_rate: 0.133,
    cache_read_rate: 0,
  },
  "claude-haiku-4-5-20251001": {
    input_rate: 0.133,
    output_rate: 0.667,
    cache_creation_rate: 0.133,
    cache_read_rate: 0,
  },
  _default: {
    input_rate: 0.4,
    output_rate: 2.0,
    cache_creation_rate: 0.4,
    cache_read_rate: 0,
  },
};

const EMPTY_CREDIT_RULE: CreditRule = { window: "5h", window_type: "sliding", max_credits: 0 };
const EMPTY_CLASSIC_RULE: ClassicRule = { metric: "rpm", limit: 0, per_model: false };

interface PlanFormState {
  name: string;
  slug: string;
  display_name: string;
  description: string;
  tier_level: number;
  group_tag: string;
  price_per_period: number;
  period_months: number;
  credit_rules: CreditRule[];
  model_credit_rates: { model: string; rate: CreditRate }[];
  classic_rules: ClassicRule[];
}

function emptyForm(): PlanFormState {
  return {
    name: "",
    slug: "",
    display_name: "",
    description: "",
    tier_level: 0,
    group_tag: "",
    price_per_period: 0,
    period_months: 1,
    credit_rules: [],
    model_credit_rates: [],
    classic_rules: [],
  };
}

function planToForm(p: Plan): PlanFormState {
  return {
    name: p.name,
    slug: p.slug,
    display_name: p.display_name,
    description: p.description ?? "",
    tier_level: p.tier_level,
    group_tag: p.group_tag ?? "",
    price_per_period: p.price_per_period,
    period_months: p.period_months,
    credit_rules: p.credit_rules ?? [],
    model_credit_rates: Object.entries(p.model_credit_rates ?? {}).map(
      ([model, rate]) => ({ model, rate }),
    ),
    classic_rules: p.classic_rules ?? [],
  };
}

function formToPayload(f: PlanFormState) {
  const rates: Record<string, CreditRate> = {};
  for (const { model, rate } of f.model_credit_rates) {
    if (model) rates[model] = rate;
  }
  return {
    name: f.name,
    slug: f.slug,
    display_name: f.display_name,
    description: f.description || undefined,
    tier_level: f.tier_level,
    group_tag: f.group_tag || undefined,
    price_per_period: f.price_per_period,
    period_months: f.period_months,
    credit_rules: f.credit_rules.length > 0 ? f.credit_rules : undefined,
    model_credit_rates: Object.keys(rates).length > 0 ? rates : undefined,
    classic_rules: f.classic_rules.length > 0 ? f.classic_rules : undefined,
  };
}

function formatPrice(cents: number) {
  if (cents === 0) return "Free";
  return `\u00A5${(cents / 100).toFixed(2)}`;
}

const PER_PAGE = 20;

export function PlansPage() {
  const [page, setPage] = useState(1);
  const { data, isLoading } = usePlans(page, PER_PAGE);
  const createPlan = useCreatePlan();
  const updatePlan = useUpdatePlan();
  const deletePlan = useDeletePlan();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [form, setForm] = useState<PlanFormState>(emptyForm());
  const [deleteTarget, setDeleteTarget] = useState<Plan | null>(null);

  const plans = data?.data ?? [];
  const meta = data?.meta;

  function openCreate() {
    setEditingId(null);
    setForm(emptyForm());
    setDialogOpen(true);
  }

  function openEdit(p: Plan) {
    setEditingId(p.id);
    setForm(planToForm(p));
    setDialogOpen(true);
  }

  async function handleSave() {
    const payload = formToPayload(form);
    try {
      if (editingId) {
        await updatePlan.mutateAsync({ planId: editingId, ...payload });
        toast.success("Plan updated");
      } else {
        await createPlan.mutateAsync(payload);
        toast.success("Plan created");
      }
      setDialogOpen(false);
    } catch {
      toast.error("Failed to save plan");
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    try {
      await deletePlan.mutateAsync(deleteTarget.id);
      toast.success("Plan deleted");
    } catch {
      toast.error("Failed to delete plan");
    }
    setDeleteTarget(null);
  }

  async function toggleActive(p: Plan) {
    try {
      await updatePlan.mutateAsync({ planId: p.id, is_active: !p.is_active });
      toast.success(p.is_active ? "Plan deactivated" : "Plan activated");
    } catch {
      toast.error("Failed to update plan");
    }
  }

  function loadDefaults() {
    setForm((prev) => ({
      ...prev,
      model_credit_rates: Object.entries(DEFAULT_MODEL_CREDIT_RATES).map(
        ([model, rate]) => ({ model, rate }),
      ),
    }));
  }

  function setCreditRule(idx: number, partial: Partial<CreditRule>) {
    setForm((prev) => ({
      ...prev,
      credit_rules: prev.credit_rules.map((r, i) => (i === idx ? { ...r, ...partial } : r)),
    }));
  }

  function setClassicRule(idx: number, partial: Partial<ClassicRule>) {
    setForm((prev) => ({
      ...prev,
      classic_rules: prev.classic_rules.map((r, i) => (i === idx ? { ...r, ...partial } : r)),
    }));
  }

  function setModelRate(idx: number, field: "model" | "rate", value: string | CreditRate) {
    setForm((prev) => ({
      ...prev,
      model_credit_rates: prev.model_credit_rates.map((r, i) =>
        i === idx ? { ...r, [field]: value } : r,
      ),
    }));
  }

  function setRateField(idx: number, field: keyof CreditRate, value: number) {
    setForm((prev) => ({
      ...prev,
      model_credit_rates: prev.model_credit_rates.map((r, i) =>
        i === idx ? { ...r, rate: { ...r.rate, [field]: value } } : r,
      ),
    }));
  }

  const isSaving = createPlan.isPending || updatePlan.isPending;

  const columns: Column<Plan>[] = [
    {
      header: "ID",
      accessor: (p) => (
        <code className="text-xs text-muted-foreground">{p.id.slice(0, 8)}</code>
      ),
      className: "w-24",
    },
    { header: "Name", accessor: (p) => p.display_name || p.name },
    { header: "Slug", accessor: "slug" },
    {
      header: "Tier",
      accessor: (p) => <Badge variant="outline">{p.tier_level}</Badge>,
      className: "w-16",
    },
    { header: "Price", accessor: (p) => formatPrice(p.price_per_period) },
    {
      header: "Period",
      accessor: (p) =>
        p.period_months === 1 ? "Monthly" : `${p.period_months} months`,
    },
    {
      header: "Status",
      accessor: (p) => (
        <Badge variant={p.is_active ? "default" : "secondary"}>
          {p.is_active ? "Active" : "Inactive"}
        </Badge>
      ),
    },
    {
      header: "Group",
      accessor: (p) =>
        p.group_tag ? (
          <Badge variant="outline">{p.group_tag}</Badge>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    {
      header: "",
      accessor: (p) => (
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="ghost" size="icon" className="h-8 w-8" />}>
            <MoreHorizontal className="h-4 w-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={() => openEdit(p)}>
              <Pencil className="mr-2 h-4 w-4" />
              Edit
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => toggleActive(p)}>
              {p.is_active ? "Deactivate" : "Activate"}
            </DropdownMenuItem>
            <DropdownMenuItem
              className="text-destructive-foreground"
              onClick={() => setDeleteTarget(p)}
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
        title="Plans"
        description="Manage subscription plans"
        actions={
          <Button onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            Create Plan
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
            <DataTable columns={columns} data={plans} keyFn={(p) => p.id} emptyMessage="No plans" />
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

      {/* Create / Edit Dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="sm:max-w-4xl max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{editingId ? "Edit Plan" : "Create Plan"}</DialogTitle>
          </DialogHeader>
          <Tabs defaultValue="general" className="py-4">
            <TabsList>
              <TabsTrigger value="general">General</TabsTrigger>
              <TabsTrigger value="credits">Credit Limits</TabsTrigger>
              <TabsTrigger value="classic">Classic Rules</TabsTrigger>
            </TabsList>

            <TabsContent value="general" className="space-y-6 pt-4">
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label>Name</Label>
                  <Input
                    value={form.name}
                    onChange={(e) => setForm((p) => ({ ...p, name: e.target.value }))}
                    placeholder="Pro Plan"
                  />
                </div>
                <div className="space-y-2">
                  <Label>Slug</Label>
                  <Input
                    value={form.slug}
                    onChange={(e) => setForm((p) => ({ ...p, slug: e.target.value }))}
                    placeholder="pro"
                  />
                </div>
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label>Display Name</Label>
                  <Input
                    value={form.display_name}
                    onChange={(e) => setForm((p) => ({ ...p, display_name: e.target.value }))}
                    placeholder="Pro"
                  />
                </div>
                <div className="space-y-2">
                  <Label>Group Tag</Label>
                  <Input
                    value={form.group_tag}
                    onChange={(e) => setForm((p) => ({ ...p, group_tag: e.target.value }))}
                    placeholder="(optional)"
                  />
                </div>
              </div>
              <div className="space-y-2">
                <Label>Description</Label>
                <Input
                  value={form.description}
                  onChange={(e) => setForm((p) => ({ ...p, description: e.target.value }))}
                  placeholder="Plan description"
                />
              </div>
              <div className="grid grid-cols-3 gap-4">
                <div className="space-y-2">
                  <Label>Tier Level</Label>
                  <Input
                    type="number"
                    value={form.tier_level}
                    onChange={(e) => setForm((p) => ({ ...p, tier_level: Number(e.target.value) || 0 }))}
                  />
                </div>
                <div className="space-y-2">
                  <Label>Price (cents)</Label>
                  <Input
                    type="number"
                    value={form.price_per_period}
                    onChange={(e) =>
                      setForm((p) => ({ ...p, price_per_period: Number(e.target.value) || 0 }))
                    }
                  />
                </div>
                <div className="space-y-2">
                  <Label>Period (months)</Label>
                  <Input
                    type="number"
                    min={1}
                    value={form.period_months}
                    onChange={(e) =>
                      setForm((p) => ({ ...p, period_months: Math.max(1, Number(e.target.value) || 1) }))
                    }
                  />
                </div>
              </div>
            </TabsContent>

            <TabsContent value="credits" className="space-y-6 pt-4">
              {/* Credit Rules */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <Label className="text-base">Credit Rules</Label>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      setForm((p) => ({
                        ...p,
                        credit_rules: [...p.credit_rules, { ...EMPTY_CREDIT_RULE }],
                      }))
                    }
                  >
                    <Plus className="mr-1 h-3 w-3" /> Add
                  </Button>
                </div>
                {form.credit_rules.map((rule, idx) => (
                  <div key={idx} className="flex items-end gap-2 rounded border p-3">
                    <div className="flex-1 space-y-1">
                      <Label className="text-xs">Window</Label>
                      <Input
                        value={rule.window}
                        onChange={(e) => setCreditRule(idx, { window: e.target.value })}
                        placeholder="5h"
                      />
                    </div>
                    <div className="flex-1 space-y-1">
                      <Label className="text-xs">Type</Label>
                      <Select
                        value={rule.window_type}
                        onValueChange={(v) =>
                          setCreditRule(idx, { window_type: v as "sliding" | "calendar" | "fixed" })
                        }
                      >
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="sliding">Sliding</SelectItem>
                          <SelectItem value="calendar">Calendar</SelectItem>
                          <SelectItem value="fixed">Fixed</SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="flex-1 space-y-1">
                      <Label className="text-xs">Max Credits</Label>
                      <Input
                        type="number"
                        value={rule.max_credits}
                        onChange={(e) =>
                          setCreditRule(idx, { max_credits: Number(e.target.value) || 0 })
                        }
                      />
                    </div>
                    <div className="flex-1 space-y-1">
                      <Label className="text-xs">Scope</Label>
                      <Select
                        value={rule.scope || "project"}
                        onValueChange={(v) =>
                          setCreditRule(idx, { scope: v as "project" | "key" })
                        }
                      >
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="project">Project</SelectItem>
                          <SelectItem value="key">Key</SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 shrink-0"
                      onClick={() =>
                        setForm((p) => ({
                          ...p,
                          credit_rules: p.credit_rules.filter((_, i) => i !== idx),
                        }))
                      }
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
              </div>

              {/* Model Credit Rates */}
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <Label className="text-base">Model Credit Rates</Label>
                  <div className="flex gap-2">
                    <Button variant="outline" size="sm" onClick={loadDefaults}>
                      Load Defaults
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() =>
                        setForm((p) => ({
                          ...p,
                          model_credit_rates: [
                            ...p.model_credit_rates,
                            {
                              model: "",
                              rate: {
                                input_rate: 0,
                                output_rate: 0,
                                cache_creation_rate: 0,
                                cache_read_rate: 0,
                              },
                            },
                          ],
                        }))
                      }
                    >
                      <Plus className="mr-1 h-3 w-3" /> Add
                    </Button>
                  </div>
                </div>
                {form.model_credit_rates.map((entry, idx) => (
                  <div key={idx} className="space-y-2 rounded border p-3">
                    <div className="flex items-start gap-2">
                      <div className="flex-1 space-y-1">
                        <Label className="text-xs">Model</Label>
                        {entry.model === "_default" ? (
                          <Input value="_default" disabled />
                        ) : (
                          <ModelSingleSelect
                            value={entry.model}
                            onChange={(next) => setModelRate(idx, "model", next)}
                            placeholder="Pick a catalog model"
                          />
                        )}
                      </div>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 shrink-0 mt-6"
                        onClick={() =>
                          setForm((p) => ({
                            ...p,
                            model_credit_rates: p.model_credit_rates.filter((_, i) => i !== idx),
                          }))
                        }
                      >
                        <X className="h-4 w-4" />
                      </Button>
                    </div>
                    <div className="grid grid-cols-4 gap-2">
                      <div className="space-y-1">
                        <Label className="text-xs">Input</Label>
                        <Input
                          type="number"
                          step="0.001"
                          value={entry.rate.input_rate}
                          onChange={(e) =>
                            setRateField(idx, "input_rate", Number(e.target.value) || 0)
                          }
                        />
                      </div>
                      <div className="space-y-1">
                        <Label className="text-xs">Output</Label>
                        <Input
                          type="number"
                          step="0.001"
                          value={entry.rate.output_rate}
                          onChange={(e) =>
                            setRateField(idx, "output_rate", Number(e.target.value) || 0)
                          }
                        />
                      </div>
                      <div className="space-y-1">
                        <Label className="text-xs">Cache Create</Label>
                        <Input
                          type="number"
                          step="0.001"
                          value={entry.rate.cache_creation_rate}
                          onChange={(e) =>
                            setRateField(idx, "cache_creation_rate", Number(e.target.value) || 0)
                          }
                        />
                      </div>
                      <div className="space-y-1">
                        <Label className="text-xs">Cache Read</Label>
                        <Input
                          type="number"
                          step="0.001"
                          value={entry.rate.cache_read_rate}
                          onChange={(e) =>
                            setRateField(idx, "cache_read_rate", Number(e.target.value) || 0)
                          }
                        />
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            </TabsContent>

            <TabsContent value="classic" className="space-y-6 pt-4">
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <Label className="text-base">Classic Rules</Label>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      setForm((p) => ({
                        ...p,
                        classic_rules: [...p.classic_rules, { ...EMPTY_CLASSIC_RULE }],
                      }))
                    }
                  >
                    <Plus className="mr-1 h-3 w-3" /> Add
                  </Button>
                </div>
                {form.classic_rules.map((rule, idx) => (
                  <div key={idx} className="flex items-end gap-2 rounded border p-3">
                    <div className="flex-1 space-y-1">
                      <Label className="text-xs">Metric</Label>
                      <Select
                        value={rule.metric}
                        onValueChange={(v) =>
                          setClassicRule(idx, { metric: v as ClassicRule["metric"] })
                        }
                      >
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="rpm">RPM</SelectItem>
                          <SelectItem value="rpd">RPD</SelectItem>
                          <SelectItem value="tpm">TPM</SelectItem>
                          <SelectItem value="tpd">TPD</SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="flex-1 space-y-1">
                      <Label className="text-xs">Limit</Label>
                      <Input
                        type="number"
                        value={rule.limit}
                        onChange={(e) =>
                          setClassicRule(idx, { limit: Number(e.target.value) || 0 })
                        }
                      />
                    </div>
                    <div className="flex-1 space-y-1">
                      <Label className="text-xs">Per Model</Label>
                      <Select
                        value={rule.per_model ? "yes" : "no"}
                        onValueChange={(v) => setClassicRule(idx, { per_model: v === "yes" })}
                      >
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="no">No</SelectItem>
                          <SelectItem value="yes">Yes</SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 shrink-0"
                      onClick={() =>
                        setForm((p) => ({
                          ...p,
                          classic_rules: p.classic_rules.filter((_, i) => i !== idx),
                        }))
                      }
                    >
                      <X className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
              </div>
            </TabsContent>
          </Tabs>

          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleSave} disabled={!form.name || !form.slug || isSaving}>
              {isSaving ? "Saving..." : editingId ? "Update" : "Create"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation */}
      <Dialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete Plan</DialogTitle>
            <DialogDescription>
              This will permanently delete the plan "{deleteTarget?.display_name || deleteTarget?.name}".
              Existing subscriptions will not be affected.
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
