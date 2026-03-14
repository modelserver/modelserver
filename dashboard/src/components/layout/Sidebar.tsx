import { NavLink, useParams } from "react-router";
import { useAuth } from "@/hooks/useAuth";
import { ProjectSwitcher } from "./ProjectSwitcher";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { Avatar, AvatarImage, AvatarFallback } from "@/components/ui/avatar";
import {
  LayoutDashboard,
  Key,
  Users,
  FileText,
  BarChart3,
  Zap,
  Settings,
  Shield,
  Radio,
  Coins,
  FolderOpen,
  LogOut,
  Route,
} from "lucide-react";
import { cn } from "@/lib/utils";

function SidebarLink({
  to,
  icon: Icon,
  children,
  end,
}: {
  to: string;
  icon: React.ComponentType<{ className?: string }>;
  children: React.ReactNode;
  end?: boolean;
}) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        cn(
          "flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors",
          isActive
            ? "bg-accent text-accent-foreground"
            : "text-muted-foreground hover:bg-accent hover:text-accent-foreground",
        )
      }
    >
      <Icon className="h-4 w-4" />
      {children}
    </NavLink>
  );
}

export function Sidebar() {
  const { user, logout } = useAuth();
  const { projectId } = useParams<{ projectId: string }>();

  const initials =
    user?.nickname
      ?.split(" ")
      .map((w) => w[0])
      .join("")
      .toUpperCase()
      .slice(0, 2) ?? "?";

  return (
    <aside className="flex h-screen w-60 flex-col border-r bg-sidebar text-sidebar-foreground">
      <div className="flex items-center gap-2 px-4 py-4">
        <div className="flex h-8 w-8 items-center justify-center rounded-md bg-primary text-primary-foreground font-bold text-sm">
          MS
        </div>
        <span className="font-semibold">ModelServer</span>
      </div>

      <div className="px-3 pb-2">
        <ProjectSwitcher />
      </div>

      <Separator />

      <nav className="flex-1 space-y-1 overflow-y-auto px-3 py-2">
        <SidebarLink to="/projects" icon={FolderOpen}>
          Projects
        </SidebarLink>

        {projectId && (
          <>
            <div className="pt-3 pb-1 px-3 text-xs font-medium text-muted-foreground uppercase tracking-wider">
              Project
            </div>
            <SidebarLink to={`/projects/${projectId}`} icon={LayoutDashboard} end>
              Overview
            </SidebarLink>
            <SidebarLink to={`/projects/${projectId}/keys`} icon={Key}>
              API Keys
            </SidebarLink>
            <SidebarLink to={`/projects/${projectId}/members`} icon={Users}>
              Members
            </SidebarLink>
            <SidebarLink to={`/projects/${projectId}/requests`} icon={FileText}>
              Requests
            </SidebarLink>
            <SidebarLink to={`/projects/${projectId}/traces`} icon={Route}>
              Traces
            </SidebarLink>
            <SidebarLink to={`/projects/${projectId}/usage`} icon={BarChart3}>
              Usage
            </SidebarLink>
            <SidebarLink to={`/projects/${projectId}/subscription`} icon={Zap}>
              Subscription
            </SidebarLink>
            <SidebarLink
              to={`/projects/${projectId}/settings`}
              icon={Settings}
            >
              Settings
            </SidebarLink>
          </>
        )}

        {user?.is_superadmin && (
          <>
            <div className="pt-3 pb-1 px-3 text-xs font-medium text-muted-foreground uppercase tracking-wider">
              Admin
            </div>
            <SidebarLink to="/admin/users" icon={Shield}>
              Users
            </SidebarLink>
            <SidebarLink to="/admin/projects" icon={FolderOpen}>
              Projects
            </SidebarLink>
            <SidebarLink to="/admin/plans" icon={Coins}>
              Plans
            </SidebarLink>
            <SidebarLink to="/admin/requests" icon={FileText}>
              Requests
            </SidebarLink>
            <SidebarLink to="/admin/channels" icon={Radio}>
              Channels
            </SidebarLink>
            <SidebarLink to="/admin/routes" icon={Route}>
              Routes
            </SidebarLink>
          </>
        )}
      </nav>

      <Separator />

      <div className="flex items-center gap-2 p-3">
        <Avatar className="h-8 w-8">
          {user?.picture && <AvatarImage src={user.picture} alt={user.nickname || user.email} />}
          <AvatarFallback className="text-xs">{initials}</AvatarFallback>
        </Avatar>
        <div className="flex-1 min-w-0">
          <p className="truncate text-sm font-medium">{user?.nickname || user?.email}</p>
        </div>
        <Button variant="ghost" size="icon" className="h-8 w-8" onClick={logout} title="Sign out">
          <LogOut className="h-4 w-4" />
        </Button>
      </div>
    </aside>
  );
}
