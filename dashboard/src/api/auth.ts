import { useQuery } from "@tanstack/react-query";
import { api } from "./client";

export interface AuthConfig {
  password_login_enabled: boolean;
  allow_registration: boolean;
  oauth_providers: string[];
}

export function useAuthConfig() {
  return useQuery({
    queryKey: ["auth-config"],
    queryFn: () => api.get<AuthConfig>("/api/v1/auth/config"),
    staleTime: 5 * 60 * 1000,
  });
}
