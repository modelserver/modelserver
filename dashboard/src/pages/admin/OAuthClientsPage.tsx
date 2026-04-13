import { useState } from "react";
import {
  useOAuthClients,
  useCreateOAuthClient,
  useUpdateOAuthClient,
  useDeleteOAuthClient,
} from "@/api/oauth-clients";
import type { OAuthClient } from "@/api/oauth-clients";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Plus, MoreHorizontal, Pencil, Trash2, Loader2, Copy, Check } from "lucide-react";
import { toast } from "sonner";

const GRANT_TYPE_OPTIONS = [
  { value: "authorization_code", label: "Authorization Code" },
  { value: "refresh_token", label: "Refresh Token" },
  { value: "client_credentials", label: "Client Credentials" },
];

const RESPONSE_TYPE_OPTIONS = [
  { value: "code", label: "Code" },
  { value: "token", label: "Token" },
];

interface OAuthClientFormState {
  client_name: string;
  client_id: string;
  redirect_uris: string;
  grant_types: string[];
  response_types: string[];
  scope: string;
  token_endpoint_auth_method: string;
  regenerate_secret: boolean;
}

function emptyForm(): OAuthClientFormState {
  return {
    client_name: "",
    client_id: "",
    redirect_uris: "",
    grant_types: ["authorization_code", "refresh_token"],
    response_types: ["code"],
    scope: "project:inference offline_access",
    token_endpoint_auth_method: "client_secret_post",
    regenerate_secret: false,
  };
}

function clientToForm(c: OAuthClient): OAuthClientFormState {
  return {
    client_name: c.client_name,
    client_id: c.client_id,
    redirect_uris: c.redirect_uris.join("\n"),
    grant_types: c.grant_types,
    response_types: c.response_types,
    scope: c.scope,
    token_endpoint_auth_method: c.token_endpoint_auth_method,
    regenerate_secret: false,
  };
}

function toggleItem(arr: string[], item: string): string[] {
  return arr.includes(item) ? arr.filter((v) => v !== item) : [...arr, item];
}

