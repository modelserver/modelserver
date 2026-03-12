import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, ProjectMember } from "./types";

export function useMembers(projectId: string) {
  return useQuery({
    queryKey: ["members", projectId],
    queryFn: () =>
      api.get<ListResponse<ProjectMember>>(
        `/api/v1/projects/${projectId}/members?per_page=100`,
      ),
  });
}

export function useAddMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { email: string; role: string }) =>
      api.post(`/api/v1/projects/${projectId}/members`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["members", projectId] }),
  });
}

export function useUpdateMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ userId, role }: { userId: string; role: string }) =>
      api.put(`/api/v1/projects/${projectId}/members/${userId}`, { role }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["members", projectId] }),
  });
}

export function useRemoveMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) =>
      api.delete(`/api/v1/projects/${projectId}/members/${userId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["members", projectId] }),
  });
}
