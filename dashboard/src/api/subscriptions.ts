import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "./client";
import type { DataResponse, ListResponse, Subscription, Order } from "./types";

export function useSubscriptions(projectId: string) {
  return useQuery({
    queryKey: ["subscriptions", projectId],
    queryFn: () =>
      api.get<DataResponse<Subscription[]>>(
        `/api/v1/projects/${projectId}/subscriptions`,
      ),
  });
}

export function useOrders(projectId: string, page = 1, perPage = 10) {
  return useQuery({
    queryKey: ["orders", projectId, page, perPage],
    queryFn: () =>
      api.get<ListResponse<Order>>(
        `/api/v1/projects/${projectId}/orders?page=${page}&per_page=${perPage}`,
      ),
  });
}

export interface CreateOrderInput {
  plan_slug: string;
  periods: number;
  channel: string;
}

export function useCreateOrder(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateOrderInput) =>
      api.post<DataResponse<Order>>(
        `/api/v1/projects/${projectId}/orders`,
        body,
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["orders", projectId] });
      qc.invalidateQueries({ queryKey: ["subscriptions", projectId] });
      qc.invalidateQueries({ queryKey: ["available-plans", projectId] });
    },
  });
}

export function useCancelOrder(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (orderId: string) =>
      api.post<DataResponse<Order>>(
        `/api/v1/projects/${projectId}/orders/${orderId}/cancel`,
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["orders", projectId] });
    },
  });
}

export interface CreditWindowStatus {
  window: string;
  percentage: number;
  resets_at?: string;
}

export function useSubscriptionUsage(projectId: string) {
  return useQuery({
    queryKey: ["subscription-usage", projectId],
    queryFn: () =>
      api.get<DataResponse<CreditWindowStatus[]>>(
        `/api/v1/projects/${projectId}/subscription/usage`,
      ),
  });
}
