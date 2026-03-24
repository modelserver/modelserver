import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { DataResponse, OAuthGrant } from "./types";

export function useOAuthGrants(projectId: string) {
  return useQuery({
    queryKey: ["oauth-grants", projectId],
    queryFn: () =>
      api.get<DataResponse<OAuthGrant[]>>(`/api/v1/projects/${projectId}/oauth-grants`),
  });
}

export function useRevokeOAuthGrant(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (grantId: string) =>
      api.delete(`/api/v1/projects/${projectId}/oauth-grants/${grantId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["oauth-grants", projectId] }),
  });
}