export function OAuthClientsPage() {
  const { data, isLoading, isError } = useOAuthClients();
  const createClient = useCreateOAuthClient();
  const updateClient = useUpdateOAuthClient();
  const deleteClient = useDeleteOAuthClient();

  const [dialogOpen, setDialogOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [form, setForm] = useState<OAuthClientFormState>(emptyForm());
  const [deleteTarget, setDeleteTarget] = useState<OAuthClient | null>(null);
  const [revealedSecret, setRevealedSecret] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const clients = data ?? [];

  function openCreate() {
    setEditingId(null);
    setForm(emptyForm());
    setDialogOpen(true);
  }

  function openEdit(c: OAuthClient) {
    setEditingId(c.client_id);
    setForm(clientToForm(c));
    setDialogOpen(true);
  }

  async function handleSave() {
    const redirectUris = form.redirect_uris
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean);

    try {
      if (editingId) {
        const payload: Partial<OAuthClient> & { clientId: string; regenerate_secret?: boolean } = {
          clientId: editingId,
          client_name: form.client_name,
          redirect_uris: redirectUris,
          grant_types: form.grant_types,
          response_types: form.response_types,
          scope: form.scope,
          token_endpoint_auth_method: form.token_endpoint_auth_method,
        };
        if (form.regenerate_secret) {
          (payload as Record<string, unknown>).regenerate_secret = true;
        }
        const result = await updateClient.mutateAsync(payload);
        if (result.client_secret) {
          setRevealedSecret(result.client_secret);
        }
        toast.success("Client updated");
        setDialogOpen(false);
      } else {
        const payload: Partial<OAuthClient> = {
          client_name: form.client_name,
          redirect_uris: redirectUris,
          grant_types: form.grant_types,
          response_types: form.response_types,
          scope: form.scope,
          token_endpoint_auth_method: form.token_endpoint_auth_method,
        };
        if (form.client_id.trim()) {
          payload.client_id = form.client_id.trim();
        }
        const result = await createClient.mutateAsync(payload);
        if (result.client_secret) {
          setRevealedSecret(result.client_secret);
        }
        setDialogOpen(false);
        toast.success("Client created");
      }
    } catch {
      toast.error("Failed to save OAuth client");
    }
  }

  async function handleDelete() {
    if (!deleteTarget) return;
    try {
      await deleteClient.mutateAsync(deleteTarget.client_id);
      toast.success("Client deleted");
    } catch {
      toast.error("Failed to delete OAuth client");
    }
    setDeleteTarget(null);
  }

  function handleCopy() {
    if (revealedSecret) {
      navigator.clipboard.writeText(revealedSecret);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }

  const isSaving = createClient.isPending || updateClient.isPending;

  const columns: Column<OAuthClient>[] = [
    {
      header: "Client ID",
      accessor: (c) => (
        <code className="text-xs text-muted-foreground font-mono">
          {c.client_id.length > 20 ? `${c.client_id.slice(0, 20)}…` : c.client_id}
        </code>
      ),
      className: "w-48",
    },
    {
      header: "Client Name",
      accessor: (c) => <span className="font-medium">{c.client_name}</span>,
    },
    {
      header: "Grant Types",
      accessor: (c) => (
        <div className="flex flex-wrap gap-1">
          {c.grant_types.map((g) => (
            <Badge key={g} variant="secondary" className="text-xs">
              {g.replace(/_/g, " ")}
            </Badge>
          ))}
        </div>
      ),
    },
    {
      header: "Redirect URIs",
      accessor: (c) => {
        const first = c.redirect_uris[0];
        const rest = c.redirect_uris.length - 1;
        if (!first) return <span className="text-muted-foreground">—</span>;
        return (
          <span className="text-xs font-mono">
            {first}
            {rest > 0 && (
              <Badge variant="outline" className="ml-1 text-xs">
                +{rest}
              </Badge>
            )}
          </span>
        );
      },
    },
    {
      header: "Scope",
      accessor: (c) => (
        <span className="text-xs text-muted-foreground font-mono">{c.scope}</span>
      ),
    },
    {
      header: "Auth Method",
      accessor: (c) => (
        <Badge variant="outline" className="text-xs">
          {c.token_endpoint_auth_method}
        </Badge>
      ),
    },
    {
      header: "",
      accessor: (c) => (
        <DropdownMenu>
          <DropdownMenuTrigger render={<Button variant="ghost" size="icon" className="h-8 w-8" />}>
            <MoreHorizontal className="h-4 w-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={() => openEdit(c)}>
              <Pencil className="mr-2 h-4 w-4" />
              Edit
            </DropdownMenuItem>
            <DropdownMenuItem
              className="text-destructive-foreground"
              onClick={() => setDeleteTarget(c)}
            >
              <Trash2 className="mr-2 h-4 w-4" />
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      ),
      className: "w-12",
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="OAuth Clients"
        description="Manage OAuth 2.0 client applications"
        actions={
          <Button onClick={openCreate}>
            <Plus className="mr-2 h-4 w-4" />
            Create Client
          </Button>
        }
      />

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <div className="flex items-center gap-2 p-6 text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading...
            </div>
          ) : isError ? (
            <div className="p-6 text-destructive">
              Failed to load OAuth clients. Make sure Hydra is configured and reachable.
            </div>
          ) : (
            <DataTable
              columns={columns}
              data={clients}
              keyFn={(c) => c.client_id}
              emptyMessage="No OAuth clients"
            />
          )}
        </CardContent>
      </Card>

      {/* Create / Edit Dialog */}
      <Dialog open={dialogOpen} onOpenChange={setDialogOpen}>
        <DialogContent className="sm:max-w-lg max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{editingId ? "Edit OAuth Client" : "Create OAuth Client"}</DialogTitle>
          </DialogHeader>

          <div className="space-y-5 py-4">
            <div className="space-y-2">
              <Label>Client Name <span className="text-destructive">*</span></Label>
              <Input
                value={form.client_name}
                onChange={(e) => setForm((p) => ({ ...p, client_name: e.target.value }))}
                placeholder="My Application"
              />
            </div>

            {!editingId && (
              <div className="space-y-2">
                <Label>
                  Client ID{" "}
                  <span className="text-muted-foreground text-xs">(leave blank to auto-generate)</span>
                </Label>
                <Input
                  value={form.client_id}
                  onChange={(e) => setForm((p) => ({ ...p, client_id: e.target.value }))}
                  placeholder="auto-generated"
                  className="font-mono"
                />
              </div>
            )}

            <div className="space-y-2">
              <Label>Redirect URIs <span className="text-muted-foreground text-xs">(one per line)</span></Label>
              <Textarea
                value={form.redirect_uris}
                onChange={(e) => setForm((p) => ({ ...p, redirect_uris: e.target.value }))}
                placeholder={"https://app.example.com/callback\nhttps://app.example.com/callback2"}
                rows={3}
                className="font-mono text-xs"
              />
            </div>

            <div className="space-y-2">
              <Label>Grant Types</Label>
              <div className="flex flex-wrap gap-3">
                {GRANT_TYPE_OPTIONS.map((opt) => (
                  <label key={opt.value} className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="h-4 w-4 rounded border"
                      checked={form.grant_types.includes(opt.value)}
                      onChange={() =>
                        setForm((p) => ({
                          ...p,
                          grant_types: toggleItem(p.grant_types, opt.value),
                        }))
                      }
                    />
                    <span className="text-sm">{opt.label}</span>
                  </label>
                ))}
              </div>
            </div>

            <div className="space-y-2">
              <Label>Response Types</Label>
              <div className="flex flex-wrap gap-3">
                {RESPONSE_TYPE_OPTIONS.map((opt) => (
                  <label key={opt.value} className="flex items-center gap-2 cursor-pointer">
                    <input
                      type="checkbox"
                      className="h-4 w-4 rounded border"
                      checked={form.response_types.includes(opt.value)}
                      onChange={() =>
                        setForm((p) => ({
                          ...p,
                          response_types: toggleItem(p.response_types, opt.value),
                        }))
                      }
                    />
                    <span className="text-sm">{opt.label}</span>
                  </label>
                ))}
              </div>
            </div>

            <div className="space-y-2">
              <Label>Scope</Label>
              <Input
                value={form.scope}
                onChange={(e) => setForm((p) => ({ ...p, scope: e.target.value }))}
                placeholder="project:inference offline_access"
              />
            </div>

            <div className="space-y-2">
              <Label>Token Endpoint Auth Method</Label>
              <Select
                value={form.token_endpoint_auth_method}
                onValueChange={(v) =>
                  setForm((p) => ({ ...p, token_endpoint_auth_method: v ?? p.token_endpoint_auth_method }))
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="client_secret_post">client_secret_post</SelectItem>
                  <SelectItem value="client_secret_basic">client_secret_basic</SelectItem>
                  <SelectItem value="none">none</SelectItem>
                </SelectContent>
              </Select>
            </div>

            {editingId && (
              <div className="space-y-2">
                <label className="flex items-center gap-2 cursor-pointer">
                  <input
                    type="checkbox"
                    className="h-4 w-4 rounded border"
                    checked={form.regenerate_secret}
                    onChange={(e) =>
                      setForm((p) => ({ ...p, regenerate_secret: e.target.checked }))
                    }
                  />
                  <span className="text-sm">Regenerate client secret</span>
                </label>
                {form.regenerate_secret && (
                  <p className="text-xs text-muted-foreground">
                    A new secret will be shown once after saving. The old secret will stop working immediately.
                  </p>
                )}
              </div>
            )}
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setDialogOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleSave} disabled={!form.client_name || isSaving}>
              {isSaving ? "Saving..." : editingId ? "Update" : "Create"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Secret Reveal Dialog */}
      <Dialog open={!!revealedSecret} onOpenChange={() => setRevealedSecret(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Client Secret</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <p className="text-sm text-muted-foreground">
              Copy your client secret now. You won&apos;t be able to see it again.
            </p>
            <div className="flex items-center gap-2">
              <code className="flex-1 rounded bg-muted px-3 py-2 text-sm font-mono break-all">
                {revealedSecret}
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
            <Button onClick={() => setRevealedSecret(null)}>Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation */}
      <Dialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete OAuth Client</DialogTitle>
            <DialogDescription>
              This will permanently delete the client &ldquo;{deleteTarget?.client_name}&rdquo;. All
              existing tokens issued to this client will stop working.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteTarget(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={handleDelete} disabled={deleteClient.isPending}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
