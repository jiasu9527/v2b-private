export type CommissionVisibility = 'active' | 'deleted' | 'all';
export type CommissionFilter = { id?: number; ids?: number[]; invite_user_id?: number; user_id?: number; trade_no?: string; created_from?: number; created_to?: number; commission_status?: number; visibility?: CommissionVisibility };
export type CommissionDraft = { action: 'delete' | 'restore'; filter: CommissionFilter; singleID?: number };

export function hasCommissionSelection(filter: CommissionFilter) {
  return ['id', 'invite_user_id', 'user_id', 'created_from', 'created_to', 'commission_status'].some((key) => Number.isInteger((filter as any)[key])) || !!filter.trade_no?.trim() || !!filter.ids?.length;
}

// Build retries from the frozen preview, never from the current table filter.
export function commissionApplyRequest(draft: CommissionDraft, preview: { action: string; preview_hash: string; all_records: boolean }, idempotencyKey: string, confirmAll: boolean) {
  if (draft.action !== preview.action || !preview.preview_hash || !idempotencyKey || (preview.all_records && !confirmAll)) throw new Error('请核对预览并确认操作范围');
  const path = draft.action === 'restore' ? 'restore' : draft.singleID ? 'delete' : 'delete-many';
  return { path: `/commission-log/${path}`, body: { preview_hash: preview.preview_hash, idempotency_key: idempotencyKey, confirm: true, confirm_all: confirmAll, ...(draft.singleID ? { id: draft.singleID } : {}) } };
}
