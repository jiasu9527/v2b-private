export type EntryUserEmailSnapshot = {
  policyID: number;
  name: string;
  conditionCount: number;
  emails: string[];
  text: string;
};

type ConfiguredCondition = { field?: unknown; operator?: unknown; values?: unknown };

function configuredConditions(value: unknown): ConfiguredCondition[] {
  let current = value;
  for (let index = 0; index < 3 && typeof current === 'string'; index += 1) {
    if (!current.trim()) return [];
    try { current = JSON.parse(current); } catch { throw new Error('规则条件格式异常，请刷新重试'); }
  }
  if (current === undefined || current === null) return [];
  if (!Array.isArray(current) || current.some((item) => !item || typeof item !== 'object' || Array.isArray(item))) {
    throw new Error('规则条件格式异常，请刷新重试');
  }
  return current;
}

function isStandardRule(rule: any) {
  return rule?.__row_kind !== 'split_group' && String(rule?.mode || 'standard').trim().toLowerCase() === 'standard';
}

function emailConditions(rule: any) {
  return configuredConditions(rule?.conditions).filter((condition) => condition.field === 'email' && condition.operator === 'in');
}

export function hasConfiguredEmailCondition(rule: any): boolean {
  if (!isStandardRule(rule)) return false;
  try { return emailConditions(rule).length > 0; } catch { return false; }
}

// This is the union of configured values, not an evaluation of AND conditions or user eligibility.
export function entryUserEmailSnapshot(policies: unknown, policyID: number): EntryUserEmailSnapshot {
  if (!Number.isSafeInteger(policyID) || policyID <= 0) throw new Error('规则 ID 无效，请关闭后重试');
  if (!Array.isArray(policies)) throw new Error('入口规则响应格式异常，请刷新重试');
  const matches = policies.filter((rule) => Number(rule?.id) === policyID);
  if (matches.length === 0) throw new Error('规则已删除或不存在，请关闭后刷新规则列表');
  if (matches.length !== 1) throw new Error('入口规则响应重复，请刷新重试');
  const rule = matches[0];
  if (!isStandardRule(rule)) throw new Error('规则已变为固定名单或其他类型，不能读取指定邮箱');
  const conditions = emailConditions(rule);
  if (conditions.length === 0) throw new Error('规则已不包含指定邮箱条件，请关闭后刷新规则列表');
  const emails = new Set<string>();
  for (const condition of conditions) {
    if (!Array.isArray(condition.values)) throw new Error('指定邮箱名单格式异常，请刷新重试');
    for (const raw of condition.values) {
      // Reject malformed records rather than silently exporting a partial or injected multi-line list.
      if (typeof raw !== 'string' || /[\u0000-\u001f\u007f-\u009f\u2028\u2029]/.test(raw)) {
        throw new Error('指定邮箱名单包含无效内容，请检查规则配置后重试');
      }
      const email = raw.trim().toLowerCase();
      if (!email) continue;
      if (!email.includes('@')) throw new Error('指定邮箱名单包含无效内容，请检查规则配置后重试');
      emails.add(email);
    }
  }
  const sorted = [...emails].sort();
  return { policyID, name: String(rule.name || '').trim() || `规则 #${policyID}`, conditionCount: conditions.length, emails: sorted, text: sorted.join('\n') };
}

export function entryUserEmailsFilename(policyID: number, name: string): string {
  if (!Number.isSafeInteger(policyID) || policyID <= 0) throw new Error('规则 ID 无效');
  const safeName = Array.from(String(name || '').trim()
    .replace(/[\u0000-\u001f\u007f-\u009f\u200b-\u200f\u2028-\u202e\u2066-\u2069/\\:*?"<>|]/g, '_')
    .replace(/\s+/g, '-').replace(/_+/g, '_')).slice(0, 60).join('').replace(/[. _-]+$/g, '') || 'rule';
  return `entry-${policyID}-${safeName}-emails.txt`;
}
