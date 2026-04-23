import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { DataResponse, ListResponse, Order } from "./types";

export interface ExtraUsageSettingsResponse {
  enabled: boolean;
  balance_fen: number;
  monthly_limit_fen: number;
  monthly_spent_fen: number;
  monthly_window_start: string;
  credit_price_fen: number;
  min_topup_fen: number;
  max_topup_fen: number;
  daily_topup_limit_fen: number;
  bypass_balance_check: boolean;
  updated_at?: string;
}

export interface ExtraUsageTransaction {
  id: string;
  project_id: string;
  type: "topup" | "deduction" | "refund" | "adjust";
  amount_fen: number;
  balance_after_fen: number;
  request_id?: string;
  order_id?: string;
  reason?: string;
  description?: string;
  created_at: string;
}

export interface UpdateExtraUsageInput {
  enabled?: boolean;
  monthly_limit_fen?: number;
}

export interface CreateTopupInput {
  amount_fen: number;
  channel: string;
}

export function useExtraUsage(projectId: string) {
  return useQuery({
    queryKey: ["extra-usage", projectId],
    queryFn: () =>
      api.get<DataResponse<ExtraUsageSettingsResponse>>(
        `/api/v1/projects/${projectId}/extra-usage`,
      ),
  });
}

export function useUpdateExtraUsage(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateExtraUsageInput) =>
      api.put<DataResponse<unknown>>(
        `/api/v1/projects/${projectId}/extra-usage`,
        body,
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["extra-usage", projectId] });
    },
  });
}

export function useExtraUsageTransactions(
  projectId: string,
  page = 1,
  perPage = 20,
  typeFilter = "",
) {
  const params = new URLSearchParams({
    page: String(page),
    per_page: String(perPage),
  });
  if (typeFilter) params.set("type", typeFilter);
  return useQuery({
    queryKey: ["extra-usage-transactions", projectId, page, perPage, typeFilter],
    queryFn: () =>
      api.get<ListResponse<ExtraUsageTransaction>>(
        `/api/v1/projects/${projectId}/extra-usage/transactions?${params}`,
      ),
  });
}

export function useCreateExtraUsageTopup(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateTopupInput) =>
      api.post<DataResponse<Order>>(
        `/api/v1/projects/${projectId}/extra-usage/topup`,
        body,
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["extra-usage", projectId] });
    },
  });
}

export function useExtraUsageTopupStatus(
  projectId: string,
  orderId: string | null,
) {
  return useQuery({
    queryKey: ["extra-usage-topup-status", projectId, orderId],
    queryFn: () =>
      api.get<DataResponse<Order>>(
        `/api/v1/projects/${projectId}/extra-usage/topup/${orderId}`,
      ),
    enabled: !!orderId,
    refetchInterval: 3000,
  });
}

// Admin-only: shape of a row returned by /admin/extra-usage/overview.
export interface AdminExtraUsageRow {
  project_id: string;
  enabled: boolean;
  balance_fen: number;
  monthly_limit_fen: number;
  bypass_balance_check: boolean;
  created_at: string;
  updated_at: string;
  spend_7d_fen: number;
}

export function useAdminExtraUsageOverview() {
  return useQuery({
    queryKey: ["admin", "extra-usage", "overview"],
    queryFn: () =>
      api.get<DataResponse<AdminExtraUsageRow[]>>(
        `/api/v1/admin/extra-usage/overview`,
      ),
  });
}

export function useSetExtraUsageBypass() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      projectId,
      bypass,
    }: {
      projectId: string;
      bypass: boolean;
    }) =>
      api.put<DataResponse<unknown>>(
        `/api/v1/admin/extra-usage/projects/${projectId}/bypass`,
        { bypass },
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "extra-usage", "overview"] });
    },
  });
}
