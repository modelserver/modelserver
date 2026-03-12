import { useState } from "react";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useTraces, useTraceRequests } from "@/api/traces";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import type { Trace, Request } from "@/api/types";
import { ChevronLeft, ChevronRight } from "lucide-react";

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

export function TracesPage() {
  const projectId = useCurrentProject();
  const [page, setPage] = useState(1);
  const [selected, setSelected] = useState<Trace | null>(null);

  const { data, isLoading } = useTraces(projectId, page, 20);
  const traces = data?.data ?? [];
  const meta = data?.meta;

  const columns: Column<Trace>[] = [
    {
      header: "Trace ID",
      accessor: (t) => (
        <span className="font-mono text-xs">{t.id.slice(0, 8)}...</span>
      ),
    },
    {
      header: "Source",
      accessor: (t) => <StatusBadge status={t.source} />,
    },
    {
      header: "Thread",
      accessor: (t) =>
        t.thread_id ? (
          <span className="font-mono text-xs">{t.thread_id.slice(0, 8)}...</span>
        ) : (
          <span className="text-muted-foreground">-</span>
        ),
    },
    {
      header: "Created",
      accessor: (t) => new Date(t.created_at).toLocaleString(),
    },
    {
      header: "Updated",
      accessor: (t) => new Date(t.updated_at).toLocaleString(),
    },
  ];

  const totalPages = meta ? Math.ceil(meta.total / meta.per_page) : 1;

  return (
    <div className="space-y-6">
      <PageHeader title="Traces" description="View distributed traces across API requests" />

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <p className="p-6 text-muted-foreground">Loading...</p>
          ) : (
            <DataTable
              columns={columns}
              data={traces}
              keyFn={(t) => t.id}
              emptyMessage="No traces found"
              onRowClick={setSelected}
            />
          )}
        </CardContent>
      </Card>

      {meta && meta.total > 0 && (
        <div className="flex items-center justify-between text-sm text-muted-foreground">
          <span>
            Showing {(page - 1) * meta.per_page + 1}–
            {Math.min(page * meta.per_page, meta.total)} of {meta.total}
          </span>
          <div className="flex gap-1">
            <Button
              variant="outline"
              size="icon"
              className="h-8 w-8"
              disabled={page <= 1}
              onClick={() => setPage((p) => p - 1)}
            >
              <ChevronLeft className="h-4 w-4" />
            </Button>
            <Button
              variant="outline"
              size="icon"
              className="h-8 w-8"
              disabled={page >= totalPages}
              onClick={() => setPage((p) => p + 1)}
            >
              <ChevronRight className="h-4 w-4" />
            </Button>
          </div>
        </div>
      )}

      {/* Detail drawer */}
      <Sheet open={!!selected} onOpenChange={() => setSelected(null)}>
        <SheetContent className="overflow-y-auto sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>Trace Details</SheetTitle>
          </SheetHeader>
          {selected && (
            <TraceDetail projectId={projectId} trace={selected} />
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}

function TraceDetail({ projectId, trace }: { projectId: string; trace: Trace }) {
  const { data, isLoading } = useTraceRequests(projectId, trace.id);
  const requests = data?.data ?? [];

  const reqColumns: Column<Request>[] = [
    {
      header: "Model",
      accessor: (r) => (
        <span className="text-xs">{r.model}</span>
      ),
    },
    {
      header: "Status",
      accessor: (r) => <StatusBadge status={r.status} />,
    },
    {
      header: "Input",
      accessor: (r) => formatTokens(r.input_tokens),
      className: "text-right",
    },
    {
      header: "Output",
      accessor: (r) => formatTokens(r.output_tokens),
      className: "text-right",
    },
    {
      header: "Latency",
      accessor: (r) => `${r.latency_ms}ms`,
      className: "text-right",
    },
    {
      header: "Time",
      accessor: (r) => new Date(r.created_at).toLocaleTimeString(),
    },
  ];

  return (
    <div className="mt-4 space-y-6 text-sm">
      <div className="space-y-4">
        <DetailRow label="Trace ID" value={trace.id} />
        <DetailRow label="Source" value={trace.source} />
        {trace.thread_id && (
          <DetailRow label="Thread ID" value={trace.thread_id} />
        )}
        <DetailRow label="Created" value={new Date(trace.created_at).toLocaleString()} />
        <DetailRow label="Updated" value={new Date(trace.updated_at).toLocaleString()} />
      </div>

      <div>
        <h4 className="font-medium mb-2">Requests ({requests.length})</h4>
        {isLoading ? (
          <p className="text-muted-foreground">Loading requests...</p>
        ) : requests.length === 0 ? (
          <p className="text-muted-foreground">No requests for this trace</p>
        ) : (
          <div className="border rounded-md">
            <DataTable
              columns={reqColumns}
              data={requests}
              keyFn={(r) => r.id}
              emptyMessage="No requests"
            />
          </div>
        )}
      </div>
    </div>
  );
}

function DetailRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between border-b pb-2">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono text-right break-all max-w-[60%]">{value}</span>
    </div>
  );
}
