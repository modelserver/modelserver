import { useEffect } from "react";

export function ClaudeCodeOAuthCallback() {
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const code = params.get("code");
    const state = params.get("state");

    if (window.opener) {
      window.opener.postMessage(
        { type: "claudecode-oauth-callback", code, state },
        window.location.origin,
      );
    }
    window.close();
  }, []);

  return (
    <div className="flex items-center justify-center h-screen text-muted-foreground">
      Completing authorization...
    </div>
  );
}
