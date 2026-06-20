import { useState } from "react";

export function SecretRevealOnce({
  secret, onAcknowledge,
}: { secret: string; onAcknowledge: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="w-[480px] space-y-4 rounded-md bg-background p-6 shadow-lg">
        <h2 className="text-lg font-semibold">Save this secret now</h2>
        <p className="text-sm text-muted-foreground">
          This is the only time the secret will be shown. Copy it and store it in the upstream's
          config. If you lose it, you'll need to rotate.
        </p>
        <pre className="overflow-x-auto break-all rounded border bg-muted p-3 font-mono text-sm">
          {secret}
        </pre>
        <div className="flex justify-end gap-2">
          <button
            onClick={async () => {
              await navigator.clipboard.writeText(secret);
              setCopied(true);
            }}
            className="rounded border px-3 py-1 text-sm"
          >
            {copied ? "Copied" : "Copy"}
          </button>
          <button
            disabled={!copied}
            onClick={onAcknowledge}
            className="rounded bg-primary px-3 py-1 text-sm text-primary-foreground disabled:opacity-50"
          >
            I've saved it
          </button>
        </div>
      </div>
    </div>
  );
}
