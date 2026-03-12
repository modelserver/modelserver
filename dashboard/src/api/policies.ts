import { useQuery } from "@tanstack/react-query";
import { api } from "./client";
import type { DataResponse, RateLimitPolicy } from "./types";

export function usePolicies(projectId: string) {
  return useQuery({
    queryKey: ["policies", projectId],
    queryFn: () =>
      api.get<DataResponse<RateLimitPolicy[]>>(
        `/api/v1/projects/${projectId}/policies`,
      ),
  });
}
