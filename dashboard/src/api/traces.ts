import { useQuery } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, Trace, Request } from "./types";

export function useTraces(projectId: string, page = 1, perPage = 20) {
  return useQuery({
    queryKey: ["traces", projectId, page],
    queryFn: () =>
      api.get<ListResponse<Trace>>(
        `/api/v1/projects/${projectId}/traces?page=${page}&per_page=${perPage}`,
      ),
  });
}

export function useTrace(traceId: string) {
  return useQuery({
    queryKey: ["trace", traceId],
    queryFn: () => api.get<DataResponse<Trace>>(`/api/v1/traces/${traceId}`),
    enabled: !!traceId,
  });
}

export function useTraceRequests(projectId: string, traceId: string) {
  return useQuery({
    queryKey: ["trace-requests", projectId, traceId],
    queryFn: () =>
      api.get<DataResponse<Request[]>>(
        `/api/v1/projects/${projectId}/traces/${traceId}/requests`,
      ),
    enabled: !!traceId,
  });
}
