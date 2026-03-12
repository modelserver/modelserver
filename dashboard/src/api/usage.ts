import { useQuery } from "@tanstack/react-query";
import { api } from "./client";
import type { DataResponse, UsageOverview, UsageSummary, DailyUsage, UsageByKey } from "./types";

export function useUsageOverview(projectId: string, since?: string, until?: string) {
  const params = new URLSearchParams();
  if (since) params.set("since", since);
  if (until) params.set("until", until);
  const qs = params.toString();

  return useQuery({
    queryKey: ["usage", projectId, "overview", since, until],
    queryFn: () =>
      api.get<DataResponse<UsageOverview>>(
        `/api/v1/projects/${projectId}/usage${qs ? `?${qs}` : ""}`,
      ),
  });
}

export function useUsageByModel(projectId: string, since?: string, until?: string) {
  const params = new URLSearchParams({ breakdown: "model" });
  if (since) params.set("since", since);
  if (until) params.set("until", until);

  return useQuery({
    queryKey: ["usage", projectId, "model", since, until],
    queryFn: () =>
      api.get<DataResponse<UsageSummary[]>>(
        `/api/v1/projects/${projectId}/usage?${params}`,
      ),
  });
}

export function useDailyUsage(projectId: string, since?: string, until?: string) {
  const params = new URLSearchParams({ breakdown: "daily" });
  if (since) params.set("since", since);
  if (until) params.set("until", until);

  return useQuery({
    queryKey: ["usage", projectId, "daily", since, until],
    queryFn: () =>
      api.get<DataResponse<DailyUsage[]>>(
        `/api/v1/projects/${projectId}/usage?${params}`,
      ),
  });
}

export function useUsageByKey(projectId: string, since?: string, until?: string) {
  const params = new URLSearchParams({ breakdown: "key" });
  if (since) params.set("since", since);
  if (until) params.set("until", until);

  return useQuery({
    queryKey: ["usage", projectId, "key", since, until],
    queryFn: () =>
      api.get<DataResponse<UsageByKey[]>>(
        `/api/v1/projects/${projectId}/usage?${params}`,
      ),
  });
}
