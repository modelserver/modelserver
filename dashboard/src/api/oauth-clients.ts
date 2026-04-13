import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";

export interface OAuthClient {
  client_id: string;
  client_name: string;
  client_secret?: string;
  redirect_uris: string[];
  grant_types: string[];
  response_types: string[];
  scope: string;
  token_endpoint_auth_method: string;
  created_at?: string;
  updated_at?: string;
}

export function useOAuthClients() {
  return useQuery({
    queryKey: ["oauth-clients"],
    queryFn: () => api.get<OAuthClient[]>("/api/v1/oauth-clients"),
  });
}

export function useCreateOAuthClient() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Partial<OAuthClient>) =>
      api.post<OAuthClient>("/api/v1/oauth-clients", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["oauth-clients"] }),
  });
}

export function useUpdateOAuthClient() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ clientId, ...body }: Partial<OAuthClient> & { clientId: string }) =>
      api.put<OAuthClient>(`/api/v1/oauth-clients/${clientId}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["oauth-clients"] }),
  });
}

export function useDeleteOAuthClient() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (clientId: string) =>
      api.delete(`/api/v1/oauth-clients/${clientId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["oauth-clients"] }),
  });
}
