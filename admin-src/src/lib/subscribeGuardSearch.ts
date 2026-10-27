export type SubscribeGuardFilters = {
  account: string;
  account_match: 'contains' | 'exact';
  entry: string;
  ua: string;
  ua_match: 'contains' | 'equals' | 'not_contains';
  start_at?: number;
  end_at?: number;
  result: 'all' | 'allowed' | 'blocked';
  sort: 'last_at' | 'count';
};

export type EntrySearchOption = { value: string; label: string; disabled: boolean };

export function emptyGuardFilters(): SubscribeGuardFilters {
  return { account: '', account_match: 'contains', entry: '', ua: '', ua_match: 'contains', result: 'all', sort: 'last_at' };
}

export function normalizedGuardFilters(filters: SubscribeGuardFilters): SubscribeGuardFilters {
  return { ...filters, account: filters.account.trim(), ua: filters.ua.trim(), entry: filters.entry || '' };
}

export function entrySearchSelection(value: string): { policy_id?: number; group_id?: number } {
  if (!value) return {};
  const match = /^policy:(\d+)(?:\/group:(\d+))?$/.exec(value);
  if (!match || !(Number(match[1]) > 0) || (match[2] && !(Number(match[2]) > 0))) throw new Error('请选择有效的入口规则');
  return { policy_id: Number(match[1]), ...(match[2] ? { group_id: Number(match[2]) } : {}) };
}

export function guardFilterError(input: SubscribeGuardFilters): string {
  const filters = normalizedGuardFilters(input);
  try { entrySearchSelection(filters.entry); } catch (error: any) { return error.message; }
  const { start_at: start, end_at: end } = filters;
  if ((start === undefined) !== (end === undefined)) return '请选择完整的开始时间和结束时间';
  if (start !== undefined && (!Number.isSafeInteger(start) || !Number.isSafeInteger(end) || start < 0 || end! <= start)) return '结束时间必须晚于开始时间';
  if (!filters.account && !filters.entry && !filters.ua && start === undefined && filters.result === 'all') return '请至少填写一项筛选条件，也可以选择最近 24 小时';
  return '';
}

export function guardSearchParams(filters: SubscribeGuardFilters, current = 1, pageSize = 20): Record<string, string | number> {
  const normalized = normalizedGuardFilters(filters);
  const error = guardFilterError(normalized);
  if (error) throw new Error(error);
  const params: Record<string, string | number> = {
    ...entrySearchSelection(normalized.entry), result: normalized.result, sort: normalized.sort,
    current: Math.max(1, Math.floor(current) || 1), page_size: Math.max(1, Math.min(100, Math.floor(pageSize) || 20)),
  };
  if (normalized.account) { params.account = normalized.account; params.account_match = normalized.account_match; }
  if (normalized.ua) { params.ua = normalized.ua; params.ua_match = normalized.ua_match; }
  if (normalized.start_at !== undefined) { params.start_at = normalized.start_at; params.end_at = normalized.end_at!; }
  return params;
}

export function guardFilterFingerprint(filters: SubscribeGuardFilters): string {
  const next = normalizedGuardFilters(filters);
  return JSON.stringify([next.account, next.account ? next.account_match : '', next.entry, next.ua, next.ua ? next.ua_match : '', next.start_at ?? null, next.end_at ?? null, next.result, next.sort]);
}

export function entrySearchOptions(policies: any): EntrySearchOption[] {
  if (!Array.isArray(policies)) return [];
  return policies.flatMap((policy) => {
    const id = Number(policy?.id);
    if (!Number.isSafeInteger(id) || id <= 0) return [];
    const split = String(policy.mode).toLowerCase() === 'split';
    const disabled = !Number(policy.enabled);
    const name = String(policy.name || `规则 #${id}`);
    const suffix = disabled ? '（已停用）' : '';
    const options: EntrySearchOption[] = [{ value: `policy:${id}`, label: `${split ? '固定分组' : '普通规则'} · ${name}${split ? ' · 全部子组' : ''}${suffix}`, disabled }];
    if (split && Array.isArray(policy.split_groups)) {
      policy.split_groups.filter((group: any) => group.is_leaf === true || Number(group.is_leaf) === 1)
        .slice().sort((a: any, b: any) => Number(a.sort || 0) - Number(b.sort || 0) || String(a.path || '').localeCompare(String(b.path || ''), undefined, { numeric: true }))
        .forEach((group: any) => {
          const groupID = Number(group.id);
          if (!Number.isSafeInteger(groupID) || groupID <= 0) return;
          options.push({ value: `policy:${id}/group:${groupID}`, label: `子组 · ${name} / ${group.name || group.path || `#${groupID}`}${suffix}`, disabled });
        });
    }
    return options;
  });
}
