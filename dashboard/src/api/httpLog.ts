import { useQuery } from "@tanstack/react-query";
import { api } from "./client";
import type { HttpLogDocument } from "./types";

export function useHttpLog(
  projectId: string,
  requestId: string | undefined,
  enabled: boolean,
) {
  return useQuery({
    queryKey: ["httpLog", projectId, requestId],
    queryFn: () =>
      api.get<HttpLogDocument>(
        `/api/v1/projects/${projectId}/requests/${requestId}/http-log`,
      ),
    enabled: !!requestId && enabled,
  });
}

export function useAdminHttpLog(
  requestId: string | undefined,
  enabled: boolean,
) {
  return useQuery({
    queryKey: ["adminHttpLog", requestId],
    queryFn: () =>
      api.get<HttpLogDocument>(
        `/api/v1/admin/requests/${requestId}/http-log`,
      ),
    enabled: !!requestId && enabled,
  });
}
