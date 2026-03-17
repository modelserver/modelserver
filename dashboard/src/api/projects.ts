import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, Project } from "./types";

export function useProjects() {
  return useQuery({
    queryKey: ["projects"],
    queryFn: () => api.get<ListResponse<Project>>("/api/v1/projects?per_page=100"),
  });
}

export function useAllProjects() {
  return useQuery({
    queryKey: ["admin-projects"],
    queryFn: () => api.get<ListResponse<Project>>("/api/v1/admin/projects?per_page=100"),
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
