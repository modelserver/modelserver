export interface Tenant {
  id: string;
  name: string;
  callback_url: string;
  description: string;
  is_active: boolean;
  created_at: string;
  updated_at: string;
}

export interface Payment {
  id: string;
  tenant_id: string;
  order_id: string;
  channel: string;
  trade_no: string;
  payment_url: string;
  amount: number;
  status: string;
  callback_status: string;
  callback_retries: number;
  raw_notify: string | null;
  paid_at: string | null;
  created_at: string;
  updated_at: string;
}
