import { useQuery } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, Request } from "./types";

export interface AdminRequestFilters {
  model?: string;
  status?: string;
  since?: string;
  until?: string;
  page?: number;
  per_page?: number;
}

export function useAdminRequests(filters: AdminRequestFilters = {}) {
  const params = new URLSearchParams();
  if (filters.model) params.set("model", filters.model);
  if (filters.status) params.set("status", filters.status);
  if (filters.since) params.set("since", filters.since);
  if (filters.until) params.set("until", filters.until);
  if (filters.page) params.set("page", String(filters.page));
  if (filters.per_page) params.set("per_page", String(filters.per_page));

  return useQuery({
    queryKey: ["admin", "requests", filters],
    queryFn: () =>
      api.get<ListResponse<Request>>(`/api/v1/admin/requests?${params}`),
  });
}
