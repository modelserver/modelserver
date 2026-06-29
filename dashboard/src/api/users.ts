import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, User } from "./types";

export function useUsers(page = 1, perPage = 20) {
  return useQuery({
    queryKey: ["admin", "users", page, perPage],
    queryFn: () => api.get<ListResponse<User>>(`/api/v1/users?page=${page}&per_page=${perPage}`),
  });
}

export interface UserCompact {
  id: string;
  nickname?: string;
  email?: string;
  picture?: string;
}

// useAllUsersCompact returns every user (superadmin only) in a single
// lightweight request — used to populate filter dropdowns without the
// pagination cap that miss users outside the first page.
export function useAllUsersCompact() {
  return useQuery({
    queryKey: ["admin", "users-compact"],
    queryFn: () => api.get<DataResponse<UserCompact[]>>(`/api/v1/users/compact`),
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
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      qc.invalidateQueries({ queryKey: ["admin", "users-compact"] });
    },
  });
}
