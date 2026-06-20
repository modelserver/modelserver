import { useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";
import {
  useTenants, useCreateTenant, useDeleteTenant, useRotateSecret, useUpdateTenant,
} from "@/api/tenants";
import { SecretRevealOnce } from "@/components/SecretRevealOnce";

export function TenantsPage() {
  const { data, isLoading, error } = useTenants();
  const create = useCreateTenant();
  const del = useDeleteTenant();
  const rotate = useRotateSecret();
  const update = useUpdateTenant();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [form, setForm] = useState({ name: "", callback_url: "", callback_secret: "", description: "" });
  const [secretReveal, setSecretReveal] = useState<string | null>(null);

  if (isLoading) return <div className="text-sm text-muted-foreground">Loading…</div>;
  if (error) return <div className="text-sm text-destructive">Error: {String(error)}</div>;

  const items = data?.items ?? [];

  async function submitCreate() {
    if (!form.name.trim()) {
      toast.error("Name is required");
      return;
    }
    try {
      const res = await create.mutateAsync(form);
      setDialogOpen(false);
      setForm({ name: "", callback_url: "", callback_secret: "", description: "" });
      setSecretReveal(res.secret);
    } catch (e: unknown) {
      toast.error(String((e as Error).message ?? e));
    }
  }

  async function handleRotate(id: string) {
    if (!confirm("Rotate this tenant's secret? The old secret will fail immediately.")) return;
    try {
      const res = await rotate.mutateAsync(id);
      setSecretReveal(res.secret);
    } catch (e: unknown) {
      toast.error(String((e as Error).message ?? e));
    }
  }

  async function handleDelete(id: string) {
    if (!confirm("Delete this tenant? Cannot be undone.")) return;
    try {
      await del.mutateAsync(id);
      toast.success("Tenant deleted");
    } catch (e: unknown) {
      const msg = String((e as Error).message ?? e);
      toast.error(msg);
      if (msg.includes("payments")) {
        if (confirm("Tenant has payments; deactivate instead?")) {
          try {
            await update.mutateAsync({ id, patch: { is_active: false } });
            toast.success("Tenant deactivated");
          } catch (e2: unknown) {
            toast.error(String((e2 as Error).message ?? e2));
          }
        }
      }
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Tenants</h1>
        <button
          onClick={() => setDialogOpen(true)}
          className="rounded bg-primary px-3 py-1 text-sm text-primary-foreground"
        >
          + New Tenant
        </button>
      </div>

      <table className="w-full text-sm">
        <thead className="border-b">
          <tr>
            <th className="px-2 py-2 text-left">Name</th>
            <th className="px-2 py-2 text-left">Callback URL</th>
            <th className="px-2 py-2 text-left">Status</th>
            <th className="px-2 py-2 text-left">Created</th>
            <th className="px-2 py-2 text-right">Actions</th>
          </tr>
        </thead>
        <tbody>
          {items.map((t) => (
            <tr key={t.id} className="border-b hover:bg-accent/30">
              <td className="px-2 py-2">
                <Link to={`/tenants/${t.id}`} className="font-medium underline-offset-2 hover:underline">
                  {t.name}
                </Link>
              </td>
              <td className="px-2 py-2 truncate max-w-md text-muted-foreground">{t.callback_url || "—"}</td>
              <td className="px-2 py-2">
                <span className={`rounded px-2 py-0.5 text-xs ${t.is_active ? "bg-emerald-100 text-emerald-900" : "bg-zinc-100 text-zinc-700"}`}>
                  {t.is_active ? "active" : "inactive"}
                </span>
              </td>
              <td className="px-2 py-2 text-muted-foreground">{new Date(t.created_at).toLocaleDateString()}</td>
              <td className="px-2 py-2 text-right space-x-2">
                <button onClick={() => handleRotate(t.id)} className="text-xs underline">Rotate</button>
                <button onClick={() => handleDelete(t.id)} className="text-xs text-destructive underline">Delete</button>
              </td>
            </tr>
          ))}
          {items.length === 0 && (
            <tr><td colSpan={5} className="px-2 py-6 text-center text-muted-foreground">No tenants yet</td></tr>
          )}
        </tbody>
      </table>

      {dialogOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="w-[480px] space-y-4 rounded-md bg-background p-6 shadow-lg">
            <h2 className="text-lg font-semibold">New Tenant</h2>
            <div className="space-y-3">
              <div>
                <label className="text-sm">Name <span className="text-destructive">*</span></label>
                <input className="w-full rounded border px-2 py-1 text-sm" value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="e.g. modelserver"/>
              </div>
              <div>
                <label className="text-sm">Callback URL</label>
                <input className="w-full rounded border px-2 py-1 text-sm" value={form.callback_url}
                  onChange={(e) => setForm({ ...form, callback_url: e.target.value })}
                  placeholder="https://yourapp.example/webhook"/>
              </div>
              <div>
                <label className="text-sm">Callback HMAC Secret</label>
                <input type="password" className="w-full rounded border px-2 py-1 text-sm font-mono" value={form.callback_secret}
                  onChange={(e) => setForm({ ...form, callback_secret: e.target.value })}
                  placeholder="shared with the upstream's verifier"/>
              </div>
              <div>
                <label className="text-sm">Description</label>
                <input className="w-full rounded border px-2 py-1 text-sm" value={form.description}
                  onChange={(e) => setForm({ ...form, description: e.target.value })}/>
              </div>
              <p className="text-xs text-muted-foreground">
                Use UUIDs as your order IDs. <code>payments.order_id</code> is globally unique;
                reusing across tenants returns 409.
              </p>
            </div>
            <div className="flex justify-end gap-2">
              <button onClick={() => setDialogOpen(false)} className="rounded border px-3 py-1 text-sm">Cancel</button>
              <button onClick={submitCreate} disabled={create.isPending}
                className="rounded bg-primary px-3 py-1 text-sm text-primary-foreground disabled:opacity-50">
                {create.isPending ? "Creating…" : "Create"}
              </button>
            </div>
          </div>
        </div>
      )}

      {secretReveal && (
        <SecretRevealOnce secret={secretReveal} onAcknowledge={() => setSecretReveal(null)}/>
      )}
    </div>
  );
}
