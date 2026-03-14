// API types matching backend Go structs in internal/types/

// --- Envelope types ---
export interface Meta {
  total: number;
  page: number;
  per_page: number;
  total_pages: number;
}

export interface ListResponse<T> {
  data: T[];
  meta: Meta;
}

export interface DataResponse<T> {
  data: T;
}

export interface ErrorDetail {
  code: string;
  message: string;
  details?: unknown;
}

export interface ErrorResponse {
  error: ErrorDetail;
}

// --- Auth ---
export interface AuthResponse {
  access_token: string;
  refresh_token: string;
  user: User;
}

// --- User ---
export interface User {
  id: string;
  email: string;
  nickname: string;
  picture?: string;
  is_superadmin: boolean;
  max_projects: number;
  status: "active" | "disabled";
  created_at: string;
  updated_at: string;
}

// --- Project ---
export interface Project {
  id: string;
  name: string;
  description?: string;
  created_by: string;
  status: "active" | "suspended";
  settings?: Record<string, unknown>;
  billing_tags?: string[];
  created_at: string;
  updated_at: string;
}

// --- Project Member ---
export interface ProjectMember {
  user_id: string;
  project_id: string;
  role: "owner" | "maintainer" | "developer";
  created_at: string;
  user?: User;
}

// --- API Key ---
export interface APIKey {
  id: string;
  project_id: string;
  created_by: string;
  key_suffix: string;
  name: string;
  description?: string;
  status: "active" | "disabled" | "revoked";
  allowed_models?: string[];
  expires_at?: string;
  last_used_at?: string;
  created_at: string;
  updated_at: string;
  request_count: number;
  total_tokens: number;
}

// Key creation returns the full plaintext key once
export interface APIKeyCreateResponse {
  id: string;
  key: string;
  name: string;
  key_suffix: string;
  project_id: string;
  status: string;
  created_at: string;
}

// --- Channel ---
export interface Channel {
  id: string;
  provider: "anthropic" | "openai" | "gemini" | "bedrock";
  name: string;
  base_url: string;
  supported_models: string[];
  weight: number;
  selection_priority: number;
  status: "active" | "disabled";
  max_concurrent: number;
  test_model?: string;
  created_at: string;
  updated_at: string;
}

export interface ChannelUsageSummary {
  channel_id: string;
  request_count: number;
  input_tokens: number;
  output_tokens: number;
  total_credits: number;
  avg_latency_ms: number;
  success_count: number;
  error_count: number;
}

export interface ChannelTestResult {
  success: boolean;
  status_code?: number;
  latency_ms?: number;
  model?: string;
  error?: string;
}

// --- Channel Route ---
export interface ChannelRoute {
  id: string;
  project_id?: string;
  model_pattern: string;
  channel_ids: string[];
  match_priority: number;
  status: string;
  created_at: string;
  updated_at: string;
}

// --- Request ---
export interface Request {
  id: string;
  project_id: string;
  api_key_id: string;
  channel_id: string;
  trace_id?: string;
  msg_id?: string;
  provider: string;
  model: string;
  streaming: boolean;
  status: "success" | "error" | "rate_limited";
  input_tokens: number;
  output_tokens: number;
  cache_creation_tokens: number;
  cache_read_tokens: number;
  latency_ms: number;
  ttft_ms: number;
  error_message?: string;
  client_ip?: string;
  created_at: string;
}

// --- Rate Limit Policy ---
export interface CreditRule {
  window: string;
  window_type: "sliding" | "calendar";
  max_credits: number;
  scope?: "project" | "key";
}

export interface CreditRate {
  input_rate: number;
  output_rate: number;
  cache_creation_rate: number;
  cache_read_rate: number;
}

export interface ClassicRule {
  metric: "rpm" | "rpd" | "tpm" | "tpd";
  limit: number;
  per_model: boolean;
}

export interface RateLimitPolicy {
  id: string;
  project_id: string;
  name: string;
  is_default: boolean;
  credit_rules?: CreditRule[];
  model_credit_rates?: Record<string, CreditRate>;
  classic_rules?: ClassicRule[];
  starts_at?: string;
  expires_at?: string;
  created_at: string;
  updated_at: string;
}

// --- Plan ---
export interface Plan {
  id: string;
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
  is_active: boolean;
  created_at: string;
  updated_at: string;
}

// --- Subscription ---
export interface Subscription {
  id: string;
  project_id: string;
  plan_id?: string;
  plan_name: string;
  status: "active" | "expired" | "revoked";
  starts_at: string;
  expires_at: string;
  created_at: string;
  updated_at: string;
}

// --- Order ---
export interface Order {
  id: string;
  project_id: string;
  plan_id: string;
  periods: number;
  unit_price: number;
  amount: number;
  currency: string;
  status: "pending" | "paying" | "paid" | "delivered" | "failed" | "cancelled";
  channel?: string;
  payment_ref?: string;
  payment_url?: string;
  existing_subscription_id?: string;
  metadata?: string;
  created_at: string;
  updated_at: string;
}

// --- Usage ---
export interface UsageOverview {
  request_count: number;
  total_tokens: number;
  since: string;
  until: string;
}

export interface UsageSummary {
  model: string;
  request_count: number;
  total_input_tokens: number;
  total_output_tokens: number;
  total_cache_creation_tokens: number;
  total_cache_read_tokens: number;
  avg_latency_ms: number;
}

export interface DailyUsage {
  date: string;
  request_count: number;
  total_tokens: number;
}

export interface UsageByKey {
  api_key_id: string;
  api_key_name: string;
  key_suffix: string;
  request_count: number;
  total_tokens: number;
}

// --- Traces ---
export interface Trace {
  id: string;
  project_id: string;
  thread_id?: string;
  source: string;
  created_at: string;
  updated_at: string;
}

export interface Thread {
  id: string;
  project_id: string;
  created_at: string;
  updated_at: string;
}
