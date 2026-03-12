import { useState, type FormEvent } from "react";
import { useAuth } from "@/hooks/useAuth";
import { api } from "@/api/client";
import { PageHeader } from "@/components/layout/PageHeader";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { APIError } from "@/api/client";

export function UserSettingsPage() {
  const { user } = useAuth();
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [pwMsg, setPwMsg] = useState("");
  const [pwError, setPwError] = useState("");
  const [changingPw, setChangingPw] = useState(false);

  async function handleChangePassword(e: FormEvent) {
    e.preventDefault();
    setPwMsg("");
    setPwError("");
    setChangingPw(true);
    try {
      await api.patch("/api/v1/auth/password", {
        current_password: currentPassword,
        new_password: newPassword,
      });
      setPwMsg("Password updated");
      setCurrentPassword("");
      setNewPassword("");
    } catch (err) {
      setPwError(err instanceof APIError ? err.message : "Failed to update password");
    } finally {
      setChangingPw(false);
    }
  }

  return (
    <div className="space-y-6 max-w-lg">
      <PageHeader title="Account Settings" />

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Profile</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <div>
            <Label className="text-xs text-muted-foreground">Name</Label>
            <p className="text-sm">{user?.name || "—"}</p>
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

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Change Password</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleChangePassword} className="space-y-4">
            {pwError && (
              <div className="rounded-md bg-destructive/10 p-3 text-sm text-destructive-foreground">
                {pwError}
              </div>
            )}
            {pwMsg && (
              <div className="rounded-md bg-emerald-500/10 p-3 text-sm text-emerald-500">
                {pwMsg}
              </div>
            )}
            <div className="space-y-2">
              <Label htmlFor="current-pw">Current Password</Label>
              <Input
                id="current-pw"
                type="password"
                value={currentPassword}
                onChange={(e) => setCurrentPassword(e.target.value)}
              />
            </div>
            <Separator />
            <div className="space-y-2">
              <Label htmlFor="new-pw">New Password</Label>
              <Input
                id="new-pw"
                type="password"
                value={newPassword}
                onChange={(e) => setNewPassword(e.target.value)}
                minLength={8}
                required
              />
            </div>
            <Button type="submit" disabled={changingPw || !newPassword}>
              {changingPw ? "Updating..." : "Update Password"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
