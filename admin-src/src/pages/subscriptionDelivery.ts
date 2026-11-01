export const SUBSCRIPTION_DELIVERY_PATH = '/server/client-entry-user-policy/investigations/';
export const DELIVERY_PROTOCOLS = ['shadowsocks', 'vmess', 'vless', 'trojan', 'tuic', 'hysteria', 'hysteria2', 'anytls', 'v2node', 'juicity', 'mieru', 'socks', 'http'];

export type DeliveryFilters = {
  host: string; port: string; account: string; ua: string;
  from?: number; to?: number; policy_id?: number; group_id?: number; collection_id?: number;
  server_type: string; server_id?: number; rule_revision: string; version?: string;
};
export type DeliveryMeta = { keep_days: number; available_from: number; available_to: number; warnings?: string[] };
export type DeliveryStatistics = DeliveryMeta & { users: number; requests: number; first_at: number; last_at: number };
export type DeliveryUser = { user_id: number; email_at_event: string; requests: number; first_at: number; last_at: number };
export type DeliveryVersion = {
  policy_id: number; group_id: number; collection_id: number; provider_id: number; rule_revision: string; version: string; resolved: boolean;
  source_host: string; delivered_host: string; port: string; source: string;
  users: number; requests: number; first_at: number; last_at: number;
};
export type DeliveryPage<T> = DeliveryMeta & { items: T[]; total: number; current: number; page_size: number };

export function initialDeliveryFilters(now = Math.floor(Date.now() / 1000)): DeliveryFilters {
  return { host: '', port: '54101', account: '', ua: '', from: now - 86400, to: now, server_type: '', rule_revision: '' };
}
export function normalizedDeliveryFilters(filters: DeliveryFilters): DeliveryFilters {
  return { ...filters, host: filters.host.trim(), port: filters.port.trim(), account: filters.account.trim(), ua: filters.ua.trim(), server_type: filters.server_type.trim(), rule_revision: filters.rule_revision.trim() };
}
export function deliveryFilterKey(filters: DeliveryFilters): string { return JSON.stringify(normalizedDeliveryFilters(filters)); }
export function deliveryFilterError(filters: DeliveryFilters): string {
  const f = normalizedDeliveryFilters(filters);
  if ((f.from === undefined) !== (f.to === undefined)) return '请选择完整的开始和结束时间，或清空时间查询全部保留期';
  if (f.from !== undefined && (!Number.isSafeInteger(f.from) || !Number.isSafeInteger(f.to) || f.from < 0 || f.to <= f.from)) return '结束时间必须晚于开始时间';
  if (f.host.length > 255 || /[\s/@?#]/.test(f.host)) return '入口只填写域名或 IP，不填写订阅链接、协议和路径';
  if (f.port && !/^[0-9,\-]{1,64}$/.test(f.port)) return '端口须为数字或范围，如 54101、54101-54110；留空不限';
  if (f.account.length > 254 || f.ua.length > 1024) return '账号或 UA 条件过长';
  for (const id of [f.policy_id, f.group_id, f.collection_id]) {
    if (id !== undefined && (!Number.isSafeInteger(id) || id < 1)) return '规则、叶子、合集 ID 必须为正整数';
  }
  if (f.server_id !== undefined && (!Number.isSafeInteger(f.server_id) || f.server_id === 0)) return '节点 ID 必须为非零整数；额外节点可为负数';
  if (f.server_type && !DELIVERY_PROTOCOLS.includes(f.server_type)) return '节点协议无效';
  if (f.rule_revision && !/^[a-f0-9]{64}$/.test(f.rule_revision)) return '下发快照版本必须为完整的 64 位版本标识';
  if (f.version && !/^[a-f0-9]{32}$/.test(f.version)) return '入口快照筛选标识无效，请重新查询版本列表';
  return '';
}
export function deliveryParams(filters: DeliveryFilters, current = 1, pageSize = 20): Record<string, string | number> {
  const error = deliveryFilterError(filters);
  if (error) throw new Error(error);
  const size = Math.max(1, Math.min(100, Math.floor(pageSize) || 20));
  const page = Math.max(1, Math.floor(current) || 1);
  if ((page - 1) * size > 10000) throw new Error('翻页超过查询上限，请缩小时间范围或增加筛选条件');
  const params: Record<string, string | number> = { current: page, page_size: size };
  for (const [key, value] of Object.entries(normalizedDeliveryFilters(filters))) {
    if (value !== undefined && value !== '') params[key] = value;
  }
  return params;
}
export function deliveryVersionKey(version: DeliveryVersion): string {
  return version.version || JSON.stringify([version.policy_id, version.group_id, version.collection_id, version.provider_id, version.rule_revision, version.source_host, version.delivered_host, version.port, version.source, version.resolved]);
}
export function deliveryVersionFilters(filters: DeliveryFilters, version: DeliveryVersion): DeliveryFilters {
  if (!/^[a-f0-9]{64}$/.test(version.rule_revision || '')) throw new Error('这条历史记录没有下发快照版本，不能当作精确版本筛选');
  if (!/^[a-f0-9]{32}$/.test(version.version || '')) throw new Error('入口快照筛选标识无效，请更新后端并重新查询');
  return normalizedDeliveryFilters({
    ...filters, version: version.version,
  });
}
export function deliveryTime(value: number): string { return value > 0 ? new Date(value * 1000).toLocaleString() : '—'; }
export function deliveryPage<T>(value: unknown): DeliveryPage<T> {
  const page = value as DeliveryPage<T>;
  if (!page || !Array.isArray(page.items) || !Number.isSafeInteger(page.total) || page.total < 0 || !Number.isSafeInteger(page.current) || page.current < 1 || !Number.isSafeInteger(page.page_size) || page.page_size < 1 || page.page_size > 100) throw new Error('下发分页返回格式无效，请更新后端后重试');
  return page;
}
export function deliveryStatistics(value: unknown): DeliveryStatistics {
  const result = value as DeliveryStatistics;
  if (!result || [result.users, result.requests, result.first_at, result.last_at].some((v) => !Number.isSafeInteger(v) || v < 0)) throw new Error('下发统计返回格式无效，请更新后端后重试');
  return result;
}
