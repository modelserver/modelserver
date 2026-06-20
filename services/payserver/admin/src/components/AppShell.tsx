import { Link, Outlet, useLocation } from "react-router";
import { cn } from "@/lib/utils";

export function AppShell({ email }: { email: string }) {
  const loc = useLocation();
  const nav = [
    { to: "/tenants", label: "Tenants" },
    { to: "/payments", label: "Payments" },
  ];
  async function logout() {
    await fetch("/admin/logout", { method: "POST" });
    window.location.href = "/admin/login";
  }
  return (
    <div className="min-h-screen flex">
      <aside className="w-56 border-r p-4 space-y-1">
        <div className="text-sm font-semibold mb-3">Payserver Admin</div>
        {nav.map((n) => (
          <Link
            key={n.to}
            to={n.to}
            className={cn(
              "block rounded px-3 py-2 text-sm hover:bg-accent",
              loc.pathname.startsWith(n.to) && "bg-accent font-medium",
            )}
          >
            {n.label}
          </Link>
        ))}
      </aside>
      <div className="flex-1 flex flex-col">
        <header className="flex items-center justify-end gap-4 border-b px-6 py-2 text-sm">
          <span className="text-muted-foreground">{email}</span>
          <button onClick={logout} className="text-sm underline">Logout</button>
        </header>
        <main className="flex-1 p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
