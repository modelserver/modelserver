import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { ListResponse, DataResponse, Plan, CreditRule, CreditRate, ClassicRule } from "./types";

export function usePlans(page = 1, perPage = 20) {
  return useQuery({
    queryKey: ["plans", page, perPage],
    queryFn: () => api.get<ListResponse<Plan>>(`/api/v1/plans?page=${page}&per_page=${perPage}`),
  });
}

export function useAvailablePlans(projectId: string) {
  return useQuery({
    queryKey: ["available-plans", projectId],
    queryFn: () =>
      api.get<DataResponse<Plan[]>>(
        `/api/v1/projects/${projectId}/available-plans`,
      ),
  });
}

export interface PlanCreateInput {
  name: string;
  slug: string;
  display_name: string;
  description?: string;
  tier_level: number;
  group_tag?: string;
  price_per_period: number;
  period_months: number;
  credit_rules?: CreditRule[];
  model_credit_rates?: Record<string, CreditRate>;
  classic_rules?: ClassicRule[];
}

export function useCreatePlan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: PlanCreateInput) =>
      api.post<DataResponse<Plan>>("/api/v1/plans", body),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["plans"] }),
  });
}

export interface PlanUpdateInput {
  planId: string;
  name?: string;
  slug?: string;
  display_name?: string;
  description?: string;
  tier_level?: number;
  group_tag?: string;
  price_per_period?: number;
  period_months?: number;
  credit_rules?: CreditRule[];
  model_credit_rates?: Record<string, CreditRate>;
  classic_rules?: ClassicRule[];
  is_active?: boolean;
}

export function useUpdatePlan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ planId, ...body }: PlanUpdateInput) =>
      api.put<DataResponse<Plan>>(`/api/v1/plans/${planId}`, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["plans"] });
      qc.invalidateQueries({ queryKey: ["available-plans"] });
    },
  });
}

export function useDeletePlan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (planId: string) => api.delete(`/api/v1/plans/${planId}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["plans"] });
      qc.invalidateQueries({ queryKey: ["available-plans"] });
    },
  });
}
