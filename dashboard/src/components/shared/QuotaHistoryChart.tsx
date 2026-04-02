import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
  ReferenceLine,
  ResponsiveContainer,
  CartesianGrid,
} from "recharts";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { QuotaWindowHistory } from "@/api/types";

const tooltipStyle = {
  backgroundColor: "hsl(var(--card))",
  border: "1px solid hsl(var(--border))",
  borderRadius: "var(--radius)",
};

function formatTick(ts: string): string {
  // Hourly: "2026-04-02T10:00:00Z" → "10:00"
  if (ts.includes("T")) {
    return ts.slice(11, 16);
  }
  // Daily: "2026-04-01" → "04-01"
  return ts.slice(5);
}

interface Props {
  windows: QuotaWindowHistory[];
}

export function QuotaHistoryChart({ windows }: Props) {
  if (windows.length === 0) return null;

  return (
    <div className="space-y-4">
      {windows.map((w) => (
        <Card key={w.window}>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm font-medium capitalize">
              {w.window} quota usage
            </CardTitle>
          </CardHeader>
          <CardContent>
            {w.series.length > 0 ? (
              <ResponsiveContainer width="100%" height={200}>
                <AreaChart data={w.series}>
                  <CartesianGrid strokeDasharray="3 3" opacity={0.1} />
                  <XAxis
                    dataKey="timestamp"
                    tickFormatter={formatTick}
                    fontSize={12}
                    stroke="currentColor"
                    opacity={0.5}
                  />
                  <YAxis
                    domain={[0, (max: number) => Math.max(max, 110)]}
                    fontSize={12}
                    stroke="currentColor"
                    opacity={0.5}
                    tickFormatter={(v: number) => `${v}%`}
                  />
                  <Tooltip
                    contentStyle={tooltipStyle}
                    formatter={(value: number) => [`${value.toFixed(2)}%`, "Usage"]}
                    labelFormatter={formatTick}
                  />
                  <ReferenceLine
                    y={100}
                    stroke="hsl(var(--destructive))"
                    strokeDasharray="4 4"
                    label={{ value: "Quota", position: "right", fill: "hsl(var(--destructive))", fontSize: 12 }}
                  />
                  <Area
                    type="monotone"
                    dataKey="percentage"
                    stroke="oklch(0.488 0.243 264.376)"
                    fill="oklch(0.488 0.243 264.376)"
                    fillOpacity={0.15}
                    strokeWidth={2}
                    dot={false}
                  />
                </AreaChart>
              </ResponsiveContainer>
            ) : (
              <p className="py-6 text-center text-sm text-muted-foreground">
                No usage data
              </p>
            )}
          </CardContent>
        </Card>
      ))}
    </div>
  );
}
