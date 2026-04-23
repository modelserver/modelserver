import { useState } from "react";
import {
  useAdminExtraUsageOverview,
  useSetExtraUsageBypass,
  type AdminExtraUsageRow,
} from "@/api/extra-usage";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

function fenToYuan(fen: number): string {
  return (fen / 100).toFixed(2);
}

export function AdminExtraUsagePage() {
  const { data, isLoading } = useAdminExtraUsageOverview();
  const setBypass = useSetExtraUsageBypass();
  const [lookupId, setLookupId] = useState("");

  const rows = data?.data ?? [];

  const columns: Column<AdminExtraUsageRow>[] = [
    {
      header: "Project",
      accessor: (r) => (
        <code className="text-xs text-muted-foreground">{r.project_id.slice(0, 8)}</code>
      ),
      className: "w-28",
    },
    {
      header: "Enabled",
      accessor: (r) => (r.enabled ? "Yes" : "No"),
      className: "w-20",
    },
    {
      header: "Balance ¥",
      accessor: (r) => (
        <span className={r.balance_fen < 0 ? "text-red-600" : undefined}>
          {fenToYuan(r.balance_fen)}
        </span>
      ),
      className: "w-28 text-right",
    },
    {
      header: "Monthly limit ¥",
      accessor: (r) => (r.monthly_limit_fen > 0 ? fenToYuan(r.monthly_limit_fen) : "—"),
      className: "w-32 text-right",
    },
    {
      header: "7d spend ¥",
      accessor: (r) => fenToYuan(r.spend_7d_fen),
      className: "w-28 text-right",
    },
    {
      header: "Bypass",
      accessor: (r) => (
        <Button
          variant={r.bypass_balance_check ? "default" : "outline"}
          size="sm"
          onClick={() =>
            setBypass.mutate({
              projectId: r.project_id,
              bypass: !r.bypass_balance_check,
            })
          }
          disabled={setBypass.isPending}
        >
          {r.bypass_balance_check ? "On" : "Off"}
        </Button>
      ),
      className: "w-24",
    },
  ];

  return (
    <div className="space-y-4">
      <PageHeader
        title="Extra Usage"
        description="Per-project balance and superadmin bypass"
      />

      <Card>
        <CardContent className="p-4 flex items-end gap-2">
          <div className="flex-1">
            <label className="text-xs text-muted-foreground block mb-1">
              Set bypass on a project by ID (creates the settings row if absent)
            </label>
            <Input
              placeholder="project UUID"
              value={lookupId}
              onChange={(e) => setLookupId(e.target.value.trim())}
            />
          </div>
          <Button
            onClick={() => {
              if (lookupId) setBypass.mutate({ projectId: lookupId, bypass: true });
            }}
            disabled={!lookupId || setBypass.isPending}
          >
            Enable bypass
          </Button>
          <Button
            variant="outline"
            onClick={() => {
              if (lookupId) setBypass.mutate({ projectId: lookupId, bypass: false });
            }}
            disabled={!lookupId || setBypass.isPending}
          >
            Disable bypass
          </Button>
        </CardContent>
      </Card>

      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : (
        <DataTable
          columns={columns}
          data={rows}
          keyFn={(r) => r.project_id}
          emptyMessage="No projects with extra-usage settings yet"
        />
      )}
    </div>
  );
}
