import type { Tenant } from "./types";
export { adminFetch } from "./client";
export type { Tenant };
export type CreateTenantInput = { name: string; callback_url: string; callback_secret: string; description: string };
export type CreateTenantResponse = { tenant: Tenant; secret: string };
