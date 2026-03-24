import { useState } from "react";
import { useCurrentProject } from "@/hooks/useCurrentProject";
import { useAuth } from "@/hooks/useAuth";
import { useMembers } from "@/api/members";
import { useOAuthGrants, useRevokeOAuthGrant } from "@/api/oauth-grants";
import { PageHeader } from "@/components/layout/PageHeader";
import { DataTable, type Column } from "@/components/shared/DataTable";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Avatar, AvatarImage, AvatarFallback } from "@/components/ui/avatar";
import type { OAuthGrant } from "@/api/types";

export function OAuthGrantsPage() {
  const projectId = useCurrentProject();
  const { user: currentUser } = useAuth();
  const { data, isLoading } = useOAuthGrants(projectId);
  const { data: membersData } = useMembers(projectId);
  const revokeGrant = useRevokeOAuthGrant(projectId);

  const [confirmGrant, setConfirmGrant] = useState<OAuthGrant | null>(null);

  const grants = data?.data ?? [];
  const members = membersData?.data ?? [];

  // Determine current user's role in this project
  const currentMember = members.find((m) => m.user_id === currentUser?.id);
  const currentRole = currentMember?.role;
  const canRevoke = currentRole === "owner" || currentRole === "maintainer";

  async function handleRevoke() {
    if (!confirmGrant) return;
    await revokeGrant.mutateAsync(confirmGrant.id);
    setConfirmGrant(null);
  }

  const columns: Column<OAuthGrant>[] = [
    {
      header: "Application",
      accessor: (g) => (
        <div>
          <span className="font-medium">{g.client_name || g.client_id}</span>
          {g.client_name && (
            <span className="block text-xs text-muted-foreground font-mono">{g.client_id}</span>
          )}
        </div>
      ),
    },
    {
      header: "Authorized By",
      accessor: (g) => {
        const name = g.user_nickname || g.user_id;
        const initials = name.slice(0, 2).toUpperCase();
        return (
          <div className="flex items-center gap-2">
            <Avatar size="sm">
              {g.user_picture && (
                <AvatarImage src={g.user_picture} alt={name} />
              )}
              <AvatarFallback>{initials}</AvatarFallback>
            </Avatar>
            <span>{name}</span>
          </div>
        );
      },
    },
    {
      header: "Scopes",
      accessor: (g) => (
        <div className="flex flex-wrap gap-1">
          {g.scopes.map((scope) => (
            <Badge key={scope} variant="outline">
              {scope}
            </Badge>
          ))}
        </div>
      ),
    },
    {
      header: "Authorized At",
      accessor: (g) => new Date(g.created_at).toLocaleDateString(),
    },
    {
      header: "",
      accessor: (g) =>
        canRevoke ? (
          <Button
            variant="ghost"
            size="sm"
            className="text-destructive hover:text-destructive"
            onClick={() => setConfirmGrant(g)}
          >
            Revoke
          </Button>
        ) : null,
      className: "w-24",
    },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Authorized Apps"
        description="External applications authorized to access this project"
      />

      <Card>
        <CardContent className="p-0">
          {isLoading ? (
            <p className="p-6 text-muted-foreground">Loading...</p>
          ) : (
            <DataTable
              columns={columns}
              data={grants}
              keyFn={(g) => g.id}
              emptyMessage="No authorized applications"
            />
          )}
        </CardContent>
      </Card>

      {/* Revoke Confirmation Dialog */}
      <Dialog open={!!confirmGrant} onOpenChange={() => setConfirmGrant(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Revoke Authorization</DialogTitle>
          </DialogHeader>
          <div className="py-4">
            <p className="text-sm text-muted-foreground">
              Are you sure you want to revoke access for{" "}
              <span className="font-mono font-medium text-foreground">
                {confirmGrant?.client_id}
              </span>
              ? This application will no longer be able to access this project.
            </p>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmGrant(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={handleRevoke}
              disabled={revokeGrant.isPending}
            >
              {revokeGrant.isPending ? "Revoking..." : "Revoke"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
