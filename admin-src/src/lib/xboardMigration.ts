import { clearAuth, getAdminPath, getAuth } from './api';

export type XboardSource = {
  host: string;
  port: number;
  database: string;
  username: string;
  password: string;
  tls: boolean;
};
export type LockingBalancePolicy = 'skip' | 'to_balance' | 'discard';
export type MigrationPlan = {
  id: number;
  name: string;
  users: number;
  transfer_enable: number;
  group_id: number;
  reset_traffic_method: number | null;
};
export type MigrationInspection = {
  total_users: number;
  source_admins: number;
  source_staff: number;
  locking_balance_users: number;
  locking_balance_total: number;
  source_plans: MigrationPlan[];
  target_plans: MigrationPlan[];
  warnings: string[];
};
export type MigrationCounts = Record<string, number>;
export type MigrationPreviewRequest = {
  source: XboardSource;
  plan_mapping: Record<string, number>;
  locking_balance_policy: LockingBalancePolicy;
  confirm_locking_balance: boolean;
  regenerate_tokens: boolean;
  regenerate_uuids: boolean;
};
export type MigrationPreview = {
  preview_id: string;
  expires_at: number;
  counts: MigrationCounts;
  blockers: string[];
  warnings: string[];
  can_apply: boolean;
  source_digest: string;
  locking_balance_policy: LockingBalancePolicy;
  regenerate_tokens?: boolean;
  regenerate_uuids?: boolean;
};
export type MigrationReceipt = {
  preview_id: string;
  status: string;
  imported: number;
  counts: MigrationCounts;
  source_digest: string;
  locking_balance_policy: LockingBalancePolicy;
  regenerate_tokens?: boolean;
  regenerate_uuids?: boolean;
  created_at: number;
  completed_at: number;
  mail_tracking_available?: boolean;
};

export type MigrationMailTask = {
  request_id: string;
  preview_id: string;
  status: 'queued' | 'running' | 'completed' | 'partial_failed' | 'interrupted' | 'queue_failed';
  recipients: number;
  sent: number;
  failed: number;
  interval_seconds: number;
  created_at: number;
  updated_at: number;
};
export type MigrationMailInfo = {
  preview_id: string;
  imported: number;
  recipients: number;
  tracking_available: boolean;
  configured: boolean;
  queue_available: boolean;
  interval_seconds: number;
  task: MigrationMailTask | null;
};
export type MigrationMailRequest = {
  preview_id: string;
  request_id: string;
  subject: string;
  content: string;
  confirm: true;
};

export class MigrationRequestError extends Error {
  constructor(message: string, public status: number, public code = '') {
    super(message);
    this.name = 'MigrationRequestError';
  }
}

// Do not reuse the generic admin helpers: credentials and auth must never enter URLs.
async function request<T>(path: string, body?: unknown): Promise<T> {
  const auth = getAuth();
  const res = await fetch(`/api/v1/${getAdminPath()}/migration/xboard${path}`, {
    method: body === undefined ? 'GET' : 'POST',
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
    headers: {
      Accept: 'application/json',
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(auth ? { Authorization: /^Bearer\s/i.test(auth) ? auth : `Bearer ${auth}` } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 401) {
    clearAuth();
    window.dispatchEvent(new Event('forest-auth-invalid'));
  }
  let payload: any;
  try { payload = await res.json(); }
  catch { throw new MigrationRequestError(`服务器未返回有效结果（HTTP ${res.status}），请检查服务状态`, res.status); }
  if (!res.ok) {
    const message = payload?.message || payload?.error;
    throw new MigrationRequestError(typeof message === 'string' ? message : `请求失败（HTTP ${res.status}）`, res.status, typeof payload?.code === 'string' ? payload.code : '');
  }
  return payload?.data ?? payload;
}

export const inspectXboard = (source: XboardSource) => request<MigrationInspection>('/inspect', source);
export const previewXboard = (body: MigrationPreviewRequest) => request<MigrationPreview>('/preview', body);
export const applyXboard = (previewID: string) => request<MigrationReceipt>('/apply', { preview_id: previewID, confirm: true });
export const getXboardReceipt = (previewID: string) => request<MigrationReceipt>(`/receipt?preview_id=${encodeURIComponent(previewID)}`);
export const getXboardMailInfo = (previewID: string) => request<MigrationMailInfo>(`/mail/status?preview_id=${encodeURIComponent(previewID)}`);
export const sendXboardMail = (body: MigrationMailRequest) => request<MigrationMailTask>('/mail/send', body);
