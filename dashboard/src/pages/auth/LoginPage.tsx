import { useAuthConfig } from "@/api/auth";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Loader2 } from "lucide-react";

const DEFAULT_OAUTH_LABELS: Record<string, string> = {
  github: "GitHub",
  google: "Google",
  oidc: "SSO (OIDC)",
};

const DEFAULT_DESCRIPTION = "Sign in to your ModelServer account";

export function LoginPage() {
  const { data: authConfig, isLoading: configLoading } = useAuthConfig();

  const oauthProviders = authConfig?.oauth_providers ?? [];
  const oauthLabels = { ...DEFAULT_OAUTH_LABELS, ...authConfig?.oauth_labels };
  const description = authConfig?.login_description || DEFAULT_DESCRIPTION;

  function handleOAuth(provider: string) {
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
          <CardDescription>{description}</CardDescription>
        </CardHeader>
        <CardContent>
          {oauthProviders.length > 0 ? (
            <div className="space-y-2">
              {oauthProviders.map((provider) => (
                <Button
                  key={provider}
                  variant="outline"
                  className="w-full"
                  onClick={() => handleOAuth(provider)}
                >
                  Continue with {oauthLabels[provider] ?? provider}
                </Button>
              ))}
            </div>
          ) : (
            <p className="text-center text-sm text-muted-foreground">
              No authentication providers configured.
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
