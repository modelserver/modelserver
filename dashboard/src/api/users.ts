import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, User } from "./types";

export function useUsers(page = 1, perPage = 20) {
  return useQuery({
    queryKey: ["admin", "users", page, perPage],
    queryFn: () => api.get<ListResponse<User>>(`/api/v1/users?page=${page}&per_page=${perPage}`),
  });
}

export function useUpdateUser() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      userId,
      ...body
    }: {
      userId: string;
      name?: string;
      status?: string;
      is_superadmin?: boolean;
      max_projects?: number;
    }) => api.put<DataResponse<User>>(`/api/v1/users/${userId}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "users"] }),
  });
}
