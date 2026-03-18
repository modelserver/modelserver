import { useRoutingHealth } from "@/api/upstreams";
import { PageHeader } from "@/components/layout/PageHeader";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { UpstreamHealth, GroupHealth } from "@/api/types";
import { Loader2 } from "lucide-react";

const circuitStateStyles: Record<string, string> = {
  closed: "bg-emerald-500/10 text-emerald-500 border-emerald-500/20",
  half_open: "bg-yellow-500/10 text-yellow-500 border-yellow-500/20",
  open: "bg-red-500/10 text-red-500 border-red-500/20",
};

const healthStatusStyles: Record<string, string> = {
  ok: "bg-emerald-500/10 text-emerald-500 border-emerald-500/20",
  degraded: "bg-yellow-500/10 text-yellow-500 border-yellow-500/20",
  down: "bg-red-500/10 text-red-500 border-red-500/20",
  unknown: "bg-gray-500/10 text-gray-500 border-gray-500/20",
};

function UpstreamCard({ upstream }: { upstream: UpstreamHealth }) {
  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <CardTitle className="text-base font-medium">{upstream.name}</CardTitle>
          <Badge variant="outline" className="text-xs">
            {upstream.provider}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">Circuit</span>
          <Badge variant="outline" className={circuitStateStyles[upstream.circuit_state] ?? ""}>
            {upstream.circuit_state.replace(/_/g, " ")}
          </Badge>
        </div>
        <div className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">Health</span>
          <Badge variant="outline" className={healthStatusStyles[upstream.health_status] ?? ""}>
            {upstream.health_status}
          </Badge>
        </div>
        <div className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">Active Connections</span>
          <span className="text-sm font-medium">{upstream.active_connections}</span>
        </div>
        <div className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">Recent Errors</span>
          <span className={`text-sm font-medium ${upstream.recent_errors > 0 ? "text-red-500" : ""}`}>
            {upstream.recent_errors}
          </span>
        </div>
        {upstream.last_check_at && (
          <div className="flex items-center justify-between">
            <span className="text-sm text-muted-foreground">Last Check</span>
            <span className="text-xs text-muted-foreground">
              {new Date(upstream.last_check_at).toLocaleString()}
            </span>
          </div>
        )}
        {upstream.last_error_at && (
          <div className="flex items-center justify-between">
            <span className="text-sm text-muted-foreground">Last Error</span>
            <span className="text-xs text-red-500">
              {new Date(upstream.last_error_at).toLocaleString()}
            </span>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function GroupCard({ group }: { group: GroupHealth }) {
  const allHealthy = group.healthy_members === group.total_members;
  const noneHealthy = group.healthy_members === 0 && group.total_members > 0;

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <CardTitle className="text-base font-medium">{group.name}</CardTitle>
          <Badge variant="outline" className="text-xs">
            {group.lb_policy.replace(/_/g, " ")}
          </Badge>
        </div>
      </CardHeader>
      <CardContent>
        <div className="flex items-center justify-between">
          <span className="text-sm text-muted-foreground">Healthy Members</span>
          <span
            className={`text-sm font-medium ${
              noneHealthy ? "text-red-500" : allHealthy ? "text-emerald-500" : "text-yellow-500"
            }`}
          >
            {group.healthy_members} / {group.total_members}
          </span>
        </div>
      </CardContent>
    </Card>
  );
}

export function RoutingHealthPage() {
  const { data, isLoading } = useRoutingHealth();

  const upstreams = data?.data?.upstreams ?? [];
  const groups = data?.data?.groups ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Routing Health"
        description="Live health status of upstreams and groups (auto-refreshes every 10s)"
      />

      {isLoading ? (
        <div className="flex items-center gap-2 p-6 text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          Loading health data...
        </div>
      ) : (
        <>
          <div>
            <h2 className="text-lg font-semibold mb-3">Upstreams</h2>
            {upstreams.length === 0 ? (
              <p className="text-sm text-muted-foreground">No upstream health data available</p>
            ) : (
              <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
                {upstreams.map((u) => (
                  <UpstreamCard key={u.id} upstream={u} />
                ))}
              </div>
            )}
          </div>

          <div>
            <h2 className="text-lg font-semibold mb-3">Groups</h2>
            {groups.length === 0 ? (
              <p className="text-sm text-muted-foreground">No group health data available</p>
            ) : (
              <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
                {groups.map((g) => (
                  <GroupCard key={g.id} group={g} />
                ))}
              </div>
            )}
          </div>
        </>
      )}
    </div>
  );
}
