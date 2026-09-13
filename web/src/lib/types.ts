export type Page<T> = { data: T[]; has_more?: boolean; next_cursor?: string };

export type Balance = {
  available: number;
  held: number;
  currency: string;
  cached?: number;
  cache_agreed: boolean;
  ledger_balance: number;
};

export type Profile = {
  id: string;
  name: string;
  phone: string;
  kyc_tier: string;
  status: string;
  limits: {
    max_balance: number;
    max_per_transaction: number;
    max_monthly: number;
    used_this_month: number;
    currency: string;
  };
};

export type Activity = {
  id: string;
  kind: string;
  direction: "in" | "out";
  amount: number;
  currency: string;
  status: string;
  counterpart?: string;
  created_at: string;
};

export type Device = {
  id: string;
  platform: string;
  model?: string;
  trusted: boolean;
  last_seen_at?: string;
  revoked_at?: string;
  created_at: string;
  current: boolean;
};

export type Points = {
  balance: { points: number; expiring_soon: number; expires_at?: string; rate: string };
  history: { id: string; amount: number; kind: string; source?: string }[];
};

export type Biller = {
  code: string;
  name: string;
  category: string;
  admin_fee: number;
  prepaid: boolean;
};

export type Inquiry = {
  biller_code: string;
  biller_name: string;
  customer_ref: string;
  customer_name: string;
  period?: string;
  amount: number;
  admin_fee: number;
  total_payable: number;
};

export type Promo = {
  id: string;
  code: string;
  name: string;
  kind: string;
  value_bps?: number;
  value?: number;
  max_benefit?: number;
  min_spend: number;
  remaining_quota: number;
  status: string;
};

export type MoneyRequest = {
  id: string;
  requester_id: string;
  requester_name?: string;
  payer_id: string;
  amount: number;
  note?: string;
  status: string;
};

export type Split = {
  id: string;
  title: string;
  total: number;
  participants: number;
  share_each: number;
  requests: MoneyRequest[];
};

export type Notification = {
  id: string;
  kind: string;
  title: string;
  detail: string;
  amount?: number;
  unread: boolean;
  created_at: string;
};

export type Inbox = { data: Notification[]; unread: number; source: string };

export type Charge = {
  id: string;
  merchant_id: string;
  reference: string;
  description: string;
  amount: number;
  status: string;
  payment_id?: string;
  expires_at: string;
  expired: boolean;
  created_at: string;
};

export type Payment = {
  id: string;
  merchant_id: string;
  outlet_id?: string;
  method: string;
  amount: number;
  fee: number;
  net: number;
  status: string;
  ledger_transaction_id?: string;
  created_at: string;
};

export type PaymentPage = {
  data: Payment[];
  totals: {
    count: number;
    gross: number;
    fees: number;
    net: number;
    succeeded: number;
    failed: number;
  };
};

export type Payout = {
  id: string;
  merchant_id: string;
  payment_id?: string;
  mode: string;
  gross: number;
  holdback: number;
  net: number;
  rail?: string;
  status: string;
  latency_ms?: number;
  created_at: string;
  settled_at?: string;
};

export type PayoutPage = {
  data: Payout[];
  summary: {
    count: number;
    instant: number;
    batch: number;
    settled: number;
    failed: number;
    gross: number;
    holdback: number;
    net: number;
    median_latency_ms?: number;
    p95_latency_ms?: number;
  };
};

export type PayoutConfig = {
  merchant_id: string;
  score: number;
  holdback_bps: number;
  payout_mode: string;
  components: Record<string, number>;
  holdback_floor_bps: number;
  holdback_ceiling_bps: number;
  formula: string;
};

export type Settlement = {
  id: string;
  period_start: string;
  period_end: string;
  payout_count: number;
  gross: number;
  fee: number;
  net: number;
  status: string;
};

export type ApiKey = {
  id: string;
  name: string;
  prefix: string;
  mode: string;
  scopes: string[];
  secret?: string;
  last_used_at?: string;
  revoked_at?: string;
  created_at: string;
};

export type Outlet = {
  id: string;
  name: string;
  nmid: string;
  status: string;
  staff_count: number;
};

