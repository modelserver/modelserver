import { Badge } from "@/components/ui/badge";

const statusStyles: Record<string, string> = {
  active: "bg-emerald-500/10 text-emerald-500 border-emerald-500/20",
  success: "bg-emerald-500/10 text-emerald-500 border-emerald-500/20",
  disabled: "bg-yellow-500/10 text-yellow-500 border-yellow-500/20",
  revoked: "bg-red-500/10 text-red-500 border-red-500/20",
  error: "bg-red-500/10 text-red-500 border-red-500/20",
  rate_limited: "bg-orange-500/10 text-orange-500 border-orange-500/20",
  suspended: "bg-red-500/10 text-red-500 border-red-500/20",
  archived: "bg-gray-500/10 text-gray-500 border-gray-500/20",
  pending: "bg-blue-500/10 text-blue-500 border-blue-500/20",
  paid: "bg-emerald-500/10 text-emerald-500 border-emerald-500/20",
  expired: "bg-gray-500/10 text-gray-500 border-gray-500/20",
  // Trace sources
  header: "bg-blue-500/10 text-blue-500 border-blue-500/20",
  "claude-code": "bg-violet-500/10 text-violet-500 border-violet-500/20",
  opencode: "bg-teal-500/10 text-teal-500 border-teal-500/20",
  codex: "bg-amber-500/10 text-amber-500 border-amber-500/20",
  openclaw: "bg-rose-500/10 text-rose-500 border-rose-500/20",
  body: "bg-cyan-500/10 text-cyan-500 border-cyan-500/20",
  auto: "bg-gray-500/10 text-gray-500 border-gray-500/20",
};

export function StatusBadge({ status }: { status: string }) {
  return (
    <Badge variant="outline" className={statusStyles[status] ?? ""}>
      {status.replace(/_/g, " ")}
    </Badge>
  );
}
