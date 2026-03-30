import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, ProjectMember, QuotaUsageResponse, MemberUsage } from "./types";

export function useMembers(projectId: string, page = 1, perPage = 20) {
  return useQuery({
    queryKey: ["members", projectId, page, perPage],
    queryFn: () =>
      api.get<ListResponse<ProjectMember>>(
        `/api/v1/projects/${projectId}/members?page=${page}&per_page=${perPage}`,
      ),
  });
}

export function useMembersUsage(projectId: string, userIds: string[]) {
  return useQuery({
    queryKey: ["members-usage", projectId, userIds],
    queryFn: () =>
      api.get<DataResponse<MemberUsage[]>>(
        `/api/v1/projects/${projectId}/members/usage?user_ids=${userIds.join(",")}`,
      ),
    enabled: userIds.length > 0,
  });
}

export function useAddMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { email: string; role: string; credit_quota_percent?: number }) =>
      api.post(`/api/v1/projects/${projectId}/members`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["members", projectId] }),
  });
}

export function useUpdateMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      userId,
      role,
      credit_quota_percent,
      clear_quota,
    }: {
      userId: string;
      role?: string;
      credit_quota_percent?: number;
      clear_quota?: boolean;
    }) =>
      api.put(`/api/v1/projects/${projectId}/members/${userId}`, {
        role,
        credit_quota_percent,
        clear_quota,
      }),
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

export function useQuotaUsage(projectId: string, userId: string) {
  return useQuery({
    queryKey: ["quota-usage", projectId, userId],
    queryFn: () =>
      api.get<DataResponse<QuotaUsageResponse>>(
        `/api/v1/projects/${projectId}/members/${userId}/quota-usage`,
      ),
    enabled: !!userId,
  });
}

export function useMyQuota(projectId: string) {
  return useQuery({
    queryKey: ["my-quota", projectId],
    queryFn: () =>
      api.get<DataResponse<QuotaUsageResponse>>(
        `/api/v1/projects/${projectId}/my-quota`,
      ),
  });
}
