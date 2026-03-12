import { useState } from "react";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useKeys, useCreateKey, useUpdateKey } from "@/api/keys";
import { usePolicies } from "@/api/policies";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { StatusBadge } from "@/components/shared/StatusBadge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Card, CardContent } from "@/components/ui/card";
import type { APIKey } from "@/api/types";
import { Plus, MoreHorizontal, Copy, Check } from "lucide-react";

export function KeysPage() {
  const projectId = useCurrentProject();
  const { data, isLoading } = useKeys(projectId);
  const { data: policiesData } = usePolicies(projectId);
  const createKey = useCreateKey(projectId);
  const updateKey = useUpdateKey(projectId);

  const [showCreate, setShowCreate] = useState(false);
  const [newKeyName, setNewKeyName] = useState("");
  const [newKeyPolicyId, setNewKeyPolicyId] = useState("");
  const [revealedKey, setRevealedKey] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const keys = data?.data ?? [];
  const policies = policiesData?.data ?? [];

  async function handleCreate() {
    const res = await createKey.mutateAsync({
      name: newKeyName,
      rate_limit_policy_id: newKeyPolicyId || undefined,
    });
    setShowCreate(false);
    setNewKeyName("");
    setNewKeyPolicyId("");
    setRevealedKey(res.key);
  }

  function handleCopy() {
    if (revealedKey) {
      navigator.clipboard.writeText(revealedKey);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }

  function policyName(policyId: string | undefined) {
    if (!policyId) return "—";
    const p = policies.find((pol) => pol.id === policyId);
    return p ? p.name : policyId.slice(0, 8);
  }

  const columns: Column<APIKey>[] = [
    { header: "Name", accessor: "name" },
    { header: "Prefix", accessor: "key_prefix" },
    {
      header: "Status",
      accessor: (k) => <StatusBadge status={k.status} />,
    },
    {
      header: "Policy",
      accessor: (k) => policyName(k.rate_limit_policy_id),
    },
    {
      header: "Last Used",
      accessor: (k) =>
        k.last_used_at
          ? new Date(k.last_used_at).toLocaleDateString()
          : "Never",
    },
    {
      header: "Created",
      accessor: (k) => new Date(k.created_at).toLocaleDateString(),
    },
    {
      header: "",
      accessor: (k) => (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={<Button variant="ghost" size="icon" className="h-8 w-8" />}
          >
            <MoreHorizontal className="h-4 w-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {k.status === "active" && (
              <DropdownMenuItem
                onClick={() =>
                  updateKey.mutate({ keyId: k.id, status: "disabled" })
                }
              >
                Disable
              </DropdownMenuItem>
            )}
            {k.status === "disabled" && (
              <DropdownMenuItem
                onClick={() =>
                  updateKey.mutate({ keyId: k.id, status: "active" })
                }
              >
                Enable
              </DropdownMenuItem>
            )}
            {k.status !== "revoked" && (
              <DropdownMenuItem
                className="text-destructive-foreground"
                onClick={() =>
                  updateKey.mutate({ keyId: k.id, status: "revoked" })
                }
              >
                Revoke
              </DropdownMenuItem>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      ),
      className: "w-12",
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="API Keys"
        description="Manage API keys for this project"
        actions={
          <Button onClick={() => setShowCreate(true)}>
            <Plus className="mr-2 h-4 w-4" />
            Create Key
          </Button>
        }
      />

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <p className="p-6 text-muted-foreground">Loading...</p>
          ) : (
            <DataTable
              columns={columns}
              data={keys}
              keyFn={(k) => k.id}
              emptyMessage="No API keys yet"
            />
          )}
        </CardContent>
      </Card>

      {/* Create Key Dialog */}
      <Dialog open={showCreate} onOpenChange={setShowCreate}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Create API Key</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="key-name">Key Name</Label>
              <Input
                id="key-name"
                value={newKeyName}
                onChange={(e) => setNewKeyName(e.target.value)}
                placeholder="Production key"
              />
            </div>
            {policies.length > 0 && (
              <div className="space-y-2">
                <Label>Rate Limit Policy</Label>
                <Select
                  value={newKeyPolicyId}
                  onValueChange={(v) => setNewKeyPolicyId(v ?? "")}
                >
                  <SelectTrigger>
                    <SelectValue placeholder="Default (none)" />
                  </SelectTrigger>
                  <SelectContent>
                    {policies.map((p) => (
                      <SelectItem key={p.id} value={p.id}>
                        {p.name}
                        {p.is_default ? " (default)" : ""}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
          </div>
          <DialogFooter>
            <Button
              onClick={handleCreate}
              disabled={!newKeyName || createKey.isPending}
            >
              {createKey.isPending ? "Creating..." : "Create"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Key Reveal Dialog */}
      <Dialog
        open={!!revealedKey}
        onOpenChange={() => setRevealedKey(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>API Key Created</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <p className="text-sm text-muted-foreground">
              Copy your API key now. You won&apos;t be able to see it again.
            </p>
            <div className="flex items-center gap-2">
              <code className="flex-1 rounded bg-muted px-3 py-2 text-sm font-mono break-all">
                {revealedKey}
              </code>
              <Button variant="outline" size="icon" onClick={handleCopy}>
                {copied ? (
                  <Check className="h-4 w-4" />
                ) : (
                  <Copy className="h-4 w-4" />
                )}
              </Button>
            </div>
          </div>
          <DialogFooter>
            <Button onClick={() => setRevealedKey(null)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
