export const ENTRY_EVIDENCE_PATH = '/server/client-entry-user-policy/investigations';

export type EvidenceKind = 'policy' | 'split_group';
export type EvidenceFilters = {
  target: string; host: string; port?: number; from: number; to: number; ua: string; account: string;
};
export type EvidenceTargetOption = { value: string; label: string; host: string };
export type EvidencePage<T> = { items: T[]; total: number; current: number; page_size: number; warnings?: string[] };
export type EvidenceStatus = {
  enabled: boolean; keep_days: number; available_from?: number; available_to?: number;
  recorded?: number; write_failures?: number; warnings?: string[];
};
export type EvidenceDelivery = {
  id: string; user_id: number; email_at_event: string; ua: string; requested_at: number; completed_at: number;
  status: number; response_complete: boolean; coverage: string; coverage_reason: string; transport: string;
  node_count: number; bytes_written: number; write_error: string; nodes: EvidenceDeliveryNode[];
};
export type EvidenceDeliveryNode = {
  server_type: string; server_id: number; policy_id: number; group_id: number; rule_revision: string;
  source_host: string; delivered_host: string; port: string; resolved: boolean; collection_id: number; source: string;
};
export type EvidenceCandidate = {
  user_id: number; email: string; classification?: string; reasons?: string[]; request_count: number;
  first_completed_at: number; last_completed_at: number; selected?: boolean; branch?: string;
  nodes: { server_type: string; server_id: number }[]; uas: string[];
  evidence?: EvidenceReference[]; evidence_total?: number; evidence_truncated?: boolean;
};
export type EvidenceReference = { request_id: string; completed_at: number; ua: string; rule_revision: string; server_type: string; server_id: number; host: string; port: string };
export type EvidenceBranchObservation = {
  branch: string; policy_id: number; group_id: number; entry_host: string; fixed_users: number; acquired_users: number;
  no_recorded_delivery: number; first_delivery_at: number; last_delivery_at: number; enabled_at: number;
  observed_seconds: number; coverage_gap: boolean; refreshed_at: number;
};
export type EvidenceProbeEvent = {
  event_id: number; branch: 'A' | 'B'; target_id: number; probe_id: number; event_type: 'down' | 'recovered';
  happened_at: number; host: string; port: number; resolved_ip?: string;
};
export type EvidenceTargetSnapshot = {
  kind: EvidenceKind; target_id: number; policy_id: number; name: string; revision: string;
  entry_host: string; conditions: unknown[]; members: { server_type: string; server_id: number }[];
  collection_id?: number; enabled: number;
};
export type EvidenceCandidatePage = EvidencePage<EvidenceCandidate> & {
  target: EvidenceTargetSnapshot; query: Record<string, unknown>; supported: boolean;
  freeze_allowed?: boolean; unsupported_reason?: string; candidates_hash: string;
};
export type EvidenceContextUser = {
  user_id: number; email: string; classification: 'window_delivered' | 'historical_exposure' | 'request_only';
  first_at: number; last_at: number; request_count: number; orphaned: boolean;
};
export type EvidenceContextPage = EvidencePage<EvidenceContextUser> & {
  counts: { window_delivered: number; historical_exposure: number; request_only: number; orphaned: number };
};
export type EvidenceCase = {
  id: number; revision: number; title: string; status: 'frozen' | 'executed'; created_by: number;
  created_at: number; updated_at: number; parent_case_id?: number; target: EvidenceTargetSnapshot;
  query: { kind: EvidenceKind; target_id: number; host: string; port: number; from: number; to: number; ua?: string; account?: string };
  candidates_hash: string; total_candidates: number; selected_count: number; residual_count: number;
  source_user_count: number; source_users_revision: string; observation: string; conclusion?: string;
  preview?: EvidenceSplitPreview; execution?: Record<string, unknown>; probe_snapshot?: unknown;
  observations?: EvidenceBranchObservation[]; observation_warning?: string;
  probe_events?: EvidenceProbeEvent[]; probe_events_total?: number; probe_events_truncated?: boolean; probe_events_gap?: boolean;
};
export type EvidenceSplitInput = { case_id: number; revision: number; use_backup_ip_pool: boolean; entry_host_a?: string; entry_host_b?: string };
export type EvidenceSplitPreview = {
  case_id: number; revision: number; mode: 'overlay' | 'split_leaf'; supported: boolean; unsupported_reason?: string;
  groups: { branch: 'A' | 'B' | 'R'; user_count: number; entry_host: string; group_id?: number; policy_id?: number; unchanged: boolean }[];
  conditions: unknown[]; members: { server_type: string; server_id: number }[]; warnings?: string[];
};

