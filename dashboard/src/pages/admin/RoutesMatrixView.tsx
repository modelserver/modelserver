import { useMemo } from "react";
import { useSearchParams } from "react-router";
import { useRoutingMatrix, useClientBuckets } from "@/api/upstreams";
import type { RoutingMatrixCell } from "@/api/types";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Loader2 } from "lucide-react";

interface RoutesMatrixViewProps {
  onEditRoute: (routeId: string) => void;
}

export function RoutesMatrixView({ onEditRoute }: RoutesMatrixViewProps) {
  const [params, setParams] = useSearchParams();
  const clientFilter = params.get("client") ?? "";

  const { data: clientsList } = useClientBuckets();

  const { data, isLoading, error } = useRoutingMatrix({ client: clientFilter });

  // Cell lookup keyed by `${model}::${kind}`. Stores an array because the
  // unfiltered matrix returns one cell per (model, kind, client) 3-tuple —
  // multiple cells may share the same (model, kind) when per-client routing
  // differs. The render logic detects "mixed" routes and shows a badge.
  const cellIndex = useMemo(() => {
    const m = new Map<string, RoutingMatrixCell[]>();
    for (const c of data?.data.cells ?? []) {
      const key = `${c.model}::${c.kind}`;
      const arr = m.get(key) ?? [];
      arr.push(c);
      m.set(key, arr);
    }
    return m;
  }, [data]);

  if (isLoading) {
    return (
      <Card>
        <CardContent className="flex items-center justify-center py-10 text-muted-foreground">
          <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          Loading matrix…
        </CardContent>
      </Card>
    );
  }

  if (error) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-destructive-foreground text-sm">
          Failed to load route matrix.
        </CardContent>
      </Card>
    );
  }

  const models = data?.data.models ?? [];
  const kinds = data?.data.kinds ?? [];

  if (models.length === 0) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-sm text-muted-foreground">
          No active models in catalog.
        </CardContent>
      </Card>
    );
  }

  return (
    <>
      <div className="mb-4">
        <div className="space-y-1">
          <Label className="text-xs">Client</Label>
          <Select
            value={clientFilter || "all"}
            onValueChange={(v) => {
              const next = new URLSearchParams(params);
              if (!v || v === "all") next.delete("client");
              else next.set("client", v);
              setParams(next, { replace: true });
            }}
          >
            <SelectTrigger className="w-[180px]">
              <SelectValue placeholder="All clients" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All clients</SelectItem>
              {(clientsList?.data ?? []).map((c) => (
                <SelectItem key={c} value={c}>{c}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>
      <Card>
      <CardContent className="p-0">
        <div className="overflow-x-auto">
          <table className="w-full border-separate border-spacing-0 text-sm">
            <thead>
              <tr>
                <th
                  scope="col"
                  className="sticky top-0 left-0 z-30 bg-background text-left font-medium px-3 py-2 border-b border-r"
                >
                  Model
                </th>
                {kinds.map((k) => (
                  <th
                    key={k}
                    scope="col"
                    className="sticky top-0 z-20 bg-background text-left font-mono text-xs font-medium px-3 py-2 border-b whitespace-nowrap"
                  >
                    {k}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {models.map((model) => (
                <tr key={model}>
                  <th
                    scope="row"
                    className="sticky left-0 z-10 bg-background text-left font-mono text-xs font-medium px-3 py-2 border-b border-r whitespace-nowrap"
                  >
                    {model}
                  </th>
                  {kinds.map((kind) => {
                    const cells = cellIndex.get(`${model}::${kind}`) ?? [];
                    if (cells.length === 0) {
                      return (
                        <td key={kind} className="px-3 py-2 border-b align-middle">
                          <span className="text-muted-foreground">—</span>
                        </td>
                      );
                    }
                    const distinctRouteIDs = new Set(cells.map((c) => c.route_id));
                    const isMixed = distinctRouteIDs.size > 1;
                    const firstCell = cells[0]!; // safe: cells.length === 0 returns early above
                    const label = isMixed
                      ? "Mixed"
                      : (firstCell.upstream_group_name || firstCell.upstream_group_id.slice(0, 8));
                    const title = isMixed
                      ? `Routes differ by client (${distinctRouteIDs.size} distinct routes). Click to open the first; use the Client filter to see all variants.`
                      : `route ${firstCell.route_id.slice(0, 8)} (priority ${firstCell.match_priority})`;
                    return (
                      <td
                        key={kind}
                        className="px-3 py-2 border-b align-middle"
                      >
                        <button
                          type="button"
                          onClick={() => onEditRoute(firstCell.route_id)}
                          className="inline-flex"
                          title={title}
                        >
                          <Badge
                            variant={isMixed ? "secondary" : "outline"}
                            className="cursor-pointer"
                          >
                            {label}
                          </Badge>
                        </button>
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </CardContent>
    </Card>
    </>
  );
}