export type Staff = {
  id: string;
  outlet_id?: string;
  full_name: string;
  email: string;
  role: string;
  last_seen_at?: string;
  revoked_at?: string;
};

export type Delivery = {
  id: string;
  event_id: string;
  event_type: string;
  endpoint_url: string;
  attempt: number;
  status: string;
  response_code?: number;
  latency_ms?: number;
  created_at: string;
};

export type Hour = { hour: number; count: number; volume: number };

export type FloatView = {
  position: {
    outstanding: number;
    limit: number;
    utilisation_bps: number;
    instant_enabled: boolean;
    headroom: number;
    captured_at: string;
  };
  top_exposure: {
    merchant_id: string;
    display_name: string;
    outstanding: number;
    payouts: number;
  }[];
};

export type Engine = {
  window: string;
  instant: number;
  degraded_to_batch: number;
  settled: number;
  failed: number;
  sending: number;
  median_latency_ms?: number;
  p95_latency_ms?: number;
  p99_latency_ms?: number;
  rails: {
    rail: string;
    sent: number;
    settled: number;
    failed: number;
    p95_latency_ms?: number;
  }[];
  needing_attention: Payout[];
};

export type Reconciliation = {
  runs: {
    id: string;
    business_date: string;
    provider_code: string;
    rows_compared: number;
    rows_matched: number;
    discrepancies: number;
    delta: number;
    status: string;
  }[];
  open: {
    id: string;
    run_id: string;
    external_ref: string;
    internal?: number;
    provider?: number;
    delta: number;
    suspected_cause: string;
  }[];
};

export type AuditEntry = {
  id: number;
  actor: string;
  action: string;
  object_type: string;
  object_id: string;
  reason: string;
  ip?: string;
  created_at: string;
};

export type Hit = {
  kind: string;
  id: string;
  label: string;
  detail?: string;
  amount?: number;
  status: string;
  created_at: string;
};

export type LedgerView = {
  transaction_id: string;
  kind: string;
  entries: { id: string; account_id: string; owner_type: string; amount: number; created_at: string }[];
  sum: number;
  balanced: boolean;
};

export type AccountView = {
  account_id: string;
  owner_type: string;
  owner_id?: string;
  account_type: string;
  balance: number;
  entries: LedgerView["entries"];
};

export type Queues = {
  queues: {
    name: string;
    kind: string;
    waiting: number;
    failed: number;
    done: number;
    lag_seconds?: number;
    detail: string;
  }[];
  jobs: { name: string; schedule: string; last_run?: string; outcome: string; pending: number }[];
};

export type Transition = {
  id: number;
  operation_kind: string;
  operation_id: string;
  from_status?: string;
  to_status: string;
  actor: string;
  reason?: string;
  created_at: string;
};

export type Alert = {
  id: string;
  subject_type: string;
  subject_id: string;
  subject_name?: string;
  rule_code: string;
  rule: string;
  detail: string;
  observed?: number;
  severity: string;
  auto_action?: string;
  created_at: string;
};

export type AlertPage = { data: Alert[]; open: number; high_severity: number; last_24h: number };

export type Rule = {
  code: string;
  description: string;
  window_seconds: number;
  threshold: number;
  action: string;
  mode: string;
  trips_7d: number;
};

export type Dispute = {
  id: string;
  payment_id: string;
  user_id: string;
  merchant_id: string;
  amount: number;
  reason: string;
  status: string;
  covered_by_holdback: number;
  platform_loss: number;
  overdue: boolean;
};

export type Submission = {
  id: string;
  user_id: string;
  target_tier: string;
  match_score?: number;
  status: string;
  submitted_at: string;
  overdue: boolean;
};

export type Block = {
  id: string;
  subject_type: string;
  subject_id: string;
  reason: string;
  blocked_by: string;
  balance_held: number;
  appeal_status?: string;
  created_at: string;
  lifted_at?: string;
};

export type MerchantScore = {
  current: {
    merchant_id: string;
    score: number;
    holdback_bps: number;
    mode: string;
    components: Record<string, number>;
    computed_at: string;
  };
  history: { score: number; holdback_bps: number; computed_at: string }[];
};
