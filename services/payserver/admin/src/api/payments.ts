export { adminFetch } from "./client";
export type { Payment } from "./types";
export type ListPaymentsParams = { tenant_id?: string; status?: string; channel?: string; limit?: number; offset?: number };
