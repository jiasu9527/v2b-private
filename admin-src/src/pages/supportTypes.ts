export type SupportEntryTarget = {
  key: string;
  name: string;
  policy_id: number;
  group_id?: number;
  entry_host: string;
  resolve_entry_host?: boolean | number;
};

export type SupportOverride = {
  target_key: string;
  target_name?: string;
  entry_host?: string;
  active: boolean;
  status: string;
  expires_at: number;
};

export type SupportScope = {
  source_policy_id: number;
  name: string;
  automatic_entry_host: string;
  version: number;
  override?: SupportOverride | null;
  targets: SupportEntryTarget[];
  nodes?: { type: string; id: number; name: string }[];
  applicable?: boolean;
};

export type SupportUserResult = {
  found: boolean;
  user?: { id: number; email: string; plan_name: string; expired_at: number | null; remaining_bytes: number; banned?: boolean };
  scopes: SupportScope[];
  entitlement?: SupportEntitlementSnapshot | null;
  entry_membership?: SupportEntryMembership | null;
};

export type SupportEntitlementPlan = { id: number; name: string; transfer_enable: number };
export type SupportEntitlementSnapshot = {
  user_id: number;
  plan_id: number | null;
  plan_name: string;
  group_id: number | null;
  transfer_enable: number;
  u: number;
  d: number;
  expired_at: number | null;
  device_limit: number | null;
  speed_limit: number | null;
  version: string;
};
export type SupportMembershipGroup = {
  policy_id: number;
  name: string;
  entry_host: string;
  enabled: boolean;
  effective: boolean;
};
export type SupportEntryMembership = {
  version: number;
  current_groups: SupportMembershipGroup[];
  targets: SupportEntryTarget[];
  has_legacy_override: boolean;
};

export type SupportOperation = {
  id: number;
  actor_id: number;
  actor_email: string;
  target_user_id: number;
  target_email: string;
  source_policy_id: number;
  kind?: string;
  reason: string;
  before_state: Record<string, any>;
  after_state: Record<string, any>;
  created_at: number;
};

export type SupportHealthStatus = 'healthy' | 'partial' | 'unhealthy' | 'stale' | 'unmonitored' | 'disabled';
export type SupportHealthProbe = {
  probe_id: number;
  probe_name: string;
  online: boolean;
  status: SupportHealthStatus;
  status_reason?: string;
  last_success?: boolean | null;
  last_latency_ms?: number | null;
  last_reported_at?: number | null;
  last_error?: string;
  last_resolved_ip?: string;
  source_count?: number;
};
export type SupportHealthSource = {
  policy_id: number;
  policy_name: string;
  source_key: string;
  name: string;
  policy_enabled: boolean;
  monitor_enabled: boolean;
  monitored: boolean;
  check_interval_sec: number;
  tcp_timeout_ms: number;
  failure_threshold: number;
  success_threshold: number;
  status: SupportHealthStatus;
  status_reason?: string;
  probes: SupportHealthProbe[];
};
export type SupportHealthEntry = {
  key: string;
  host: string;
  port: number;
  protocol: string;
  status: SupportHealthStatus;
  status_reason?: string;
  sources: SupportHealthSource[];
  probes: SupportHealthProbe[];
  last_reported_at?: number | null;
};
export type SupportOverview = {
  coverage: string;
  coverage_note: string;
  generated_at: number;
  summary: Record<string, number>;
  items: SupportHealthEntry[];
};

export function supportDate(value?: number | null) {
  return value && Number(value) > 0 ? new Date(Number(value) * 1000).toLocaleString() : '—';
}
