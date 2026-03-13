import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, APIKey } from "./types";

export function useKeys(projectId: string) {
  return useQuery({
    queryKey: ["keys", projectId],
    queryFn: () =>
      api.get<ListResponse<APIKey>>(`/api/v1/projects/${projectId}/keys?per_page=100`),
  });
}

export function useCreateKey(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: {
      name: string;
      description?: string;
      allowed_models?: string[];
      expires_at?: string;
    }) => api.post<{ data: APIKey; key: string }>(`/api/v1/projects/${projectId}/keys`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["keys", projectId] }),
  });
}

export function useUpdateKey(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      keyId,
      ...body
    }: {
      keyId: string;
      name?: string;
      status?: string;
      description?: string;
    }) => api.put<DataResponse<APIKey>>(`/api/v1/projects/${projectId}/keys/${keyId}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["keys", projectId] }),
  });
}

export function useDeleteKey(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (keyId: string) =>
      api.delete(`/api/v1/projects/${projectId}/keys/${keyId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["keys", projectId] }),
  });
}
