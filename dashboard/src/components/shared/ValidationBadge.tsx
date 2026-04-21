import { Badge } from "@/components/ui/badge";

type ValidationStatus = "match" | "mismatch" | "absent" | undefined;

const statusStyles: Record<string, string> = {
  match: "bg-emerald-500/10 text-emerald-500 border-emerald-500/20",
  mismatch: "bg-red-500/10 text-red-500 border-red-500/20",
  absent: "bg-gray-500/10 text-gray-500 border-gray-500/20",
};

const statusLabels: Record<string, string> = {
  match: "✓",
  mismatch: "✗",
  absent: "–",
};

function SingleBadge({
  label,
  status,
  client,
  expected,
}: {
  label: string;
  status: ValidationStatus;
  client?: string;
  expected?: string;
}) {
  if (!status) {
    return <span className="text-muted-foreground text-xs">-</span>;
  }
  const title =
    status === "mismatch" && (client || expected)
      ? `${label} mismatch\nclient: ${client ?? "?"}\nexpected: ${expected ?? "?"}`
      : `${label}: ${status}`;
  return (
    <Badge
      variant="outline"
      className={`${statusStyles[status] ?? ""} px-1.5 py-0 text-xs font-mono`}
      title={title}
    >
      {label} {statusLabels[status] ?? status}
    </Badge>
  );
}

export function ValidationBadge({
  metadata,
}: {
  metadata?: Record<string, string>;
}) {
  const cch = metadata?.cch_status as ValidationStatus;
  const fp = metadata?.fingerprint_status as ValidationStatus;

  if (!cch && !fp) {
    return <span className="text-muted-foreground">-</span>;
  }

  return (
    <div className="flex items-center gap-1">
      <SingleBadge
        label="CCH"
        status={cch}
        client={metadata?.cch_client}
        expected={metadata?.cch_expected}
      />
      <SingleBadge
        label="FP"
        status={fp}
        client={metadata?.fingerprint_client}
        expected={metadata?.fingerprint_expected}
      />
    </div>
  );
}
