import { useEffect, useState } from "react";
import { Link, useParams } from "react-router";
import { toast } from "sonner";
import { useTenant, useUpdateTenant } from "@/api/tenants";

export function TenantDetailPage() {
  const { id = "" } = useParams();
  const { data: t, isLoading, error } = useTenant(id);
  const update = useUpdateTenant();

  const [form, setForm] = useState({
    callback_url: "", callback_secret: "", description: "", is_active: true,
  });
  useEffect(() => {
    if (t) setForm({
      callback_url: t.callback_url, callback_secret: "",
      description: t.description, is_active: t.is_active,
    });
  }, [t?.id]);

  if (isLoading) return <div className="text-sm text-muted-foreground">Loading…</div>;
  if (error) return <div className="text-sm text-destructive">Error: {String(error)}</div>;
  if (!t) return null;

  async function save() {
    const patch: Record<string, unknown> = {
      callback_url: form.callback_url, description: form.description, is_active: form.is_active,
    };
    if (form.callback_secret) patch.callback_secret = form.callback_secret;
    try {
      await update.mutateAsync({ id, patch });
      toast.success("Saved");
    } catch (e: unknown) { toast.error(String((e as Error).message ?? e)); }
  }

  return (
    <div className="space-y-6 max-w-2xl">
      <div>
        <Link to="/tenants" className="text-sm text-muted-foreground hover:underline">← Tenants</Link>
        <h1 className="text-xl font-semibold mt-2">{t.name}</h1>
        <dl className="mt-3 grid grid-cols-2 gap-x-6 gap-y-1 text-sm text-muted-foreground">
          <dt>ID</dt><dd className="font-mono text-xs">{t.id}</dd>
          <dt>Created</dt><dd>{new Date(t.created_at).toLocaleString()}</dd>
        </dl>
      </div>

      <div className="space-y-3">
        <div>
          <label className="text-sm">Callback URL</label>
          <input className="w-full rounded border px-2 py-1 text-sm" value={form.callback_url}
            onChange={(e) => setForm({ ...form, callback_url: e.target.value })}/>
        </div>
        <div>
          <label className="text-sm">Callback HMAC Secret (leave empty to keep)</label>
          <input className="w-full rounded border px-2 py-1 text-sm font-mono" type="password" value={form.callback_secret}
            onChange={(e) => setForm({ ...form, callback_secret: e.target.value })}/>
        </div>
        <div>
          <label className="text-sm">Description</label>
          <input className="w-full rounded border px-2 py-1 text-sm" value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}/>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={form.is_active}
            onChange={(e) => setForm({ ...form, is_active: e.target.checked })}/>
          Active
        </label>
        <button onClick={save} disabled={update.isPending}
          className="rounded bg-primary px-3 py-1 text-sm text-primary-foreground disabled:opacity-50">
          {update.isPending ? "Saving…" : "Save"}
        </button>
      </div>

      <Link to={`/payments?tenant_id=${t.id}`} className="text-sm underline">
        View this tenant's payments →
      </Link>
    </div>
  );
}
