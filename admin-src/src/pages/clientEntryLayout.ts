export type EntryRowFilters = {
  query: string;
  status: 'all' | 'enabled' | 'disabled';
  kind: 'all' | 'standard' | 'split' | 'email' | 'user_id' | 'ua';
  membership: 'all' | 'grouped' | 'ungrouped';
};

function isSplitRow(row: any) {
  return row?.__row_kind === 'split_group' && Boolean(row?.__split_group);
}

function entryRowKey(row: any) {
  return isSplitRow(row) ? `split_group:${Number(row.__split_group.id)}` : `policy:${Number(row?.id)}`;
}

export function entryRuleName(row: any): string {
  const rule = isSplitRow(row) ? row.__split_group : row;
  return String(rule?.name || `规则 #${Number(rule?.id || 0)}`);
}

export function entryRuleHost(row: any): string {
  const rule = isSplitRow(row) ? row.__split_group : row;
  return String(rule?.entry_host || '').trim();
}

function conditionFields(value: unknown): Set<string> {
  let parsed = value;
  for (let attempt = 0; attempt < 3 && typeof parsed === 'string'; attempt += 1) {
    try {
      parsed = JSON.parse(parsed);
    } catch {
      return new Set();
    }
  }
  if (!Array.isArray(parsed)) return new Set();
  return new Set(parsed.filter((item) => item && typeof item === 'object').map((item) => String(item.field || '')));
}

export function filterEntryRows<T = any>(rows: T[], filters: EntryRowFilters): T[] {
  const query = String(filters.query || '').trim().toLocaleLowerCase();
  return rows.filter((value) => {
    const row = value as any;
    const enabled = row?.enabled !== false && Number(row?.enabled ?? 1) !== 0;
    if (filters.status === 'enabled' && !enabled) return false;
    if (filters.status === 'disabled' && enabled) return false;
    const split = isSplitRow(row);
    if (filters.kind === 'standard' && split) return false;
    if (filters.kind === 'split' && !split) return false;
    if (['email', 'user_id', 'ua'].includes(filters.kind) && (split || !conditionFields(row?.conditions).has(filters.kind))) return false;
    const grouped = Boolean(row?.__entry_collection);
    if (filters.membership === 'grouped' && !grouped) return false;
    if (filters.membership === 'ungrouped' && grouped) return false;
    if (!query) return true;
    const collection = row?.__entry_collection;
    return [entryRuleName(row), entryRuleHost(row), collection?.name, collection?.entry_host, entryRowKey(row)]
      .some((field) => String(field || '').toLocaleLowerCase().includes(query));
  });
}

export function moveEntryRow<T = any>(rows: T[], key: string, position1based: number): T[] {
  const output = rows.slice();
  if (!Number.isFinite(position1based)) return output;
  const source = output.findIndex((row) => entryRowKey(row) === key);
  if (source < 0) return output;
  const target = Math.max(0, Math.min(output.length - 1, Math.trunc(position1based) - 1));
  if (source === target) return output;
  const [row] = output.splice(source, 1);
  output.splice(target, 0, row);
  return output;
}

export function mergeEntryCollectionKeys(collectionItems: { kind: string; id: number }[], additionalKeys: string[]): string[] {
  const keys = new Set<string>();
  const append = (key: string) => {
    const match = /^(policy|split_group):([1-9]\d*)$/.exec(key);
    if (!match || !Number.isSafeInteger(Number(match[2]))) return;
    keys.add(`${match[1]}:${Number(match[2])}`);
  };
  collectionItems.forEach((item) => append(`${item.kind}:${item.id}`));
  additionalKeys.forEach(append);
  return [...keys];
}
