import { useState } from "react";
import { useAdminRequests, type AdminRequestFilters } from "@/api/adminRequests";
import { useChannels } from "@/api/channels";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Pagination } from "@/components/shared/Pagination";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { DateRangePicker } from "@/components/shared/DateRangePicker";
import { Input } from "@/components/ui/input";
import { Card, CardContent } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import type { Request } from "@/api/types";

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

function defaultSince() {
  const d = new Date();
  d.setDate(d.getDate() - 7);
  return d.toISOString().split("T")[0]!;
}

function defaultUntil() {
  return new Date().toISOString().split("T")[0]!;
}

export function AdminRequestsPage() {
  const [page, setPage] = useState(1);
  const [model, setModel] = useState("");
  const [status, setStatus] = useState("");
  const [since, setSince] = useState(defaultSince);
  const [until, setUntil] = useState(defaultUntil);
  const [selected, setSelected] = useState<Request | null>(null);

  const { data: channelsData } = useChannels();
  const channels = channelsData?.data ?? [];

  const filters: AdminRequestFilters = {
    page,
    per_page: 20,
    model: model || undefined,
    status: status || undefined,
    since: since ? `${since}T00:00:00Z` : undefined,
    until: until ? `${until}T23:59:59Z` : undefined,
  };

  const { data, isLoading } = useAdminRequests(filters);
  const requests = data?.data ?? [];
  const meta = data?.meta;

  function channelName(id: string) {
    const ch = channels.find((c) => c.id === id);
    return ch ? ch.name : id.slice(0, 8);
  }

  const columns: Column<Request>[] = [
    {
      header: "Project",
      accessor: (r) => (
        <span className="font-mono text-xs">{r.project_id.slice(0, 8)}</span>
      ),
    },
    { header: "Model", accessor: "model" },
    {
      header: "Status",
      accessor: (r) => <StatusBadge status={r.status} />,
    },
    {
      header: "Channel",
      accessor: (r) => channelName(r.channel_id),
    },
    {
      header: "Stream",
      accessor: (r) => (r.streaming ? "SSE" : "Sync"),
      className: "text-center",
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
      header: "Duration",
      accessor: (r) => `${r.latency_ms}ms`,
      className: "text-right",
    },
    {
      header: "TTFT",
      accessor: (r) => (r.ttft_ms > 0 ? `${r.ttft_ms}ms` : "-"),
      className: "text-right",
    },
    {
      header: "Client IP",
      accessor: (r) =>
        r.client_ip ? (
          <span className="font-mono text-xs">{r.client_ip}</span>
        ) : (
          <span className="text-muted-foreground">-</span>
        ),
    },
    {
      header: "Time",
      accessor: (r) => new Date(r.created_at).toLocaleString(),
    },
  ];

  const totalPages = meta ? Math.ceil(meta.total / meta.per_page) : 1;

  return (
    <div className="space-y-6">
      <PageHeader
        title="All Requests"
        description="Global request logs across all projects"
      />

      <div className="flex flex-wrap items-end gap-3">
        <DateRangePicker
          since={since}
          until={until}
          onSinceChange={setSince}
          onUntilChange={setUntil}
        />
        <Input
          placeholder="Filter by model..."
          value={model}
          onChange={(e) => {
            setModel(e.target.value);
            setPage(1);
          }}
          className="w-48"
        />
        <Select
          value={status}
          onValueChange={(v) => {
            setStatus(!v || v === "all" ? "" : v);
            setPage(1);
          }}
        >
          <SelectTrigger className="w-40">
            <SelectValue placeholder="All statuses" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            <SelectItem value="success">Success</SelectItem>
            <SelectItem value="error">Error</SelectItem>
            <SelectItem value="rate_limited">Rate Limited</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <p className="p-6 text-muted-foreground">Loading...</p>
          ) : (
            <DataTable
              columns={columns}
              data={requests}
              keyFn={(r) => r.id}
              emptyMessage="No requests found"
              onRowClick={setSelected}
            />
          )}
        </CardContent>
      </Card>

      {meta && meta.total > 0 && (
        <Pagination
          page={page}
          totalPages={totalPages}
          total={meta.total}
          perPage={meta.per_page}
          onPageChange={setPage}
        />
      )}

      {/* Detail drawer */}
      <Sheet open={!!selected} onOpenChange={() => setSelected(null)}>
        <SheetContent className="overflow-y-auto">
          <SheetHeader>
            <SheetTitle>Request Details</SheetTitle>
          </SheetHeader>
          {selected && (
            <div className="space-y-4 px-4 pb-4 text-sm">
              <DetailRow label="ID" value={selected.id} />
              <DetailRow label="Project" value={selected.project_id} />
              {selected.msg_id && (
                <DetailRow label="Msg ID" value={selected.msg_id} />
              )}
              <DetailRow label="Model" value={selected.model} />
              <DetailRow label="Provider" value={selected.provider} />
              <DetailRow label="Channel" value={channelName(selected.channel_id)} />
              <DetailRow label="Status" value={selected.status} />
              <DetailRow label="Streaming" value={selected.streaming ? "Yes" : "No"} />
              <DetailRow label="Input Tokens" value={formatTokens(selected.input_tokens)} />
              <DetailRow label="Output Tokens" value={formatTokens(selected.output_tokens)} />
              <DetailRow label="Cache Creation" value={formatTokens(selected.cache_creation_tokens)} />
              <DetailRow label="Cache Read" value={formatTokens(selected.cache_read_tokens)} />
              <DetailRow label="Duration" value={`${selected.latency_ms}ms`} />
              <DetailRow label="TTFT" value={selected.ttft_ms > 0 ? `${selected.ttft_ms}ms` : "-"} />
              {selected.trace_id && (
                <DetailRow label="Trace ID" value={selected.trace_id} />
              )}
              {selected.client_ip && (
                <DetailRow label="Client IP" value={selected.client_ip} />
              )}
              {selected.error_message && (
                <div className="space-y-1">
                  <span className="text-muted-foreground">Error</span>
                  <pre className="whitespace-pre-wrap break-all rounded bg-destructive/10 p-3 text-xs text-destructive">
                    {selected.error_message}
                  </pre>
                </div>
              )}
              <DetailRow label="Time" value={new Date(selected.created_at).toLocaleString()} />
            </div>
          )}
        </SheetContent>
      </Sheet>
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