export function evidenceDeepLink(search: string, hash: string, now: number): EvidenceFilters {
  const params = new URLSearchParams(hash.split('?')[1] || search);
  const safePositive = (name: string) => { const raw = params.get(name); if (!raw || !/^[1-9]\d*$/.test(raw)) return 0; const value = Number(raw); return Number.isSafeInteger(value) ? value : 0; };
  const group = safePositive('group_id'), policy = safePositive('policy_id');
  const from = safePositive('from'), to = safePositive('to'), port = safePositive('port');
  return { target: group ? `split_group:${group}` : policy ? `policy:${policy}` : '', host: params.get('host') || '', port: port || 54101, from: from || now - 86400, to: to || now, ua: '', account: '' };
}

export function evidenceTarget(value: string): { kind: EvidenceKind; target_id: number } | null {
  if (!value) return null;
  const match = /^(policy|split_group):([1-9]\d*)$/.exec(value);
  if (!match || !Number.isSafeInteger(Number(match[2]))) throw new Error('请选择有效的入口规则或固定子组');
  return { kind: match[1] as EvidenceKind, target_id: Number(match[2]) };
}

export function evidenceTargetOptions(rows: unknown[]): EvidenceTargetOption[] {
  const seen = new Set<string>();
  return rows.flatMap((item) => {
    if (!item || typeof item !== 'object') return [];
    const row = item as Record<string, unknown>;
    const group = row.__split_group as Record<string, unknown> | undefined;
    const id = Number(group?.id || row.id);
    if (!Number.isSafeInteger(id) || id <= 0) return [];
    const kind = group ? 'split_group' : 'policy';
    const value = `${kind}:${id}`;
    if (seen.has(value)) return [];
    seen.add(value);
    const collection = row.__entry_collection as Record<string, unknown> | undefined;
    const name = String(group?.name || row.name || `#${id}`);
    return [{ value, label: `${group ? '固定子组' : '普通规则'} · ${name} #${id}${Number(row.enabled) === 0 ? '（已停用）' : ''}`, host: String(collection?.entry_host || group?.entry_host || row.entry_host || '') }];
  });
}

export function evidenceFilterError(filters: EvidenceFilters, requireTarget = false): string {
  try { const target = evidenceTarget(filters.target); if (requireTarget && !target) return '请先指定需要排查的入口规则或固定子组'; } catch (error) { return (error as Error).message; }
  if (!Number.isSafeInteger(filters.from) || !Number.isSafeInteger(filters.to) || filters.from < 0 || filters.to <= filters.from) return '请选择有效的完整时间范围（开始含、结束不含）';
  if (filters.to - filters.from > 31 * 86400) return '单次查询时间范围最多 31 天';
  if (filters.port !== undefined && (!Number.isInteger(filters.port) || filters.port < 1 || filters.port > 65535)) return '端口须在 1～65535 之间';
  if (requireTarget && (!filters.host.trim() || filters.port === undefined)) return '筛选候选需要填写实际下发入口和端口（常用端口为 54101）';
  if (filters.host.trim().length > 255 || filters.ua.trim().length > 512 || filters.account.trim().length > 255) return '筛选条件过长，请缩短后重试';
  return '';
}

export function evidenceQuery(filters: EvidenceFilters, current = 1, pageSize = 20): Record<string, string | number> {
  const error = evidenceFilterError(filters);
  if (error) throw new Error(error);
  return {
    ...evidenceTarget(filters.target), from: filters.from, to: filters.to,
    ...(filters.host.trim() ? { host: filters.host.trim() } : {}), ...(filters.port !== undefined ? { port: filters.port } : {}),
    ...(filters.ua.trim() ? { ua: filters.ua.trim() } : {}), ...(filters.account.trim() ? { account: filters.account.trim() } : {}),
    current: Math.max(1, Math.floor(current) || 1), page_size: Math.min(100, Math.max(10, Math.floor(pageSize) || 20)),
  };
}

export function evidenceFilterKey(filters: EvidenceFilters): string {
  // Rendering dirty-state must remain safe while an input is incomplete.
  return JSON.stringify([filters.target, filters.host.trim(), filters.port ?? null, filters.from, filters.to, filters.ua.trim(), filters.account.trim()]);
}

