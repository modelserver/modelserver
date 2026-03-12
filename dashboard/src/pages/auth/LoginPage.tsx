import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { toast } from "sonner";
import { useAuth } from "@/hooks/useAuth";
import { useAuthConfig } from "@/api/auth";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { APIError } from "@/api/client";
import { Loader2 } from "lucide-react";

const OAUTH_LABELS: Record<string, string> = {
  github: "GitHub",
  google: "Google",
  oidc: "SSO (OIDC)",
};

export function LoginPage() {
  const { login } = useAuth();
  const navigate = useNavigate();
  const { data: authConfig, isLoading: configLoading } = useAuthConfig();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const passwordEnabled = authConfig?.password_login_enabled ?? true;
  const oauthProviders = authConfig?.oauth_providers ?? [];
  const allowRegistration = authConfig?.allow_registration ?? true;

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError("");
    setLoading(true);
    try {
      await login(email, password);
      navigate("/");
    } catch (err) {
      const msg = err instanceof APIError ? err.message : "Login failed";
      setError(msg);
      toast.error(msg);
    } finally {
      setLoading(false);
    }
  }

  async function handleOAuth(provider: string) {
    const apiBase = import.meta.env.VITE_API_BASE_URL || "";
    window.location.href = `${apiBase}/api/v1/auth/oauth/${provider}/redirect?provider=${provider}`;
  }

  if (configLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center p-4">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle className="text-2xl">Sign In</CardTitle>
          <CardDescription>Sign in to your ModelServer account</CardDescription>
        </CardHeader>
        <CardContent>
          {passwordEnabled && (
            <form onSubmit={handleSubmit} className="space-y-4">
              {error && (
                <div className="rounded-md bg-destructive/10 p-3 text-sm text-destructive-foreground">
                  {error}
                </div>
              )}
              <div className="space-y-2">
                <Label htmlFor="email">Email</Label>
                <Input
                  id="email"
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="you@example.com"
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="password">Password</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  required
                />
              </div>
              <Button type="submit" className="w-full" disabled={loading}>
                {loading ? "Signing in..." : "Sign In"}
              </Button>
            </form>
          )}

          {oauthProviders.length > 0 && (
            <div className={passwordEnabled ? "mt-4 space-y-2" : "space-y-2"}>
              {passwordEnabled && (
                <div className="relative">
                  <div className="absolute inset-0 flex items-center">
                    <span className="w-full border-t" />
                  </div>
                  <div className="relative flex justify-center text-xs uppercase">
                    <span className="bg-card px-2 text-muted-foreground">or</span>
                  </div>
                </div>
              )}
              {oauthProviders.map((provider) => (
                <Button
                  key={provider}
                  variant="outline"
                  className="w-full"
                  onClick={() => handleOAuth(provider)}
                >
                  Continue with {OAUTH_LABELS[provider] ?? provider}
                </Button>
              ))}
            </div>
          )}

          {passwordEnabled && allowRegistration && (
            <p className="mt-4 text-center text-sm text-muted-foreground">
              Don't have an account?{" "}
              <Link to="/register" className="text-primary underline-offset-4 hover:underline">
                Sign up
              </Link>
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
