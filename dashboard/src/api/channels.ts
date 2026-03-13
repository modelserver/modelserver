import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, Channel, ChannelRoute, ChannelUsageSummary, ChannelTestResult } from "./types";

export function useChannels() {
  return useQuery({
    queryKey: ["admin", "channels"],
    queryFn: () => api.get<ListResponse<Channel>>("/api/v1/channels?per_page=100"),
  });
}

export function useChannelStats() {
  return useQuery({
    queryKey: ["admin", "channels", "stats"],
    queryFn: () => api.get<DataResponse<ChannelUsageSummary[]>>("/api/v1/channels/stats"),
  });
}

export function useTestChannel() {
  return useMutation({
    mutationFn: (channelId: string) =>
      api.post<DataResponse<ChannelTestResult>>(`/api/v1/channels/${channelId}/test`),
  });
}

export function useCreateChannel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: {
      provider: string;
      name: string;
      base_url: string;
      api_key: string;
      supported_models: string[];
      weight?: number;
      selection_priority?: number;
      max_concurrent?: number;
      test_model?: string;
    }) => api.post<DataResponse<Channel>>("/api/v1/channels", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "channels"] }),
  });
}

export function useUpdateChannel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      channelId,
      ...body
    }: {
      channelId: string;
      name?: string;
      base_url?: string;
      api_key?: string;
      supported_models?: string[];
      weight?: number;
      selection_priority?: number;
      status?: string;
      max_concurrent?: number;
      test_model?: string;
    }) => api.put<DataResponse<Channel>>(`/api/v1/channels/${channelId}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "channels"] }),
  });
}

export function useDeleteChannel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (channelId: string) =>
      api.delete(`/api/v1/channels/${channelId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "channels"] }),
  });
}

// --- Channel Routes ---

export function useChannelRoutes() {
  return useQuery({
    queryKey: ["admin", "routes"],
    queryFn: () => api.get<DataResponse<ChannelRoute[]>>("/api/v1/routes"),
  });
}

export function useCreateChannelRoute() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: {
      project_id?: string;
      model_pattern: string;
      channel_ids: string[];
      match_priority?: number;
      status?: string;
    }) => api.post<DataResponse<ChannelRoute>>("/api/v1/routes", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "routes"] }),
  });
}

export function useUpdateChannelRoute() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      routeId,
      ...body
    }: {
      routeId: string;
      model_pattern?: string;
      channel_ids?: string[];
      match_priority?: number;
      status?: string;
    }) => api.put(`/api/v1/routes/${routeId}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "routes"] }),
  });
}

export function useDeleteChannelRoute() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (routeId: string) => api.delete(`/api/v1/routes/${routeId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "routes"] }),
  });
}