export function evidenceSelectedIDs(keys: (string | number | bigint)[]): number[] {
  const ids = keys.map(Number);
  if (ids.some((id) => !Number.isSafeInteger(id) || id <= 0)) throw new Error('选中的用户名单无效，请重新查询');
  return [...new Set(ids)].sort((a, b) => a - b);
}

export function evidenceFreezePayload(filters: EvidenceFilters, revision: string, candidatesHash: string, title: string, selected?: number[]) {
  const error = evidenceFilterError(filters, true);
  if (error) throw new Error(error);
  if (!revision) throw new Error('缺少规则版本，请重新查询候选名单');
  if (!candidatesHash) throw new Error('缺少候选名单版本，请重新查询');
  if (selected && evidenceSelectedIDs(selected).length < 1) throw new Error('至少选择 1 位用户；空选中名单不会按全部用户处理');
  const query = evidenceQuery(filters);
  const { current: _current, page_size: _pageSize, ...scope } = query;
  return { ...scope, revision, expected_candidates_hash: candidatesHash, title: title.trim(), ...(selected ? { selected_user_ids: evidenceSelectedIDs(selected) } : {}) };
}

export function evidenceSplitPayload(record: EvidenceCase, pool: boolean, hostA: string, hostB: string): EvidenceSplitInput {
  if (!(record.id > 0) || !(record.revision > 0)) throw new Error('档案版本无效，请重新打开');
  const entry_host_a = hostA.trim(), entry_host_b = hostB.trim();
  if (!pool && (!entry_host_a || !entry_host_b || entry_host_a.toLowerCase() === entry_host_b.toLowerCase())) throw new Error('请填写两个不同的子组入口');
  return { case_id: record.id, revision: record.revision, use_backup_ip_pool: pool, ...(!pool ? { entry_host_a, entry_host_b } : {}) };
}

export function evidenceTime(value?: number): string { return value && value > 0 ? new Date(value * 1000).toLocaleString() : '—'; }
export function evidenceDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—';
  if (seconds < 60) return `${Math.floor(seconds)} 秒`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时 ${Math.floor(seconds % 3600 / 60)} 分钟`;
  return `${Math.floor(seconds / 86400)} 天 ${Math.floor(seconds % 86400 / 3600)} 小时`;
}
export const EVIDENCE_CONCLUSIONS = ['正常', '单组复现', '两组异常', '未复现', '证据不足'];
export const EVIDENCE_CLASSIFICATIONS = { window_delivered: '窗口内完整下发', historical_exposure: '窗口前历史暴露', request_only: '仅请求记录，入口未确认' };
export type EvidenceCaseTreeNode = { record: EvidenceCase; children: EvidenceCaseTreeNode[]; unloadedParentID?: number };
export function evidenceCaseTree(records: EvidenceCase[]): EvidenceCaseTreeNode[] {
  const nodes = new Map(records.map((record) => [record.id, { record, children: [] } as EvidenceCaseTreeNode]));
  const roots: EvidenceCaseTreeNode[] = [];
  for (const node of nodes.values()) {
    const parentID = node.record.parent_case_id;
    const visited = new Set([node.record.id]);
    let ancestor = parentID, cycle = false;
    while (ancestor && nodes.has(ancestor)) {
      if (visited.has(ancestor)) { cycle = true; break; }
      visited.add(ancestor); ancestor = nodes.get(ancestor)?.record.parent_case_id;
    }
    if (parentID && nodes.has(parentID) && !cycle) nodes.get(parentID)!.children.push(node);
    else { if (parentID && !nodes.has(parentID)) node.unloadedParentID = parentID; roots.push(node); }
  }
  return roots;
}
export function evidenceResultLabel(value: Pick<EvidenceDelivery, 'status' | 'response_complete' | 'coverage'>): string {
  if (value.status === 304) return '304 未下发新正文';
  if (value.status < 200 || value.status >= 300) return `HTTP ${value.status} · 非有效下发`;
  if (!value.response_complete) return '响应未完成';
  if (value.coverage === 'partial') return '服务端写完 · 证据部分覆盖';
  if (value.coverage === 'unavailable' || value.coverage === 'not_emitted') return '无完整节点下发证据';
  return '服务端写入成功';
}
