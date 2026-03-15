import { useQuery } from "@tanstack/react-query";
import { api } from "./client";

export interface AuthConfig {
  oauth_providers: string[];
  login_description?: string;
  oauth_labels?: Record<string, string>;
}

export function useAuthConfig() {
  return useQuery({
    queryKey: ["auth-config"],
    queryFn: () => api.get<AuthConfig>("/api/v1/auth/config"),
    staleTime: 5 * 60 * 1000,
  });
}
