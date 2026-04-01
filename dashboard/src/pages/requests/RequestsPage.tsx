import { useState } from "react";
import { Link } from "react-router";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useRequests, type RequestFilters } from "@/api/requests";
import { useKeys } from "@/api/keys";
import { useMembers } from "@/api/members";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
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
import { Pagination } from "@/components/shared/Pagination";
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

export function RequestsPage() {
  const projectId = useCurrentProject();
  const [page, setPage] = useState(1);
  const [model, setModel] = useState("");
  const [status, setStatus] = useState("");
  const [apiKeyId, setApiKeyId] = useState("");
  const [since, setSince] = useState(defaultSince);
  const [until, setUntil] = useState(defaultUntil);
  const [createdBy, setCreatedBy] = useState("");
  const [selected, setSelected] = useState<Request | null>(null);

  const { data: keysData } = useKeys(projectId);
  const apiKeys = keysData?.data ?? [];

  const { data: membersData } = useMembers(projectId, 1, 100);
  const members = membersData?.data ?? [];

  const filters: RequestFilters = {
    page,
    per_page: 20,
    model: model || undefined,
    status: status || undefined,
    api_key_id: apiKeyId || undefined,
    created_by: createdBy || undefined,
    since: since ? `${since}T00:00:00Z` : undefined,
    until: until ? `${until}T23:59:59Z` : undefined,
  };

  const { data, isLoading } = useRequests(projectId, filters);
  const requests = data?.data ?? [];
  const meta = data?.meta;

  function keyName(id: string) {
    const k = apiKeys.find((key) => key.id === id);
    return k ? k.name : id.slice(0, 8);
  }

  const columns: Column<Request>[] = [
    {
      header: "Msg ID",
      accessor: (r) => r.msg_id ? (
        <span className="font-mono text-xs">{r.msg_id.slice(0, 12)}...</span>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    },
    { header: "Model", accessor: "model" },
    {
      header: "Status",
      accessor: (r) => <StatusBadge status={r.status} />,
    },
    {
      header: "Stream",
      accessor: (r) => r.streaming ? "SSE" : "Sync",
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
      header: "Cache Create",
      accessor: (r) => formatTokens(r.cache_creation_tokens),
      className: "text-right",
    },
    {
      header: "Cache Read",
      accessor: (r) => formatTokens(r.cache_read_tokens),
      className: "text-right",
    },
    {
      header: "Duration",
      accessor: (r) => `${r.latency_ms}ms`,
      className: "text-right",
    },
    {
      header: "TTFT",
      accessor: (r) => r.ttft_ms > 0 ? `${r.ttft_ms}ms` : "-",
      className: "text-right",
    },
    {
      header: "Auth",
      accessor: (r) => {
        if (r.api_key_id) {
          return keyName(r.api_key_id);
        }
        if (r.oauth_grant_client_name) {
          return <span className="text-xs">{r.oauth_grant_client_name}</span>;
        }
        return <span className="text-muted-foreground">-</span>;
      },
    },
    {
      header: "Trace",
      accessor: (r) => r.trace_id ? (
        <Link
          to={`/projects/${projectId}/traces`}
          className="font-mono text-xs text-blue-600 hover:underline dark:text-blue-400"
          onClick={(e) => e.stopPropagation()}
        >
          {r.trace_id.slice(0, 8)}...
        </Link>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    },
    {
      header: "Client IP",
      accessor: (r) => r.client_ip ? (
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
      <PageHeader title="Request Logs" description="Browse and filter API requests" />

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
        {apiKeys.length > 0 && (
          <Select
            value={apiKeyId}
            onValueChange={(v) => {
              setApiKeyId(!v || v === "all" ? "" : v);
              setPage(1);
            }}
          >
            <SelectTrigger className="w-44">
              <SelectValue placeholder="All keys" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All keys</SelectItem>
              {apiKeys.map((k) => (
                <SelectItem key={k.id} value={k.id}>
                  {k.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {members.length > 0 && (
          <Select
            value={createdBy}
            onValueChange={(v) => {
              setCreatedBy(!v || v === "all" ? "" : v);
              setPage(1);
            }}
          >
            <SelectTrigger className="w-44">
              <SelectValue placeholder="All members" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All members</SelectItem>
              {members.map((m) => (
                <SelectItem key={m.user_id} value={m.user_id}>
                  {m.user?.nickname || m.user?.email || m.user_id.slice(0, 8)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
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
              {selected.msg_id && (
                <DetailRow label="Msg ID" value={selected.msg_id} />
              )}
              <DetailRow label="Model" value={selected.model} />
              {selected.provider && (
                <DetailRow label="Provider" value={selected.provider} />
              )}
              <DetailRow label="Status" value={selected.status} />
              <DetailRow label="Streaming" value={selected.streaming ? "Yes" : "No"} />
              {selected.api_key_id ? (
                <DetailRow label="API Key" value={keyName(selected.api_key_id)} />
              ) : selected.oauth_grant_client_name ? (
                <DetailRow label="OAuth App" value={selected.oauth_grant_client_name} />
              ) : null}
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
              {selected.metadata && Object.entries(selected.metadata).map(([k, v]) => (
                <DetailRow key={k} label={k.replace(/_/g, "-")} value={v} />
              ))}
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
