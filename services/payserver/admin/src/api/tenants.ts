import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { adminFetch } from "./client";
import type { Tenant } from "./types";

export type CreateTenantInput = {
  name: string;
  callback_url: string;
  callback_secret: string;
  description: string;
};
export type CreateTenantResponse = { tenant: Tenant; secret: string };
export type RotateSecretResponse = { secret: string };

export function useTenants() {
  return useQuery({
    queryKey: ["tenants"],
    queryFn: () => adminFetch<{ items: Tenant[]; meta: { total: number } }>("/tenants"),
  });
}

export function useTenant(id: string) {
  return useQuery({
    queryKey: ["tenants", id],
    queryFn: () => adminFetch<{ tenant: Tenant }>(`/tenants/${id}`).then((r) => r.tenant),
    enabled: !!id,
  });
}

export function useCreateTenant() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateTenantInput) =>
      adminFetch<CreateTenantResponse>("/tenants", { method: "POST", body: JSON.stringify(input) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tenants"] }),
  });
}

export function useUpdateTenant() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: Partial<Tenant> & { callback_secret?: string } }) =>
      adminFetch<{ tenant: Tenant }>(`/tenants/${id}`, { method: "PATCH", body: JSON.stringify(patch) }),
    onSuccess: (_d, v) => {
      qc.invalidateQueries({ queryKey: ["tenants"] });
      qc.invalidateQueries({ queryKey: ["tenants", v.id] });
    },
  });
}

export function useDeleteTenant() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => adminFetch<{}>(`/tenants/${id}`, { method: "DELETE" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tenants"] }),
  });
}

export function useRotateSecret() {
  return useMutation({
    mutationFn: (id: string) =>
      adminFetch<RotateSecretResponse>(`/tenants/${id}/rotate-secret`, { method: "POST" }),
  });
}
