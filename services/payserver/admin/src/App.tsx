import { Routes, Route, Navigate } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { AppShell } from "@/components/AppShell";
import { TenantsPage } from "@/pages/TenantsPage";
import { TenantDetailPage } from "@/pages/TenantDetailPage";
import { PaymentsPage } from "@/pages/PaymentsPage";

export default function App() {
  const { data: who, isLoading, error } = useQuery({
    queryKey: ["whoami"],
    queryFn: async () => {
      const r = await fetch("/admin/whoami", { headers: { Accept: "application/json" } });
      if (r.status === 401) {
        window.location.href = "/admin/login";
        throw new Error("unauthenticated");
      }
      if (!r.ok) throw new Error(`whoami ${r.status}`);
      return r.json() as Promise<{ email: string; name: string }>;
    },
    retry: false,
  });

  if (isLoading) return <div className="p-6 text-sm text-muted-foreground">Loading…</div>;
  if (error) return <div className="p-6 text-sm text-destructive">Auth error: {String(error)}</div>;
  if (!who) return null;

  return (
    <Routes>
      <Route element={<AppShell email={who.email} />}>
        <Route index element={<Navigate to="/tenants" replace />} />
        <Route path="tenants" element={<TenantsPage />} />
        <Route path="tenants/:id" element={<TenantDetailPage />} />
        <Route path="payments" element={<PaymentsPage />} />
      </Route>
    </Routes>
  );
}
