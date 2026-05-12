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

export interface MemberCompact {
  user_id: string;
  nickname?: string;
}

// useMembersCompact returns every project member in a single request as
// minimal {user_id, nickname}. Use it for filter dropdowns; the paginated
// useMembers caps at the per_page passed in and would miss members on
// projects with more rows than that.
export function useMembersCompact(projectId: string) {
  return useQuery({
    queryKey: ["members-compact", projectId],
    queryFn: () =>
      api.get<DataResponse<MemberCompact[]>>(
        `/api/v1/projects/${projectId}/members/compact`,
      ),
    enabled: !!projectId,
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
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["members", projectId] });
      qc.invalidateQueries({ queryKey: ["members-compact", projectId] });
    },
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
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["members", projectId] });
      qc.invalidateQueries({ queryKey: ["members-compact", projectId] });
    },
  });
}

export function useRemoveMember(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (userId: string) =>
      api.delete(`/api/v1/projects/${projectId}/members/${userId}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["members", projectId] });
      qc.invalidateQueries({ queryKey: ["members-compact", projectId] });
    },
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

export function useMyMembership(projectId: string) {
  return useQuery({
    queryKey: ["my-membership", projectId],
    queryFn: () =>
      api.get<DataResponse<ProjectMember>>(
        `/api/v1/projects/${projectId}/my-membership`,
      ),
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
