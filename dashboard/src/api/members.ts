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
      denied_models,
    }: {
      userId: string;
      role?: string;
      credit_quota_percent?: number;
      clear_quota?: boolean;
      // undefined  = leave unchanged
      // []         = clear the denylist
      // [...names] = replace
      denied_models?: string[];
    }) => {
      // Build body conditionally so undefined fields never serialize
      // as `null` (which the backend would reject for the wrong reason).
      const body: Record<string, unknown> = {};
      if (role !== undefined) body.role = role;
      if (credit_quota_percent !== undefined) body.credit_quota_percent = credit_quota_percent;
      if (clear_quota) body.clear_quota = clear_quota;
      if (denied_models !== undefined) body.denied_models = denied_models;
      return api.put(`/api/v1/projects/${projectId}/members/${userId}`, body);
    },
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
      api.delete<DataResponse<{ revoked_api_keys: number; deleted_oauth_grants: number }>>(
        `/api/v1/projects/${projectId}/members/${userId}`,
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["members", projectId] });
      qc.invalidateQueries({ queryKey: ["members-compact", projectId] });
    },
  });
}

// useMemberAffectedKeys returns the count of active API keys the member
// has in the project. Used by the Remove-member confirmation dialog so
// the operator sees the blast radius before clicking Confirm. Pass null
// for userId to disable the query.
export function useMemberAffectedKeys(projectId: string, userId: string | null) {
  return useQuery({
    queryKey: ["member-affected-keys", projectId, userId],
    queryFn: () =>
      api.get<DataResponse<{ active_api_keys: number }>>(
        `/api/v1/projects/${projectId}/members/${userId}/affected-keys`,
      ),
    enabled: !!userId,
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
