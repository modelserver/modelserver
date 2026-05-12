import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, Project } from "./types";
import type { CreditWindowStatus } from "./subscriptions";

export interface ProjectOwnerSnapshot {
  id: string;
  email?: string;
  nickname?: string;
  picture?: string;
}

export interface ProjectSubscriptionOverview {
  project_id: string;
  plan_id?: string;
  plan_name?: string;
  display_name?: string;
  windows: CreditWindowStatus[];
  owner?: ProjectOwnerSnapshot;
  /** Credits consumed since the active subscription's StartsAt, in integer K. Absent when there is no active subscription. */
  period_credits_k?: number;
}

export function useAdminProjectsSubscriptionsOverview(projectIds: string[]) {
  const ids = [...projectIds].sort().join(",");
  return useQuery({
    queryKey: ["admin-projects-subscriptions-overview", ids],
    queryFn: () =>
      api.get<DataResponse<ProjectSubscriptionOverview[]>>(
        `/api/v1/admin/projects/subscriptions-overview?project_ids=${encodeURIComponent(ids)}`,
      ),
    enabled: projectIds.length > 0,
  });
}

export function useProjects() {
  return useQuery({
    queryKey: ["projects"],
    queryFn: () => api.get<ListResponse<Project>>("/api/v1/projects?per_page=100"),
  });
}

export function useAllProjects(page = 1, perPage = 20) {
  return useQuery({
    queryKey: ["admin-projects", page, perPage],
    queryFn: () => api.get<ListResponse<Project>>(`/api/v1/admin/projects?page=${page}&per_page=${perPage}`),
  });
}

export function useProject(projectId: string) {
  return useQuery({
    queryKey: ["project", projectId],
    queryFn: () => api.get<DataResponse<Project>>(`/api/v1/projects/${projectId}`),
  });
}

export function useCreateProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { name: string; description?: string }) =>
      api.post<DataResponse<Project>>("/api/v1/projects", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["projects"] }),
  });
}

export function useUpdateProject(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { name?: string; description?: string }) =>
      api.put<DataResponse<Project>>(`/api/v1/projects/${projectId}`, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects"] });
      qc.invalidateQueries({ queryKey: ["project", projectId] });
    },
  });
}

export function useArchiveProject(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<DataResponse<Project>>(`/api/v1/projects/${projectId}/archive`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects"] });
      qc.invalidateQueries({ queryKey: ["project", projectId] });
    },
  });
}

export function useUnarchiveProject(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<DataResponse<Project>>(`/api/v1/projects/${projectId}/unarchive`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects"] });
      qc.invalidateQueries({ queryKey: ["project", projectId] });
    },
  });
}
