import { useAuth } from "@/hooks/useAuth";
import { PageHeader } from "@/components/layout/PageHeader";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export function UserSettingsPage() {
  const { user } = useAuth();

  return (
    <div className="space-y-6 max-w-lg">
      <PageHeader title="Account Settings" />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Profile</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div>
            <Label className="text-xs text-muted-foreground">Nickname</Label>
            <p className="text-sm">{user?.nickname || "—"}</p>
          </div>
          <div>
            <Label className="text-xs text-muted-foreground">Email</Label>
            <p className="text-sm">{user?.email}</p>
          </div>
          <div>
            <Label className="text-xs text-muted-foreground">Role</Label>
            <p className="text-sm">{user?.is_superadmin ? "Superadmin" : "User"}</p>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
