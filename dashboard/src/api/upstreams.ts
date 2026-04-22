import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, Upstream, UpstreamGroupWithMembers, RoutingRoute, RoutingHealthResponse, UpstreamTestResult, UpstreamUsageSummary, ClaudeCodeUtilization } from "./types";

// --- Upstreams ---
export function useUpstreams(page = 1, perPage = 20) {
  return useQuery({
    queryKey: ["admin", "upstreams", page, perPage],
    queryFn: () => api.get<ListResponse<Upstream>>(`/api/v1/upstreams?page=${page}&per_page=${perPage}`),
  });
}

export function useCreateUpstream() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Partial<Upstream> & { api_key: string }) =>
      api.post<DataResponse<Upstream>>("/api/v1/upstreams", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "upstreams"] }),
  });
}

export function useUpdateUpstream() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: { id: string } & Record<string, unknown>) =>
      api.put<DataResponse<Upstream>>(`/api/v1/upstreams/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "upstreams"] }),
  });
}

export function useDeleteUpstream() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete(`/api/v1/upstreams/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "upstreams"] }),
  });
}

export function useTestUpstream() {
  return useMutation({
    mutationFn: (upstreamId: string) =>
      api.post<DataResponse<UpstreamTestResult>>(`/api/v1/upstreams/${upstreamId}/test`),
  });
}

export function useUpstreamUsage() {
  return useQuery({
    queryKey: ["admin", "upstream-usage"],
    queryFn: () =>
      api.get<DataResponse<(UpstreamUsageSummary & { credits_5h: number; credits_7d: number })[]>>("/api/v1/upstreams/usage"),
  });
}

export function useClaudeCodeUtilization(upstreamId: string | undefined) {
  return useQuery({
    queryKey: ["admin", "upstreams", upstreamId, "utilization"],
    queryFn: () =>
      api.get<DataResponse<ClaudeCodeUtilization>>(
        `/api/v1/upstreams/${upstreamId}/utilization`,
      ),
    enabled: !!upstreamId,
    refetchInterval: 60_000,
  });
}

// --- Claude Code OAuth ---
export function useClaudeCodeOAuthStart() {
  return useMutation({
    mutationFn: (body?: { redirect_uri?: string }) =>
      api.post<DataResponse<{
        auth_url: string;
        state: string;
        code_verifier: string;
        redirect_uri: string;
      }>>("/api/v1/upstreams/claudecode/oauth/start", body ?? {}),
  });
}

export function useClaudeCodeOAuthExchange() {
  return useMutation({
    mutationFn: (body: {
      callback_url: string;
      code_verifier: string;
      state: string;
      redirect_uri: string;
    }) =>
      api.post<DataResponse<{
        access_token: string;
        refresh_token: string;
        expires_at: number;
        client_id: string;
      }>>("/api/v1/upstreams/claudecode/oauth/exchange", body),
  });
}

export function useUpstreamOAuthStatus(upstreamId: string | undefined) {
  return useQuery({
    queryKey: ["admin", "upstreams", upstreamId, "oauth-status"],
    queryFn: () =>
      api.get<DataResponse<{ expires_at: number; has_refresh_token: boolean }>>(
        `/api/v1/upstreams/${upstreamId}/oauth/status`,
      ),
    enabled: !!upstreamId,
    refetchInterval: 60_000,
  });
}

export function useUpstreamOAuthRefresh() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (upstreamId: string) =>
      api.post<DataResponse<{ expires_at: number; has_refresh_token: boolean }>>(
        `/api/v1/upstreams/${upstreamId}/oauth/refresh`,
      ),
    onSuccess: (_, upstreamId) => {
      qc.invalidateQueries({ queryKey: ["admin", "upstreams", upstreamId, "oauth-status"] });
    },
  });
}


// --- Upstream Groups ---
export function useUpstreamGroups(page = 1, perPage = 20) {
  return useQuery({
    queryKey: ["admin", "upstream-groups", page, perPage],
    queryFn: () => api.get<ListResponse<UpstreamGroupWithMembers>>(`/api/v1/upstream-groups?page=${page}&per_page=${perPage}`),
  });
}

export function useCreateUpstreamGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { name: string; lb_policy: string; retry_policy?: unknown; status?: string }) =>
      api.post<DataResponse<UpstreamGroupWithMembers>>("/api/v1/upstream-groups", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "upstream-groups"] }),
  });
}

export function useDeleteUpstreamGroup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete(`/api/v1/upstream-groups/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "upstream-groups"] }),
  });
}

export function useAddGroupMember() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ groupId, ...body }: { groupId: string; upstream_id: string; weight?: number; is_backup?: boolean }) =>
      api.post(`/api/v1/upstream-groups/${groupId}/members`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "upstream-groups"] }),
  });
}

export function useRemoveGroupMember() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ groupId, upstreamId }: { groupId: string; upstreamId: string }) =>
      api.delete(`/api/v1/upstream-groups/${groupId}/members/${upstreamId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "upstream-groups"] }),
  });
}

// --- Routing Routes ---
export function useRoutingRoutes(page = 1, perPage = 20) {
  return useQuery({
    queryKey: ["admin", "routing-routes", page, perPage],
    queryFn: () => api.get<ListResponse<RoutingRoute>>(`/api/v1/routing/routes?page=${page}&per_page=${perPage}`),
  });
}

export function useCreateRoutingRoute() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Partial<RoutingRoute>) =>
      api.post<DataResponse<RoutingRoute>>("/api/v1/routing/routes", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "routing-routes"] }),
  });
}

export function useUpdateRoutingRoute() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: Partial<RoutingRoute> & { id: string }) =>
      api.put<DataResponse<RoutingRoute>>(`/api/v1/routing/routes/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "routing-routes"] }),
  });
}

export function useDeleteRoutingRoute() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete(`/api/v1/routing/routes/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "routing-routes"] }),
  });
}

// useRequestKinds returns the catalog of valid route request_kinds, fetched
// from the server so the enum stays out of the frontend bundle.
export function useRequestKinds() {
  return useQuery({
    queryKey: ["admin", "routing-request-kinds"],
    queryFn: () => api.get<DataResponse<string[]>>("/api/v1/routing/request-kinds"),
    staleTime: 60 * 60 * 1000,
  });
}

// --- Routing Health ---
export function useRoutingHealth() {
  return useQuery({
    queryKey: ["admin", "routing-health"],
    queryFn: () => api.get<DataResponse<RoutingHealthResponse>>("/api/v1/routing/health"),
    refetchInterval: 10_000, // Poll every 10s
  });
}
