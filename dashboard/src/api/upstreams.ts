import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { DataResponse, Upstream, UpstreamGroupWithMembers, RoutingRoute, RoutingHealthResponse, UpstreamTestResult } from "./types";

// --- Upstreams ---
export function useUpstreams() {
  return useQuery({
    queryKey: ["admin", "upstreams"],
    queryFn: () => api.get<DataResponse<Upstream[]>>("/api/v1/upstreams"),
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

// --- Upstream Groups ---
export function useUpstreamGroups() {
  return useQuery({
    queryKey: ["admin", "upstream-groups"],
    queryFn: () => api.get<DataResponse<UpstreamGroupWithMembers[]>>("/api/v1/upstream-groups"),
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
export function useRoutingRoutes() {
  return useQuery({
    queryKey: ["admin", "routing-routes"],
    queryFn: () => api.get<DataResponse<RoutingRoute[]>>("/api/v1/routing/routes"),
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

export function useDeleteRoutingRoute() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete(`/api/v1/routing/routes/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "routing-routes"] }),
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
